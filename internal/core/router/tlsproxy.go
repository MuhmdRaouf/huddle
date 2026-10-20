// The HTTPS proxy mode shared by the front and the multi router: the client keeps its base URL at
// api.anthropic.com and points HTTPS_PROXY at the router, so `claude rc` (Remote Control) works
// with routing on. A CONNECT is accepted as a proxy accepts one: api.anthropic.com is decrypted
// with the local CA and routed exactly as base-URL mode routes a request, every other host is
// tunnelled blind with its TLS untouched, and a tunnel this machine must not open is refused. The
// proxy exists to keep Claude Code working, never to look at anyone's traffic: only
// api.anthropic.com is ever decrypted.
package router

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/netproxy"
	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// TLSProxy is the HTTPS proxy mode: api.anthropic.com is decrypted with the router CA and routed,
// any other host is tunnelled blind, and a tunnel to this machine is refused.
type TLSProxy struct {
	// Name is the router's name in error messages.
	Name string
	// CADir is the local CA's directory; "" never decrypts.
	CADir string
	// Anthropic is where the decrypted non-model requests pass through to.
	Anthropic *url.URL
	// Env is the proxy environment a blind tunnel's dial honours.
	Env ProxyEnv
	// Dial opens a blind tunnel's socket; nil dials through the user's own proxy when one applies,
	// else directly with a 10 s timeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// BodyLimit is the largest request body a decrypted model request may carry; 0 is the default
	// 64 MiB.
	BodyLimit int64
	// Now is the clock, epoch milliseconds; the CA and the leaf read it.
	Now func() int64
	// Note receives the one line per CA failure episode.
	Note func(line string)
	// Route serves a decrypted model request, the way base-URL mode serves one.
	Route func(req *http.Request, res *Response, body []byte)

	// The certificate state: the local CA, the leaf it signed for api.anthropic.com, and the last
	// load attempt — its error, when it ran and whether the failure is already in the log.
	mu           sync.Mutex
	ca           *pki.RouterCA
	caErr        error
	caAt         time.Time
	caLogged     bool
	leaf         *tls.Certificate
	leafNotAfter time.Time
}

// Connect serves one CONNECT. Browsers cannot send CONNECT, so the router's Host check (a
// DNS-rebinding guard for pages a browser could reach) does not apply to it: the authority names
// the tunnel's target, not this router. Any port on a remote host is tunnelled blind; only
// api.anthropic.com:443 is decrypted, with the local CA.
func (p *TLSProxy) Connect(w http.ResponseWriter, req *http.Request) {
	host, port, err := net.SplitHostPort(req.RequestURI)
	if err != nil {
		p.connectRefused(w, "a CONNECT names host:port")
		return
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		p.connectRefused(w, "a CONNECT names host:port")
		return
	}
	// A tunnel to this machine would reach the router's own port, the worker ports and every other
	// loopback listener from wherever Claude Code runs; the proxy is not a door into the host.
	lower := strings.ToLower(host)
	if ip := net.ParseIP(host); (ip != nil && isLocalIP(ip)) || lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		p.connectRefused(w, "a CONNECT may not name this machine")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.connectRefused(w, "the connection cannot be tunnelled")
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		return
	}
	client := &bufferedConn{Conn: conn, front: rw.Reader}
	// Only api.anthropic.com on its real port is decrypted; the same name on any other port is a
	// blind tunnel like every other host.
	if p.CADir != "" && number == 443 && strings.EqualFold(host, pki.RouterHost) {
		if _, err := p.leafFor(); err == nil {
			p.serveDecrypted(client)
			return
		}
		// The CA is unusable; the reason is in the log. Passing through blind keeps Claude Code
		// working (no routing for these requests) where refusing would cut it off.
	}
	p.blindTunnel(req.Context(), client, net.JoinHostPort(host, port))
}

// connectRefused answers a CONNECT with the Anthropic error shape, so a client that inspects the
// failure reads a message instead of a parse error.
func (p *TLSProxy) connectRefused(w http.ResponseWriter, why string) {
	AnthropicError(NewResponse(w), 403, "permission_error", p.Name+" router: CONNECT refused ("+why+")")
}

// blindTunnel pipes an established CONNECT socket to its target both ways, both sides closed once
// either ends. The bytes are never read here: the client's TLS handshake is with the target.
func (p *TLSProxy) blindTunnel(ctx context.Context, client net.Conn, addr string) {
	target, err := p.tunnelDial(ctx, "tcp", addr)
	if err != nil {
		client.Close()
		return
	}
	go func() {
		_, _ = io.Copy(target, client)
		target.Close()
	}()
	_, _ = io.Copy(client, target)
	client.Close()
}

// tunnelDial opens the blind tunnel's socket: the router's hook when one is set, else the user's
// own proxy when the environment applies one to the target, else a direct dial.
func (p *TLSProxy) tunnelDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	target := &url.URL{Scheme: "https", Host: addr}
	if proxy, ok := netproxy.ProxyFor(target, p.Env); ok {
		return tunnel(proxy)(ctx, network, addr)
	}
	// A name can resolve to this machine as well as a literal can name it: the check runs again on
	// the address the dial actually reaches.
	dialer := net.Dialer{Timeout: 10 * time.Second, Control: refuseLocalDial}
	return dialer.DialContext(ctx, network, addr)
}

// isLocalIP reports an address on this machine or its link: loopback, unspecified or link-local.
func isLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
}

// refuseLocalDial is the blind tunnel's dial control: it refuses a resolved address isLocalIP names.
func refuseLocalDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || isLocalIP(ip) {
		return fmt.Errorf("tunnel to %s refused: it is this machine", address)
	}
	return nil
}

// serveDecrypted answers the client's TLS handshake with the local CA's leaf and serves the
// requests it carries.
func (p *TLSProxy) serveDecrypted(client net.Conn) {
	config := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return p.leafFor() },
		NextProtos:     []string{"http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
	server := Harden(&http.Server{Handler: http.HandlerFunc(p.intercept)})
	_ = server.Serve(&oneConnListener{conn: tls.Server(client, config)})
}

// oneConnListener serves exactly the connection it was built with, then ends the server: a
// CONNECT carries one conversation, and the server's goroutine must not wait for a second.
type oneConnListener struct {
	conn     net.Conn
	accepted bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if l.accepted {
		return nil, net.ErrClosed
	}
	l.accepted = true
	return l.conn, nil
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// intercept serves one request that arrived through a decrypted CONNECT: a model request runs the
// very pipeline base-URL mode runs, and every other request reaches Anthropic exactly as it came —
// Remote Control's settings, policy and event calls, and its streams and upgrades with them.
func (p *TLSProxy) intercept(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost && (req.URL.Path == "/v1/messages" || req.URL.Path == "/v1/messages/count_tokens") {
		res := NewResponse(w)
		Receive(req, res, p.Name, p.BodyLimit, func(body []byte) {
			p.Route(req, res, body)
		})
		return
	}
	p.anthropicProxy().ServeHTTP(w, req)
}

// anthropicProxy is the pass-through half of proxy mode: the request goes to Anthropic with its
// host rewritten to the target's and nothing else changed, its stream flushed as it arrives and
// its upgrade relayed both ways.
func (p *TLSProxy) anthropicProxy() *httputil.ReverseProxy {
	target := p.Anthropic
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + pr.Out.URL.Path
			pr.Out.Host = target.Host
		},
		FlushInterval: -1,
		Transport:     AgentFor(target, p.Env, false),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			AnthropicError(NewResponse(w), 502, "api_error", p.Name+" router: anthropic unreachable")
		},
	}
}

// leafFor is the certificate CONNECT api.anthropic.com is answered with: the local CA's leaf,
// cached on the proxy and re-signed only once its life has less than a day left.
func (p *TLSProxy) leafFor() (*tls.Certificate, error) {
	now := time.UnixMilli(p.Now())
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.leaf != nil && p.leafNotAfter.Sub(now) > 24*time.Hour {
		return p.leaf, nil
	}
	ca, err := p.caFor(now)
	if err != nil {
		return nil, err
	}
	leaf, err := ca.Leaf(pki.RouterHost, now)
	if err != nil {
		return nil, err
	}
	p.leaf, p.leafNotAfter = leaf, leaf.Leaf.NotAfter
	return leaf, nil
}

// caFor is the proxy's local CA, loaded once and kept. A load that failed is kept too and retried
// after a minute, and one line per failure episode goes to the router log — not one per request.
// Called with p.mu held.
func (p *TLSProxy) caFor(now time.Time) (*pki.RouterCA, error) {
	if p.ca != nil {
		return p.ca, nil
	}
	if p.caErr != nil && now.Sub(p.caAt) < time.Minute {
		return nil, p.caErr
	}
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), p.CADir, now)
	if err != nil {
		if !p.caLogged {
			p.caLogged = true
			p.Note(fmt.Sprintf("local CA unusable (%v); CONNECT api.anthropic.com passes through as a blind tunnel", err))
		}
		p.caErr, p.caAt = err, now
		return nil, err
	}
	p.ca, p.caErr, p.caLogged = ca, nil, false
	return ca, nil
}
