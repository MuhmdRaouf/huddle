package server

// The hub behind a TLS reverse proxy on this machine: the proxy terminates TLS and speaks plain
// http to the loopback listener, naming the scheme it served in X-Forwarded-Proto. The sign-in
// cookie carries Secure when that scheme was https, so the browser keeps it to the https origin.

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signIn redeems one fresh dashboard code on the loopback listener with the headers given and
// answers the response's status and Set-Cookie line.
func signIn(t *testing.T, srv *childServer, headers map[string]string) (int, string) {
	t.Helper()
	_, out := srv.post(t, "/api/login", "{}",
		map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"})
	request, err := http.NewRequest(http.MethodGet, srv.base+"/?code="+bodyMap(t, out)["code"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.StatusCode, response.Header.Get("Set-Cookie")
}

// TestASignInBehindTLSProxySetsASecureCookie pins both sides: a redemption a local proxy
// forwarded from https gets a Secure cookie, a plain loopback one does not.
func TestASignInBehindTLSProxySetsASecureCookie(t *testing.T) {
	srv := startServer(t, nil)

	code, set := signIn(t, srv, map[string]string{"X-Forwarded-Proto": "https"})
	if code != 303 || !strings.Contains(set, "; Secure") {
		t.Errorf("a sign-in forwarded from https = %d %q, want 303 and a Secure cookie", code, set)
	}
	code, set = signIn(t, srv, nil)
	if code != 303 || strings.Contains(set, "Secure") {
		t.Errorf("a plain loopback sign-in = %d %q, want 303 and no Secure", code, set)
	}
	code, set = signIn(t, srv, map[string]string{"X-Forwarded-Proto": "http"})
	if code != 303 || strings.Contains(set, "Secure") {
		t.Errorf("a sign-in forwarded from http = %d %q, want 303 and no Secure", code, set)
	}
}

// TestForwardedProtoIsTrustedFromLoopbackOnly pins who may vouch for https: the listener's own
// TLS, a loopback peer naming https, and nobody else.
func TestForwardedProtoIsTrustedFromLoopbackOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote string
		proto  string
		tls    bool
		want   bool
	}{
		{"the listener's own TLS", "100.85.40.2:51000", "", true, true},
		{"a local proxy from https", "127.0.0.1:51000", "https", false, true},
		{"a local IPv6 proxy from https", "[::1]:51000", "HTTPS", false, true},
		{"a local proxy from http", "127.0.0.1:51000", "http", false, false},
		{"plain loopback", "127.0.0.1:51000", "", false, false},
		{"a network peer naming https", "192.0.2.10:51000", "https", false, false},
		{"an unreadable peer", "nonsense", "https", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			if !tc.tls {
				r.TLS = nil
			} else {
				r.TLS = &tls.ConnectionState{}
			}
			if got := servedOverHTTPS(r); got != tc.want {
				t.Errorf("servedOverHTTPS = %v, want %v", got, tc.want)
			}
		})
	}
}
