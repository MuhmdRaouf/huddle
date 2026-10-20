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
