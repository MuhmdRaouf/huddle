package router

// The HTTPS proxy mode: api.anthropic.com is decrypted with the local CA and routed exactly as
// base-URL mode routes it, every other host is tunnelled blind, and a tunnel to this machine is
// refused. The client is Claude Code in proxy mode: HTTPS_PROXY at the router, the router's CA
// as a root.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// tlsProxyRig is one TLSProxy served on a testkit port, with the CA the client trusts read back
// from the same directory the proxy loads.
type tlsProxyRig struct {
	port  int
	seen  *seenList
	log   *lineLog
	caPEM []byte
}

// lineLog records the proxy's lines concurrently.
type lineLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lineLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *lineLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func newTLSProxyRig(t *testing.T, mutate func(*TLSProxy)) *tlsProxyRig {
	t.Helper()
	rig := &tlsProxyRig{seen: &seenList{}, log: &lineLog{}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		rig.seen.record(req.URL.Path, req.Header, string(body))
		_, _ = w.Write([]byte(`{"passthrough":true}`))
	}))
	t.Cleanup(upstream.Close)
	caDir := t.TempDir()
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), caDir, time.Now())
	if err != nil {
		t.Fatalf("the local CA: %v", err)
	}
	rig.caPEM = ca.CertPEM()

	proxy := &TLSProxy{
		Name:      "test-router",
		CADir:     caDir,
		BodyLimit: BodyLimitBytes,
		Anthropic: mustURL(t, upstream.URL),
		Now:       func() int64 { return time.Now().UnixMilli() },
		Note:      rig.log.add,
		Route: func(req *http.Request, res *Response, body []byte) {
			rig.seen.record(req.URL.Path, req.Header, string(body))
			res.Header().Set("Content-Type", "application/json")
			res.WriteHeader(200)
			res.End([]byte(`{"routed":true}`))
		},
	}
	if mutate != nil {
		mutate(proxy)
	}
	server := Harden(&http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodConnect {
			proxy.Connect(rw, req)
			return
		}
		AnthropicError(NewResponse(rw), 404, "not_found_error", "no plain requests here")
	})})
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports.Free(t)))
	if err != nil {
		t.Fatal(err)
	}
	rig.port = listener.Addr().(*net.TCPAddr).Port
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return rig
}

// proxiedClient is Claude Code in proxy mode: the router is the proxy and the router's CA the
// only root, so only a decrypted CONNECT answers.
func (r *tlsProxyRig) proxiedClient(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(r.caPEM) {
		t.Fatal("the CA's PEM did not parse")
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", r.port)}, nil
			},
			TLSClientConfig:   &tls.Config{RootCAs: pool},
			DisableKeepAlives: true,
		},
	}
}

func TestTLSProxyDecryptsAndRoutesAnthropicOnly(t *testing.T) {
	t.Parallel()
	rig := newTLSProxyRig(t, nil)
	client := rig.proxiedClient(t)

	t.Run("a model request is decrypted and routed", func(t *testing.T) {
		resp, err := client.Post("https://api.anthropic.com/v1/messages", "application/json",
			strings.NewReader(`{"model":"claude-opus-5-5","messages":[]}`))
		if err != nil {
			t.Fatalf("post through the proxy: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("the routed request = %d, want 200", resp.StatusCode)
		}
		seen := rig.seen.list()
		if len(seen) != 1 || seen[0].path != "/v1/messages" {
			t.Fatalf("the route saw %v, want /v1/messages", seen)
		}
		if !strings.Contains(seen[0].body, "claude-opus-5-5") {
			t.Errorf("the routed body = %q, want the request's", seen[0].body)
		}
	})

	t.Run("any other request passes through to Anthropic", func(t *testing.T) {
		resp, err := client.Get("https://api.anthropic.com/v1/me")
		if err != nil {
			t.Fatalf("get through the proxy: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("the passthrough = %d, want 200", resp.StatusCode)
		}
		seen := rig.seen.list()
		if len(seen) == 0 || seen[len(seen)-1].path != "/v1/me" {
			t.Errorf("the passthrough saw %v, want /v1/me last", seen)
		}
	})

	t.Run("the count_tokens path is routed too", func(t *testing.T) {
		resp, err := client.Post("https://api.anthropic.com/v1/messages/count_tokens", "application/json",
			strings.NewReader(`{"model":"claude-opus-5-5"}`))
		if err != nil {
			t.Fatalf("post through the proxy: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("the routed request = %d, want 200", resp.StatusCode)
		}
	})
}

func TestTLSProxyTunnelsEveryOtherHostBlind(t *testing.T) {
	t.Parallel()
	clientEnd, proxyEnd := net.Pipe()
	rig := newTLSProxyRig(t, func(p *TLSProxy) {
		p.Dial = func(context.Context, string, string) (net.Conn, error) { return proxyEnd, nil }
	})

	t.Run("the tunnel carries both ways", func(t *testing.T) {
		conn, answer := connectThrough(t, rig.port, "someone-else.example:443")
		defer conn.Close()
		if answer.StatusCode != 200 {
			t.Fatalf("the CONNECT = %d, want 200 established", answer.StatusCode)
		}
		if _, err := conn.Write([]byte("a client hello")); err != nil {
			t.Fatal(err)
		}
		_ = clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		one := make([]byte, 14)
		if _, err := io.ReadFull(clientEnd, one); err != nil {
			t.Fatalf("the tunnel never reached the dial: %v", err)
		}
		if string(one) != "a client hello" {
			t.Errorf("the tunnel carried %q", one)
		}
		if _, err := clientEnd.Write([]byte("an upstream hello")); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		two := make([]byte, 17)
		if _, err := io.ReadFull(conn, two); err != nil {
			t.Fatalf("the tunnel never reached the client: %v", err)
		}
		if string(two) != "an upstream hello" {
			t.Errorf("the tunnel carried %q", two)
		}
	})

	t.Run("an answer the CA cannot sign passes through blind too", func(t *testing.T) {
		strict := newTLSProxyRig(t, func(p *TLSProxy) {
			p.CADir = "/no-such-ca-dir/anywhere"
			p.Dial = func(context.Context, string, string) (net.Conn, error) { return clientEnd2(t), nil }
		})
		conn, answer := connectThrough(t, strict.port, "api.anthropic.com:443")
		defer conn.Close()
		if answer.StatusCode != 200 {
			t.Fatalf("the CONNECT = %d, want 200 established", answer.StatusCode)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(strict.log.joined(), "local CA unusable") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !strings.Contains(strict.log.joined(), "local CA unusable") {
			t.Errorf("the notes = %q, want the CA's failure", strict.log.joined())
		}
	})
}

// clientEnd2 is one more pipe end for the second rig's tunnel.
func clientEnd2(t *testing.T) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close() })
	return b
}

func TestTLSProxyRefusesWhatItMustNotOpen(t *testing.T) {
	t.Parallel()
	rig := newTLSProxyRig(t, nil)

	t.Run("a CONNECT to this machine", func(t *testing.T) {
		for _, target := range []string{"127.0.0.1:20000", "localhost:443", "mine.localhost:443", "[::1]:443"} {
			conn, answer := connectThrough(t, rig.port, target)
			body := refuseBody(t, answer)
			_ = conn.Close()
			if answer.StatusCode != 403 || !strings.Contains(body, "CONNECT refused") {
				t.Errorf("CONNECT %s = %d %q, want the refusal", target, answer.StatusCode, body)
			}
		}
	})

	t.Run("a CONNECT that names no host and port", func(t *testing.T) {
		conn, answer := connectThrough(t, rig.port, "just-a-name")
		body := refuseBody(t, answer)
		_ = conn.Close()
		if answer.StatusCode != 403 {
			t.Errorf("CONNECT = %d %q, want the refusal", answer.StatusCode, body)
		}
	})
}

func TestTLSProxyNamesAnUnreachableAnthropic(t *testing.T) {
	t.Parallel()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := dead.URL
	dead.Close() // the port answers nothing now
	rig := newTLSProxyRig(t, func(p *TLSProxy) { p.Anthropic = mustURL(t, url) })
	client := rig.proxiedClient(t)
	resp, err := client.Get("https://api.anthropic.com/v1/me")
	if err != nil {
		t.Fatalf("get through the proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("the passthrough = %d, want 502", resp.StatusCode)
	}
}

func TestOneConnListenerAnswersItsOwnAddress(t *testing.T) {
	t.Parallel()
	a, b := net.Pipe()
	defer a.Close()
	listener := &oneConnListener{conn: b}
	if listener.Addr() != b.LocalAddr() {
		t.Errorf("the listener's address = %v, want the connection's", listener.Addr())
	}
	if err := listener.Close(); err != nil {
		t.Errorf("the listener's close: %v", err)
	}
}

func TestTLSProxyDrainWaitsForTheTunnelsItCarries(t *testing.T) {
	t.Parallel()

	t.Run("a decrypted answer in flight finishes, then its tunnel closes", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var proxy *TLSProxy
		rig := newTLSProxyRig(t, func(p *TLSProxy) {
			proxy = p
			p.Route = func(req *http.Request, res *Response, body []byte) {
				res.Header().Set("Content-Type", "text/event-stream")
				res.WriteHeader(200)
				_, _ = res.Write([]byte("data: first\n\n"))
				res.Flush()
				close(entered)
				<-release
				res.End([]byte("data: last\n\n"))
			}
		})
		client := rig.keepAliveClient(t)
		answered := make(chan string, 1)
		go func() {
			resp, err := client.Post("https://api.anthropic.com/v1/messages", "application/json",
				strings.NewReader(`{"model":"claude-opus-5-5","messages":[]}`))
			if err != nil {
				answered <- "error: " + err.Error()
				return
			}
			defer resp.Body.Close()
			text, err := io.ReadAll(resp.Body)
			if err != nil {
				answered <- "error: " + err.Error()
				return
			}
			answered <- string(text)
		}()
		<-entered
		drained := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			drained <- proxy.Drain(ctx)
		}()
		select {
		case err := <-drained:
			t.Fatalf("the drain returned %v while an answer streamed", err)
		case <-time.After(200 * time.Millisecond):
		}
		close(release)
		if text := <-answered; text != "data: first\n\ndata: last\n\n" {
			t.Fatalf("the answer across the drain = %q, want both events", text)
		}
		select {
		case err := <-drained:
			if err != nil {
				t.Fatalf("the drain = %v, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the drain still waits five seconds after the answer ended")
		}
	})

	t.Run("an idle decrypted tunnel closes at once", func(t *testing.T) {
		var proxy *TLSProxy
		rig := newTLSProxyRig(t, func(p *TLSProxy) { proxy = p })
		client := rig.keepAliveClient(t)
		resp, err := client.Get("https://api.anthropic.com/v1/me")
		if err != nil {
			t.Fatalf("get through the proxy: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proxy.Drain(ctx); err != nil {
			t.Fatalf("the drain of an idle tunnel = %v, want nil", err)
		}
	})

	t.Run("a blind tunnel still open when the budget ends is closed", func(t *testing.T) {
		upstreamEnd, proxyEnd := net.Pipe()
		t.Cleanup(func() { _ = upstreamEnd.Close() })
		var proxy *TLSProxy
		rig := newTLSProxyRig(t, func(p *TLSProxy) {
			proxy = p
			p.Dial = func(context.Context, string, string) (net.Conn, error) { return proxyEnd, nil }
		})
		conn, answer := connectThrough(t, rig.port, "someone-else.example:443")
		defer conn.Close()
		if answer.StatusCode != 200 {
			t.Fatalf("the CONNECT = %d, want 200 established", answer.StatusCode)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if err := proxy.Drain(ctx); err != context.DeadlineExceeded {
			t.Fatalf("the drain of a tunnel that never ends = %v, want the deadline", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("the client's read after the drain = %v, want EOF", err)
		}
	})
}

// keepAliveClient is Claude Code in proxy mode with its connection pool: a tunnel stays open
// between requests.
func (r *tlsProxyRig) keepAliveClient(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(r.caPEM) {
		t.Fatal("the CA's PEM did not parse")
	}
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", r.port)}, nil
		},
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Timeout: 15 * time.Second, Transport: transport}
}
