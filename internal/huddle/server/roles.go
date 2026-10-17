package server

// The machine's role (SPEC 3) decides which listeners exist. Solo, the default, answers loopback
// only. Hub adds a second listener on hub.listen, the NetBird address, served with the
// certificate in tls/: a paired device's cookie or a satellite's credential is the only way past
// its gate — the one-time link that signs a loopback browser in never works there, the passkey
// ceremonies always do. A satellite serves its own sessions exactly as solo does and never
// listens beyond loopback, whatever an older role left in the config. Every failure of the second
// listener (no certificate, a taken address, a death mid-run) leaves the loopback listener
// serving: the role never strands the machine's own sessions.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
)

// hubSignedOut is what a browser with no pairing is told on the hub listener: the passkey is the
// only way in from the network.
const hubSignedOut = "This browser is not paired with this Huddle. Sign in with its passkey, or pair it from the hub machine with `huddle pair`."

// hubLinkRefused is the one answer a one-time link earns on the hub listener: the link is minted
// on and for the machine itself (SPEC 3), never a way in from the network.
const hubLinkRefused = "the one-time link signs in on the machine itself: sign in with your passkey"

// satelliteKey is the context key the hub gate tucks a verified satellite's machine name under,
// so the routes past the gate read who is asking the way a cookie's Who is read.
type satelliteKey struct{}

// withSatellite carries one verified satellite's machine name in the context.
func withSatellite(ctx context.Context, machine string) context.Context {
	return context.WithValue(ctx, satelliteKey{}, machine)
}

// satelliteFrom answers the machine name the hub gate verified for this request, if it did.
func satelliteFrom(ctx context.Context) (string, bool) {
	machine, ok := ctx.Value(satelliteKey{}).(string)
	return machine, ok
}

// hubListenerKey is the context key the hub gate marks its requests with, so a route past the
// gate tells the network listener from loopback the way it reads a verified satellite's name.
type hubListenerKey struct{}

// withHubListener marks a request as having arrived on the hub listener.
func withHubListener(ctx context.Context) context.Context {
	return context.WithValue(ctx, hubListenerKey{}, true)
}

// fromHubListener answers whether the hub gate let this request in.
func fromHubListener(ctx context.Context) bool {
	marked, _ := ctx.Value(hubListenerKey{}).(bool)
	return marked
}

// applyRole starts the listeners the home's config calls for beside the loopback one. The role
// words (`huddle role …`) write the config; a restart takes it up.
func (s *server) applyRole(ctx context.Context, handler http.HandlerFunc) {
	cfg, err := home.LoadConfig(home.Home{Root: s.o.Home})
	if err != nil {
		return // a config that cannot be read never keeps the loopback listener from serving
	}
	switch cfg.Role {
	case "hub":
		s.startRenewal(ctx, cfg)
		if cfg.Hub.Listen == "" {
			fmt.Fprintf(s.o.Stdout,
				"huddle: role hub without hub.listen — the hub answers loopback only (huddle role hub --listen <addr:port> --host <name>)\n")
			return
		}
		s.startHubListener(cfg, handler)
	default: // solo, satellite and anything else a newer release may write: loopback only
		if cfg.Hub.Listen != "" {
			fmt.Fprintf(s.o.Stdout, "huddle: role %s: hub.listen %s is ignored — only the hub role listens beyond loopback\n",
				cfg.Role, cfg.Hub.Listen)
		}
	}
}

// startRenewal keeps the hub's certificate fresh (SPEC 7.2): the server itself renews it 30 days
// before expiry, for as long as it runs. It needs the DNS provider the config names (the one
// `huddle cert` recorded), that provider's token and a certificate to keep fresh — a hub
// without one of them serves with what it has, and the renewal starts with the next restart
// that has them. The loop runs whatever the listener did: a hub that cannot bind its address
// still gets a fresh certificate.
func (s *server) startRenewal(ctx context.Context, cfg home.Config) {
	if cfg.Cert.DNSProvider == "" || cfg.Hub.Host == "" {
		return
	}
	issue := s.o.CertACME
	if issue == nil {
		h := home.Home{Root: s.o.Home}
		secrets := outside.SecretsFor(ctx, h)
		account := "dns:" + cfg.Cert.DNSProvider
		issue = func(ctx context.Context) (home.ACME, error) {
			token, ok := secrets.Get(ctx, account)
			if !ok {
				return nil, fmt.Errorf("no token for %s in the keystore (huddle cert stores it)", account)
			}
			return outside.NewACME(cfg.Cert.DNSProvider, token, filepath.Join(h.TLS(), "acme-account.key"))
		}
	}
	go home.RenewCertificates(ctx, home.Home{Root: s.o.Home}, issue, cfg.Hub.Host, home.RenewalOptions{
		Now: s.now,
		Log: func(line string) { fmt.Fprintln(s.o.Stdout, line) },
	})
}

// startHubListener binds hub.listen and serves TLS from the home's tls/ on it, behind the same
// request guards plus the hub gate. Any failure answers one line and leaves the server as it was.
func (s *server) startHubListener(cfg home.Config, handler http.HandlerFunc) {
	tlsDir := home.Home{Root: s.o.Home}.TLS()
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(tlsDir, "cert.pem"), filepath.Join(tlsDir, "key.pem"))
	if err != nil {
		fmt.Fprintf(s.o.Stdout,
			"huddle: the hub listener needs a certificate in %s — the hub answers loopback only: %v\n",
			tlsDir, err)
		return
	}
	listener, err := net.Listen("tcp", cfg.Hub.Listen)
	if err != nil {
		fmt.Fprintf(s.o.Stdout,
			"huddle: the hub listener could not bind %s — the hub answers loopback only: %v\n",
			cfg.Hub.Listen, err)
		return
	}
	s.hubListener = listener
	s.hubSrv = &http.Server{
		Handler:           s.guard(s.hubGate(handler)),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 60 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	for _, host := range hubListenHosts(cfg.Hub.Listen, listener.Addr(), cfg.Hub.Host) {
		s.allowHost(host)
	}
	address := listener.Addr().String()
	fmt.Fprintf(s.o.Stdout, "hub listener on https://%s · paired devices and satellites only\n", address)
	go func() {
		if err := s.hubSrv.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(s.o.Stdout, "huddle: the hub listener ended: %v\n", err)
		}
	}()
}

// hubListenHosts answers the Host spellings the hub listener's own address makes legitimate: the
// address as its owner wrote it (with the port that bound when the config names 0), the address
// as it bound, and the hub's name with and without this port — the browser reaching the hub by
// name sends one of exactly these.
func hubListenHosts(listen string, bound net.Addr, hubHost string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil
	}
	boundHost, boundPort, err := net.SplitHostPort(bound.String())
	if err != nil {
		return nil
	}
	hosts := []string{
		net.JoinHostPort(host, port),
		net.JoinHostPort(host, boundPort),
		net.JoinHostPort(boundHost, boundPort),
	}
	if hubHost != "" {
		hosts = append(hosts, hubHost, net.JoinHostPort(hubHost, boundPort))
	}
	return hosts
}

// hubGate is the hub listener's own door (SPEC 3): everything a brand-new browser needs to load
// the app and earn a session passes through, everything else must carry a paired device's cookie
// or a satellite's credential, and the one-time link never signs anyone in.
func (s *server) hubGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withHubListener(r.Context()))
		if r.URL.Path == "/" && r.URL.Query().Has("code") {
			writeText(w, 401, hubLinkRefused, nil)
			return
		}
		if hubPublicPath(r.URL.Path) {
			next(w, r)
			return
		}
		if cookie := auth.PairCookie(r); cookie != "" && s.pairs != nil {
			if _, ok := s.pairs.Session(r.Context(), cookie); ok {
				next(w, r)
				return
			}
		}
		if machine, ok := s.satelliteBearer(r); ok {
			next(w, r.WithContext(withSatellite(r.Context(), machine)))
			return
		}
		s.writeJSON(w, 401, signinAnswer{Error: hubSignedOut, Signin: true}, nil)
	}
}

// hubPublicPath answers whether a browser that holds no credential still needs the path: the app
// shell it loads, the two ceremonies that turn it into a paired device, and the satellite join
// that turns a one-use invite into a machine's credential.
func hubPublicPath(path string) bool {
	for _, prefix := range []string{"/static/", "/fonts/", "/icons/", "/api/pair/", "/api/login/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	switch path {
	case "/", "/index.html", "/pair", "/app.js", "/app.css",
		"/manifest.webmanifest", "/sw.js", "/signed-out.css", satJoinPath:
		return true
	}
	return false
}

// satelliteBearer resolves the satellite credential a request carries in its Authorization
// header (SPEC 6.7): only the credential's sha256 digest is looked up, in the home's hub.db.
func (s *server) satelliteBearer(r *http.Request) (string, bool) {
	if s.registry == nil {
		return "", false
	}
	credential, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !auth.SatelliteRe.MatchString(credential) {
		return "", false
	}
	machine, live, err := s.registry.SatelliteSession(r.Context(),
		auth.PairDigest(credential), s.now().UnixMilli())
	if err != nil || !live {
		return "", false
	}
	return machine, true
}

// allowHost admits one more Host header spelling, the way the loopback spellings and the
// config's names are admitted at start.
func (s *server) allowHost(host string) {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	s.hosts[host] = true
}

// closeHub ends the hub listener and its connections, nil-safe: a role that opened none closes
// nothing.
func (s *server) closeHub() {
	if s.hubListener != nil {
		_ = s.hubListener.Close()
	}
	if s.hubSrv != nil {
		s.hubSrv.SetKeepAlivesEnabled(false)
		_ = s.hubSrv.Close()
	}
}
