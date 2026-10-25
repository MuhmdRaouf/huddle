// Package tlsproxy is the TLS reverse proxy the phone tests put in front of a temp hub: a local
// CA under the test's temp dir signs the proxy's leaf for localhost and 127.0.0.1, so a client
// that trusts the test root (a simulator's keychain, a Go client) reaches the hub over HTTPS the
// way a phone reaches it through Caddy. Requests go to the hub over plain http with the
// X-Forwarded-* headers set; the hub trusts X-Forwarded-Proto from a loopback peer only.
package tlsproxy

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
)

// Proxy is one running proxy, closed when its test ends.
type Proxy struct {
	// URL is the proxy's origin: https://127.0.0.1:<a free port>.
	URL string
	// RootFile is the test root's PEM, for a trust store the test owns.
	RootFile string
	// Client trusts the test root and nothing else.
	Client *http.Client
}

// Start serves a proxy to target (the hub's http origin) on a free loopback port.
func Start(t testing.TB, target string) *Proxy {
	t.Helper()
	upstream, err := url.Parse(target)
	if err != nil {
		t.Fatalf("tlsproxy: the target %q: %v", target, err)
	}
	ca := pki.New(t.TempDir(), pki.Options{})
	root, err := ca.Root()
	if err != nil {
		t.Fatalf("tlsproxy: the test root: %v", err)
	}
	leaf, err := pki.NewLeaf(ca, pki.HubLeafNames(""))
	if err != nil {
		t.Fatalf("tlsproxy: the proxy's leaf: %v", err)
	}
	cert, err := leaf.GetCertificate(nil)
	if err != nil {
		t.Fatalf("tlsproxy: the proxy's leaf: %v", err)
	}

	server := httptest.NewUnstartedServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.SetXForwarded()
	}})
	// a client dialing 127.0.0.1 sends no server name, so the leaf is the one certificate served
	server.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(func() {
		// a browser's live stream would hold Close open: its connections go first
		server.CloseClientConnections()
		server.Close()
	})

	pool := x509.NewCertPool()
	pool.AddCert(root)
	return &Proxy{
		URL:      server.URL,
		RootFile: ca.RootFile(),
		Client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
	}
}
