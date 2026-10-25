package cli

// The domain word: one command that names the hub for the Caddy flow (role hub, the name, no
// listener, the port pinned) and prints the site block to paste, the way back to loopback, and a
// probe of the proxy that runs only when asked.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// runDomain runs one domain word and answers its exit code and both outputs.
func runDomain(t *testing.T, w *World, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := Domain(context.Background(), w, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestDomainSetsTheHubNameForTheProxyFlow walks the word: a name writes the proxy-flow role and
// prints the Caddy block for the pinned port, the bare word says the name, off goes back to
// loopback, and every wrong shape exits 2.
func TestDomainSetsTheHubNameForTheProxyFlow(t *testing.T) {
	root := t.TempDir()
	if code, out, _ := runDomain(t, roleWorld(t, root), "domain"); code != 0 || strings.TrimSpace(out) != "localhost only" {
		t.Errorf("domain on a fresh home = (%d, %q), want 0 and localhost only", code, out)
	}
	if cfg := savedCfg(t, root); cfg.Role == "hub" || cfg.Hub.Host != "" {
		t.Errorf("the bare word wrote a role: %+v", cfg)
	}

	code, out, errOut := runDomain(t, roleWorld(t, root), "domain", "hub.example.dev")
	if code != 0 || errOut != "" {
		t.Fatalf("domain hub.example.dev = (%d, %q), want 0 and silence", code, errOut)
	}
	cfg := savedCfg(t, root)
	if cfg.Role != "hub" || cfg.Hub.Host != "hub.example.dev" || cfg.Hub.Listen != "" ||
		cfg.PortAuto || cfg.Port != server.PortFirst {
		t.Errorf("the config after domain hub.example.dev = %+v, want role hub, the name, no listener, port %d pinned", cfg, server.PortFirst)
	}
	port := strconv.Itoa(server.PortFirst)
	for _, want := range []string{
		"role hub: this machine answers loopback only, as https://hub.example.dev behind a TLS reverse proxy",
		"hub.example.dev {\n",
		"\treverse_proxy 127.0.0.1:" + port + " {\n",
		"\t\theader_up X-Forwarded-Proto https\n",
		"Let's Encrypt",
		"restart huddle for the role to take effect",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("domain hub.example.dev printed no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tls internal") {
		t.Errorf("a public name's block carries tls internal:\n%s", out)
	}

	if code, out, _ := runDomain(t, roleWorld(t, root), "domain"); code != 0 || !strings.Contains(out, "hub.example.dev") {
		t.Errorf("domain after a name = (%d, %q), want 0 and the name", code, out)
	}

	code, out, errOut = runDomain(t, roleWorld(t, root), "domain", "off")
	if code != 0 || errOut != "" || !strings.Contains(out, "role solo: this machine's huddle answers loopback only") {
		t.Errorf("domain off = (%d, %q, %q), want 0 and the solo lines", code, out, errOut)
	}
	if cfg := savedCfg(t, root); cfg.Role != "solo" || !cfg.PortAuto {
		t.Errorf("the config after domain off = %+v, want role solo with the port handed back", cfg)
	}

	private := t.TempDir()
	if code, out, errOut := runDomain(t, roleWorld(t, private), "domain", "hub.example.test"); code != 0 ||
		!strings.Contains(out, "\ttls internal\n") || !strings.Contains(out, "caddy trust") {
		t.Errorf("domain hub.example.test = (%d, %q, %q), want the block with tls internal and the trust line", code, out, errOut)
	}

	for _, line := range [][]string{
		{"domain", "--check"},
		{"domain", "127.0.0.2"},
		{"domain", "hub example.dev"},
		{"domain", "hub.example.dev", "extra"},
	} {
		fresh := t.TempDir()
		code, out, errOut := runDomain(t, roleWorld(t, fresh), line...)
		if code != 2 || out != "" || errOut == "" {
			t.Errorf("%v = (%d, %q, %q), want 2 and one line on stderr", line, code, out, errOut)
		}
		if cfg := savedCfg(t, fresh); cfg.Role == "hub" {
			t.Errorf("%v wrote role hub", line)
		}
	}
	if _, _, errOut := runDomain(t, roleWorld(t, t.TempDir()), "domain", "127.0.0.2"); !strings.Contains(errOut, "the hub's name, not an address") {
		t.Errorf("domain 127.0.0.2 said %q, want the role's own refusal", errOut)
	}
}

// fakeProxy is a TLS server standing in for Caddy at the hub's name, and a client that reaches it
// for any name, the way the owner's DNS points the name at the real proxy.
func fakeProxy(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Client) {
	t.Helper()
	proxy := httptest.NewTLSServer(handler)
	t.Cleanup(proxy.Close)
	return proxy, clientTo(proxy, proxy.Listener.Addr().String())
}

// clientTo is the proxy's own client with every dial sent to addr.
func clientTo(proxy *httptest.Server, addr string) *http.Client {
	transport := proxy.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com" // the name httptest's certificate carries
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: transport}
}

// TestDomainCheckReadsTheProxy probes the proxy at the saved name: a health answer naming this
// home is the hub behind it, another home is the wrong port, and a refused connection is a proxy
// that is not up.
func TestDomainCheckReadsTheProxy(t *testing.T) {
	root := t.TempDir()
	if code, _, errOut := runDomain(t, roleWorld(t, root), "domain", "hub.example.dev"); code != 0 {
		t.Fatalf("domain hub.example.dev = (%d, %q)", code, errOut)
	}
	answer := func(home string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" || r.Host != "hub.example.dev" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(struct {
				OK      bool   `json:"ok"`
				Version string `json:"version"`
				Home    string `json:"home"`
			}{true, "9.8.7", home})
		}
	}

	_, client := fakeProxy(t, answer(root))
	w := roleWorld(t, root)
	w.ProbeClient = client
	code, out, errOut := runDomain(t, w, "domain", "--check")
	if code != 0 || !strings.Contains(out, "https://hub.example.dev answers and forwards to this hub (huddle 9.8.7)") {
		t.Errorf("domain --check through the proxy = (%d, %q, %q), want 0 and the hub's version", code, out, errOut)
	}

	_, other := fakeProxy(t, answer(t.TempDir()))
	w = roleWorld(t, root)
	w.ProbeClient = other
	if code, out, _ := runDomain(t, w, "domain", "--check"); code != 1 || !strings.Contains(out, "another huddle") {
		t.Errorf("domain --check to another home = (%d, %q), want 1 and another huddle", code, out)
	}

	closed := httptest.NewTLSServer(http.NotFoundHandler())
	addr := closed.Listener.Addr().String()
	refused := clientTo(closed, addr)
	closed.Close()
	w = roleWorld(t, root)
	w.ProbeClient = refused
	if code, out, _ := runDomain(t, w, "domain", "--check"); code != 1 || !strings.Contains(out, "does not answer") {
		t.Errorf("domain --check with nothing listening = (%d, %q), want 1 and does not answer", code, out)
	}
}
