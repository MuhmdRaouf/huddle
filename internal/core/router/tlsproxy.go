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
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/domain"
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
	// AnthropicFail counts a pass-through request that failed to reach Anthropic, on the same
	// streak the decrypted route's failures feed; nil counts nothing. AnthropicOk counts a
	// pass-through request the upstream answered.
	AnthropicFail func(code string)
	AnthropicOk   func()
	// Events receives one route event per pass-through request, metadata only; nil records
	// nothing.
	Events func(event domain.SpoolEvent)
	// Transport carries the pass-through to Anthropic; nil is the shared agent for the target.
	Transport http.RoundTripper

	// The certificate state: the local CA, the leaf it signed for api.anthropic.com, and the last
	// load attempt — its error, when it ran and whether the failure is already in the log.
	mu           sync.Mutex
	ca           *pki.RouterCA
	caErr        error
	caAt         time.Time
	caLogged     bool
	leaf         *tls.Certificate
	leafNotAfter time.Time

	// tunnels is every CONNECT the proxy carries, from the handler's start until its client side
	// closes; a hijacked connection is the server's no longer, so a drain waits on these instead.
	tunnelsMu sync.Mutex
	tunnels   map[*openTunnel]struct{}
	draining  bool
}

// openTunnel is one CONNECT the proxy carries.
type openTunnel struct {
	// conn is the client's side once hijacked, nil before.
	conn net.Conn
	// server serves a decrypted conversation; nil for a blind tunnel.
	server *http.Server
	// done closes when the client's side closes.
	done chan struct{}
	once sync.Once
}

// tunnelConn is a tunnel's client side: its close ends the tunnel for the drain.
type tunnelConn struct {
	net.Conn
	proxy  *TLSProxy
	tunnel *openTunnel
}

func (c *tunnelConn) Close() error {
	err := c.Conn.Close()
	c.proxy.endTunnel(c.tunnel)
	return err
}

// Connect serves one CONNECT. Browsers cannot send CONNECT, so the router's Host check (a
// DNS-rebinding guard for pages a browser could reach) does not apply to it: the authority names
// the tunnel's target, not this router. A blind tunnel dials its target before anything goes
// back, so a target that cannot be opened is answered with the reason instead of a 200 the
// client's TLS handshake then fails on; only api.anthropic.com:443 is decrypted, with the local
// CA.
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
	// Only api.anthropic.com on its real port is decrypted; the same name on any other port is a
	// blind tunnel like every other host.
	decrypt := p.CADir != "" && number == 443 && strings.EqualFold(host, pki.RouterHost)
	if decrypt {
		if _, err := p.leafFor(); err != nil {
			// The CA is unusable; the reason is in the log. Passing through blind keeps Claude Code
			// working (no routing for these requests) where refusing would cut it off.
			decrypt = false
		}
	}
	// The dial comes before the hijack and the 200: once the client reads "established" it starts
	// its TLS handshake, which cannot say why the tunnel never opened.
	var target net.Conn
	if !decrypt {
		target, err = p.tunnelDial(req.Context(), "tcp", net.JoinHostPort(host, port))
		if err != nil {
			var local *localDialError
			if errors.As(err, &local) {
				p.connectRefused(w, "a CONNECT may not name this machine")
				return
			}
			p.connectFailed(w)
			return
		}
	}
	// The tunnel is counted before the hijack, while the server still waits on this handler: a
	// drain that starts once the server's shutdown returns finds it.
	tunnel := p.trackTunnel()
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		if target != nil {
			target.Close()
		}
		p.endTunnel(tunnel)
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		if target != nil {
			target.Close()
		}
		p.endTunnel(tunnel)
		return
	}
	client := &tunnelConn{Conn: &bufferedConn{Conn: conn, front: rw.Reader}, proxy: p, tunnel: tunnel}
	p.tunnelsMu.Lock()
	tunnel.conn = client
	p.tunnelsMu.Unlock()
	if decrypt {
		p.serveDecrypted(tunnel, client)
		return
	}
	p.tunnelPipe(client, target)
}

// connectRefused answers a CONNECT with the Anthropic error shape, so a client that inspects the
// failure reads a message instead of a parse error.
func (p *TLSProxy) connectRefused(w http.ResponseWriter, why string) {
	AnthropicError(NewResponse(w), 403, "permission_error", p.Name+" router: CONNECT refused ("+why+")")
}

// connectFailed answers a CONNECT whose target could not be opened with the Anthropic error
// shape, so the client reads why its tunnel never started.
func (p *TLSProxy) connectFailed(w http.ResponseWriter) {
	AnthropicError(NewResponse(w), 502, "api_error", p.Name+" router: CONNECT failed (the target cannot be reached)")
}

// tunnelPipe pipes an established CONNECT socket to its already-open target both ways, both
// sides closed once either ends. The bytes are never read here: the client's TLS handshake is
// with the target.
func (p *TLSProxy) tunnelPipe(client, target net.Conn) {
	safego.Go(p.Note, func() {
		_, _ = io.Copy(target, client)
		target.Close()
	})
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

// localDialError is the blind tunnel's dial refusing an address on this machine: a concrete type,
// so a CONNECT handler can tell this refusal from a target that is merely down.
type localDialError struct {
	addr string
}

func (e *localDialError) Error() string {
	return "tunnel to " + e.addr + " refused: it is this machine"
}

// refuseLocalDial is the blind tunnel's dial control: it refuses a resolved address isLocalIP names.
func refuseLocalDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || isLocalIP(ip) {
		return &localDialError{addr: address}
	}
	return nil
}

// serveDecrypted answers the client's TLS handshake with the local CA's leaf and serves the
// requests it carries. A tunnel that starts while the proxy drains serves without keep-alive, so
// it closes after its first answer.
func (p *TLSProxy) serveDecrypted(tunnel *openTunnel, client net.Conn) {
	config := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return p.leafFor() },
		NextProtos:     []string{"http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
	server := Harden(&http.Server{Handler: http.HandlerFunc(p.intercept)})
	p.tunnelsMu.Lock()
	tunnel.server = server
	draining := p.draining
	p.tunnelsMu.Unlock()
	if draining {
		server.SetKeepAlivesEnabled(false)
	}
	_ = server.Serve(&oneConnListener{conn: tls.Server(client, config)})
}

// trackTunnel counts one CONNECT the proxy carries until endTunnel.
func (p *TLSProxy) trackTunnel() *openTunnel {
	tunnel := &openTunnel{done: make(chan struct{})}
	p.tunnelsMu.Lock()
	defer p.tunnelsMu.Unlock()
	if p.tunnels == nil {
		p.tunnels = make(map[*openTunnel]struct{})
	}
	p.tunnels[tunnel] = struct{}{}
	return tunnel
}

// endTunnel drops a tunnel from the count once its client side is closed; a second call is a no-op.
func (p *TLSProxy) endTunnel(tunnel *openTunnel) {
	tunnel.once.Do(func() {
		p.tunnelsMu.Lock()
		delete(p.tunnels, tunnel)
		p.tunnelsMu.Unlock()
		close(tunnel.done)
	})
}

// Drain waits for every tunnel the proxy carries to end, for a daemon whose server has stopped
// accepting: a decrypted conversation finishes the answer it is streaming and closes instead of
// waiting for another request, and a blind tunnel runs until either side closes it. The tunnels
// still open when ctx ends are closed, and Drain answers ctx's error.
func (p *TLSProxy) Drain(ctx context.Context) error {
	p.tunnelsMu.Lock()
	p.draining = true
	open := make([]*openTunnel, 0, len(p.tunnels))
	servers := make([]*http.Server, 0, len(p.tunnels))
	for tunnel := range p.tunnels {
		open = append(open, tunnel)
		if tunnel.server != nil {
			servers = append(servers, tunnel.server)
		}
	}
	p.tunnelsMu.Unlock()
	// No keep-alive: an idle conversation closes now, a busy one after its answer. Closing a
	// tunnel takes the lock (endTunnel), so every close here runs outside it.
	for _, server := range servers {
		server.SetKeepAlivesEnabled(false)
	}
	for i, tunnel := range open {
		select {
		case <-tunnel.done:
		case <-ctx.Done():
			p.tunnelsMu.Lock()
			conns := make([]net.Conn, 0, len(open)-i)
			for _, left := range open[i:] {
				if left.conn != nil {
					conns = append(conns, left.conn)
				}
			}
			p.tunnelsMu.Unlock()
			for _, conn := range conns {
				_ = conn.Close()
			}
			return ctx.Err()
		}
	}
	return nil
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
// its upgrade relayed both ways. Every answer that arrives counts as the path's success and
// every failure as its failure, on the streak the router's health events feed. Each request gets
// its own line and route event (passLog); the reverse proxy's own error lines would go to the
// process's stderr with a local time and no path, so they are dropped.
func (p *TLSProxy) anthropicProxy() *httputil.ReverseProxy {
	target := p.Anthropic
	inner := p.Transport
	if inner == nil {
		inner = AgentFor(target, p.Env, false)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + pr.Out.URL.Path
			pr.Out.Host = target.Host
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		Transport: passLog{
			inner: answeredTransport{inner: inner, ok: p.AnthropicOk},
			proxy: p,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if p.AnthropicFail != nil {
				p.AnthropicFail(failureCode(err))
			}
			AnthropicError(NewResponse(w), 502, "api_error", p.Name+" router: anthropic unreachable")
		},
	}
}

// answeredTransport reports each answer that arrived as one success, so the Anthropic path's
// streak sees the pass-through's successes; the ErrorHandler sees its failures.
type answeredTransport struct {
	inner http.RoundTripper
	ok    func()
}

func (t answeredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	answer, err := t.inner.RoundTrip(req)
	if err == nil && t.ok != nil {
		t.ok()
	}
	return answer, err
}

// passLog writes one router-log line and one route event per pass-through request: metadata
// only — method, host, path, status, bytes, duration and why it failed — never a query, a header
// or a body byte. An answer's line waits for its body to end, so a stream the upstream cuts
// mid-body is told apart from one that finished.
type passLog struct {
	inner http.RoundTripper
	proxy *TLSProxy
}

func (t passLog) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	answer, err := t.inner.RoundTrip(req)
	if err != nil {
		if req.Context().Err() != nil {
			t.proxy.passed(req, started, passOutcome{word: "cancelled"})
		} else {
			t.proxy.passed(req, started, passOutcome{word: "error", code: failureCode(err)})
		}
		return nil, err
	}
	// An upgrade's body is the connection itself, which the reverse proxy needs unwrapped.
	if answer.StatusCode == http.StatusSwitchingProtocols {
		t.proxy.passed(req, started, passOutcome{status: answer.StatusCode})
		return answer, nil
	}
	answer.Body = &passBody{ReadCloser: answer.Body, proxy: t.proxy, req: req, started: started, status: answer.StatusCode}
	return answer, nil
}

// passOutcome is how one pass-through request ended: the upstream's status once its head
// arrived, word "" for an answer read to its end, else "cut", "error" or "cancelled", with the
// failure's code for a cut or an error, and the body bytes relayed.
type passOutcome struct {
	status int
	word   string
	code   string
	bytes  int64
}

// passBody counts an answer's body as the reverse proxy relays it, and reports the request once:
// at its end, at a read error (the cut), or at a close before either (the client left).
type passBody struct {
	io.ReadCloser
	proxy   *TLSProxy
	req     *http.Request
	started time.Time
	status  int
	bytes   int64
	once    sync.Once
}

func (b *passBody) Read(buf []byte) (int, error) {
	n, err := b.ReadCloser.Read(buf)
	b.bytes += int64(n)
	switch {
	case err == nil:
	case err == io.EOF:
		b.report(passOutcome{})
	case b.req.Context().Err() != nil:
		b.report(passOutcome{word: "cancelled"})
	default:
		b.report(passOutcome{word: "cut", code: cutCode(err)})
	}
	return n, err
}

func (b *passBody) Close() error {
	b.report(passOutcome{word: "cancelled"})
	return b.ReadCloser.Close()
}

func (b *passBody) report(outcome passOutcome) {
	b.once.Do(func() {
		outcome.status, outcome.bytes = b.status, b.bytes
		b.proxy.passed(b.req, b.started, outcome)
	})
}

// h2ResetRE reads the code of an HTTP/2 stream reset; net/http keeps its stream error type
// unexported, so its message is the only handle on the code.
var h2ResetRE = regexp.MustCompile(`stream error: stream ID \d+; ([A-Z_]+)`)

// cutCode is why an answer's body stopped: the HTTP/2 reset's code, else the connection's errno.
func cutCode(err error) string {
	if match := h2ResetRE.FindStringSubmatch(err.Error()); match != nil {
		return match[1]
	}
	return failureCode(err)
}

// passed writes one pass-through request's line and route event. A cut answers the route event
// 502 like a dropped provider stream, though the client already had its 200 head.
func (p *TLSProxy) passed(req *http.Request, started time.Time, outcome passOutcome) {
	elapsed := time.Since(started).Milliseconds()
	head := fmt.Sprintf("%s %s host=%s → anthropic-pass", req.Method, req.URL.Path, req.URL.Host)
	status := int64(outcome.status)
	var line, why string
	switch outcome.word {
	case "":
		line = fmt.Sprintf("%s %d bytes=%d ms=%d", head, outcome.status, outcome.bytes, elapsed)
	case "cut":
		line = fmt.Sprintf("%s cut (%s) status=%d bytes=%d ms=%d", head, outcome.code, outcome.status, outcome.bytes, elapsed)
		status, why = 502, "stream cut ("+outcome.code+")"
	case "error":
		line = fmt.Sprintf("%s error %s ms=%d", head, outcome.code, elapsed)
		status, why = 502, "connection failed ("+outcome.code+")"
	default:
		line = fmt.Sprintf("%s cancelled bytes=%d ms=%d", head, outcome.bytes, elapsed)
		status, why = 499, "client closed"
	}
	if p.Note != nil {
		p.Note(line)
	}
	if p.Events == nil {
		return
	}
	event := domain.RouteEvent{
		Ts:        isoNow(),
		Event:     domain.EventRoute,
		Plugin:    p.Name,
		Upstream:  req.URL.Host,
		Route:     "anthropic-pass",
		Status:    status,
		LatencyMs: elapsed,
	}
	if why != "" {
		event.Error = &why
	}
	if session, ok := textHeader(req, "x-claude-code-session-id"); ok {
		event.SessionID = &session
	}
	if agent, ok := textHeader(req, "x-claude-code-agent-id"); ok {
		event.AgentID = &agent
	}
	if parent, ok := textHeader(req, "x-claude-code-parent-agent-id"); ok {
		event.ParentAgentID = &parent
	}
	p.Events(event)
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
