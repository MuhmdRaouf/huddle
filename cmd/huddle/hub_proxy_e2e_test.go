package main

// The hub behind a TLS reverse proxy, end to end: `huddle role hub --host` with no listener of
// its own, the printed sign-in link naming the proxy's origin, and that link redeemed through a
// local TLS proxy that forwards to the loopback listener. The proxy is an httptest TLS server
// this test owns; the client resolves the hub's name to it, as the owner's DNS resolves it to
// the real proxy.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

func TestE2EHubBehindATLSReverseProxy(t *testing.T) {
	// a short root: the homes and sockets under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-hpx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	hubHome := filepath.Join(root, "huddle-home")
	hubPort := ports.Free(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))
	owner := argvLayer(env, map[string]string{
		"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL, "HUDDLE_TOKEN": "hub-proxy-e2e-root",
	})
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = runBin(t, huddleBin, owner, proj, "down")
		if pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) }) {
			return
		}
		raw, err := os.ReadFile(filepath.Join(hubHome, "run", "huddle.pid"))
		if err != nil {
			return
		}
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) }) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	if code, out, errOut := runBin(t, huddleBin, owner, proj, "role", "hub", "--host", "hub.example.invalid"); code != 0 {
		t.Fatalf("role hub --host = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if code, out, errOut := runBin(t, huddleBin, owner, proj, "up"); code != 0 {
		t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")

	code, printed, errOut := runBin(t, huddleBin, owner, proj, "open", "--print")
	if code != 0 {
		t.Fatalf("open --print = %d\nstdout:\n%s\nstderr:\n%s", code, printed, errOut)
	}
	link := regexp.MustCompile(`^https://hub\.example\.invalid/\?code=\S+$`).FindString(strings.TrimSpace(printed))
	if link == "" {
		t.Fatalf("open --print printed %q, want https://hub.example.invalid/?code=…", printed)
	}

	// the proxy: TLS in front, plain http to the loopback listener, the Host kept and the
	// X-Forwarded-* headers set the way Caddy sets them
	target, err := url.Parse(hubURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host
		r.SetXForwarded()
	}})
	t.Cleanup(proxy.Close)

	transport := proxy.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com" // the name httptest's certificate carries
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{
		Transport:     transport,
		Jar:           jar,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	response, err := browser.Get(link)
	if err != nil {
		t.Fatalf("redeem the link through the proxy: %v", err)
	}
	_ = response.Body.Close()
	set := response.Header.Get("Set-Cookie")
	if response.StatusCode != 303 || !strings.Contains(set, "; Secure") {
		t.Fatalf("the redemption through the proxy = %d, Set-Cookie %q; want 303 and a Secure cookie", response.StatusCode, set)
	}

	response, err = browser.Get("https://hub.example.invalid/api/channels")
	if err != nil {
		t.Fatalf("read the channels through the proxy: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("/api/channels with the cookie through the proxy = %d, want 200", response.StatusCode)
	}
}
