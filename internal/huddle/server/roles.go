package server

// The machine's role (SPEC 3) decides which listeners exist. Solo, the default, answers loopback
// only. Hub without hub.listen answers loopback only, behind a TLS reverse proxy on this machine
// that serves hub.host. Hub with hub.listen adds a second listener there, the NetBird address,
// served with the certificate in tls/: a paired device's cookie or a satellite's credential is the only way past
// its gate — beside the shell and the sign-in ways every brand-new browser needs, the one-time
// link `huddle open` prints on a hub included. A satellite serves its own sessions exactly as
// solo does and never listens beyond loopback, whatever an older role left in the config. Every
// failure of the second listener (no certificate, a taken address, a death mid-run) leaves the
// loopback listener serving: the role never strands the machine's own sessions.

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
	"github.com/MuhmdRaouf/huddle/internal/huddle/phone"
	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// hubBaseURL answers the hub's own https base when the home's role is a hub with a name, else
// nothing: the base every printed dashboard link carries on a hub machine.
func hubBaseURL(root string) string {
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "hub" || cfg.Hub.Host == "" {
		return ""
	}
	return phone.HubURL(cfg.Hub.Host, cfg.Hub.Listen)
}

// hubName answers the name a hub role serves under, else nothing: what /health calls the hub by,
// so a solo or satellite home is known by its machine alone.
func hubName(root string) string {
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "hub" {
		return ""
	}
	return cfg.Hub.Host
}

// hubSignedOut is what a browser with no pairing is told on the hub listener: the passkey is the
// only way in from the network.
const hubSignedOut = "This browser is not paired with this Huddle. Sign in with its passkey, or pair it from the hub machine with `huddle pair`."

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
		if cfg.Hub.Host == "" {
			fmt.Fprintf(s.o.Stdout,
				"huddle: role hub without hub.host — the hub answers loopback only (huddle role hub --host <name> [--listen <addr:port>])\n")
			return
		}
		if cfg.Hub.Listen == "" {
			fmt.Fprintf(s.o.Stdout,
				"huddle: role hub as %s: loopback only, for a TLS reverse proxy on this machine to forward to (--listen <addr:port> adds the hub's own listener)\n",
				phone.HubURL(cfg.Hub.Host, ""))
			return
		}
		s.startHubListener(ctx, cfg, handler)
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

// startHubListener binds hub.listen and serves TLS on it, behind the same request guards plus
// the hub gate. The certificate is whichever stands first: the pair in the home's tls/ (an
// imported pair, or one ACME issued — both belong to their owner and are never replaced), else
// an ACME setup whose first certificate `huddle cert` still has to obtain, else a leaf from
// huddle's own local CA, issued at once and renewed in the background. Any failure answers one
// line and leaves the server as it was.
func (s *server) startHubListener(ctx context.Context, cfg home.Config, handler http.HandlerFunc) {
	tlsDir := home.Home{Root: s.o.Home}.TLS()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	cert, pairErr := tls.LoadX509KeyPair(
		filepath.Join(tlsDir, "cert.pem"), filepath.Join(tlsDir, "key.pem"))
	switch {
	case pairErr == nil:
		tlsConfig.Certificates = []tls.Certificate{cert}
	case cfg.Cert.DNSProvider != "" || importedStands(s.o.Home):
		// a certificate this word never stands in for: an ACME setup waits for its first one,
		// an imported pair for the owner's fresh import
		fmt.Fprintf(s.o.Stdout,
			"huddle: the hub listener needs a certificate in %s — the hub answers loopback only: %v\n",
			tlsDir, pairErr)
		return
	default:
		h := home.Home{Root: s.o.Home}
		leaf, err := pki.NewLeaf(pki.New(h.PKI(), pki.Options{}), pki.HubLeafNames(cfg.Hub.Host))
		if err != nil {
			fmt.Fprintf(s.o.Stdout,
				"huddle: the local CA in %s could not issue the hub's certificate — the hub answers loopback only: %v\n",
				h.PKI(), err)
			return
		}
		tlsConfig.GetCertificate = leaf.GetCertificate
		go leaf.KeepFresh(ctx, pki.KeepFreshOptions{
			Log: func(line string) { fmt.Fprintln(s.o.Stdout, line) },
		})
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
		TLSConfig:         tlsConfig,
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

// importedStands answers whether the home's tls/ holds the import marker: a pair the owner laid
// by hand, whose absence on disk is the owner's to fix — never a silent switch to the local CA.
func importedStands(root string) bool {
	_, ok := home.ImportedName(home.Home{Root: root})
	return ok
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
// the app and earn a session passes through — the shell, the sign-in ceremonies, the one-time
// link `huddle open` prints on a hub (one use, five minutes, minted on the machine itself) —
// everything else must carry a paired device's cookie or a satellite's credential.
func (s *server) hubGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withHubListener(r.Context()))
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
		// the session the one-time link earns: the sign-in cookie a code redeemed on this very
		// listener set, read the way loopback always read it. The token header is not a browser's
		// way in — the machine's own credential stays off the network listener's door.
		if r.Header.Get(auth.Header) == "" {
			if _, ok := s.auth.Who(auth.Given(r, s.cookie)); ok {
				next(w, r)
				return
			}
		}
		s.writeJSON(w, 401, signinAnswer{Error: hubSignedOut, Signin: true}, nil)
	}
}

// hubPublicPath answers whether a browser that holds no credential still needs the path: the app
// shell it loads (with its static assets and its health), the two ceremonies that turn it into a
// paired device, and the satellite join that turns a one-use invite into a machine's credential.
func hubPublicPath(path string) bool {
	for _, prefix := range []string{"/static/", "/fonts/", "/icons/", "/api/pair/", "/api/login/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	switch path {
	case "/", "/index.html", "/pair", "/health",
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
