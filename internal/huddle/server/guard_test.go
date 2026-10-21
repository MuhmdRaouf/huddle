// The guard table (SPEC 7.5): every write route refuses a missing x-huddle-action, a wrong
// content-type and a foreign Origin with 403, and a foreign Host with 421; reads are Host-checked
// only; MCP over HTTP skips the action header and nothing else; every answer carries the strict
// CSP and its company.
package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guardDo runs one request with exactly the headers named: the guard tests control every header,
// so no helper may add one silently. "Host" names the Host header, not a real one.
func guardDo(t *testing.T, srv *childServer, method, path, body string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, srv.base+path, payload)
	if err != nil {
		t.Fatalf("build the %s %s request: %v", method, path, err)
	}
	for name, value := range headers {
		if name == "Host" {
			request.Host = value
			continue
		}
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("the %s %s request failed: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s %s answer: %v", method, path, err)
	}
	return response.StatusCode, response.Header, string(raw)
}

// guardWrite is one write route: the body and headers its pass case carries, and the status that
// proves the route itself (not a guard) answered. action "" says MCP over HTTP: no header needed.
type guardWrite struct {
	name   string
	method string
	path   string
	body   string
	action string
	want   int
}

// TestGuardWrites walks every write route through each guard: the pass case with every header
// right, then one guard broken at a time.
func TestGuardWrites(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}

	// the channel the channel-scoped rows use, with dev joined: the pass cases must reach their
	// route, not stop at a missing channel or session
	for _, boot := range []struct {
		path string
		body string
	}{
		{"/api/c/guards/op/join?as=dev", `{"role": "builds"}`},
	} {
		headers := map[string]string{"content-type": "application/json", ActionHeader: boot.path}
		for k, v := range root {
			headers[k] = v
		}
		if code, _, out := guardDo(t, srv, http.MethodPost, boot.path, boot.body, headers); code != 200 {
			t.Fatalf("bootstrap %s = %d %s", boot.path, code, out)
		}
	}

	writes := []guardWrite{
		{name: "join", method: http.MethodPost, path: "/api/join",
			body: `{"token": "nope.nothingatall", "name": "w"}`, action: "/api/join", want: 401},
		{name: "channels.create", method: http.MethodPost, path: "/api/channels",
			body: `{"name": "guards-made"}`, action: "/api/channels", want: 200},
		{name: "settings", method: http.MethodPost, path: "/api/settings",
			body: `{"notify": true}`, action: "/api/settings", want: 200},
		{name: "tokens.create", method: http.MethodPost, path: "/api/tokens",
			body: `{}`, action: "/api/tokens", want: 200},
		{name: "tokens.delete", method: http.MethodDelete, path: "/api/tokens/zzzzzz",
			action: "/api/tokens/zzzzzz", want: 404},
		{name: "members.kick", method: http.MethodDelete, path: "/api/members/nobody",
			action: "/api/members/nobody", want: 404},
		{name: "channels.remove", method: http.MethodDelete, path: "/api/channels/guards-made",
			action: "/api/channels/guards-made", want: 200},
		{name: "login", method: http.MethodPost, path: "/api/login",
			body: `{}`, action: "/api/login", want: 200},
		{name: "op", method: http.MethodPost, path: "/api/c/guards/op/status?as=dev",
			body: `{}`, action: "/api/c/guards/op/status", want: 200},
		{name: "x.rules", method: http.MethodPost, path: "/api/c/guards/x/rules?as=owner",
			body: `{}`, action: "/api/c/guards/x/rules", want: 200},
		{name: "x.approval", method: http.MethodPost, path: "/api/c/guards/x/approval?as=dev",
			body: `{"command": "ls"}`, action: "/api/c/guards/x/approval", want: 200},
		{name: "x.approvals.dismiss", method: http.MethodPost, path: "/api/c/guards/x/approvals/dismiss?as=owner",
			body: `{"all": true}`, action: "/api/c/guards/x/approvals/dismiss", want: 200},
		{name: "x.link", method: http.MethodPost, path: "/api/c/guards/x/link?as=dev",
			body: `{"claude_session": "abc123"}`, action: "/api/c/guards/x/link", want: 200},
		{name: "mcp", method: http.MethodPost, path: "/mcp/guards?as=dev",
			body: `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`, action: "", want: 200},
		{name: "mcp.delete", method: http.MethodDelete, path: "/mcp/guards?as=dev",
			action: "", want: 204},
	}

	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			// every header right: the route itself answers
			good := map[string]string{"x-huddle-token": srv.token}
			if w.body != "" || w.method != http.MethodDelete {
				good["content-type"] = "application/json"
			}
			if w.action != "" {
				good[ActionHeader] = w.action
			}
			if code, _, out := guardDo(t, srv, w.method, w.path, w.body, good); code != w.want {
				t.Fatalf("the pass case = %d %s, want %d", code, out, w.want)
			}

			// a foreign Host never reaches the route
			bad := map[string]string{}
			for k, v := range good {
				bad[k] = v
			}
			bad["Host"] = "evil.example:80"
			if code, _, _ := guardDo(t, srv, w.method, w.path, w.body, bad); code != 421 {
				t.Fatalf("a foreign host = %d, want 421", code)
			}

			// a foreign Origin never writes
			bad = map[string]string{}
			for k, v := range good {
				bad[k] = v
			}
			bad["Origin"] = "https://evil.example"
			if code, _, _ := guardDo(t, srv, w.method, w.path, w.body, bad); code != 403 {
				t.Fatalf("a foreign origin = %d, want 403", code)
			}

			// a write that names no action is refused; MCP over HTTP is the exception
			bad = map[string]string{}
			for k, v := range good {
				bad[k] = v
			}
			delete(bad, ActionHeader)
			code, _, _ := guardDo(t, srv, w.method, w.path, w.body, bad)
			if w.action == "" {
				if code != w.want {
					t.Fatalf("mcp without the action header = %d, want %d (the exemption)", code, w.want)
				}
			} else if code != 403 {
				t.Fatalf("a write without %s = %d, want 403", ActionHeader, code)
			}

			// a wrong content-type is refused with 403 — a delete carries no body, so none is needed
			if w.method == http.MethodDelete {
				return
			}
			bad = map[string]string{}
			for k, v := range good {
				bad[k] = v
			}
			bad["content-type"] = "text/plain"
			if code, _, _ := guardDo(t, srv, w.method, w.path, w.body, bad); code != 403 {
				t.Fatalf("a form write = %d, want 403", code)
			}
		})
	}
}

// TestGuardReads proves reads are Host-checked only: no Origin rule, no action header, and a
// foreign Host still refused.
func TestGuardReads(t *testing.T) {
	srv := startServer(t, nil)
	if code, _, _ := guardDo(t, srv, http.MethodGet, "/health",
		"", map[string]string{"Origin": "https://evil.example"}); code != 200 {
		t.Fatalf("a read with a foreign origin = %d, want 200", code)
	}
	if code, _, _ := guardDo(t, srv, http.MethodGet, "/api/channels", "",
		map[string]string{"x-huddle-token": srv.token, "Origin": "https://evil.example"}); code != 200 {
		t.Fatalf("an api read with a foreign origin = %d, want 200", code)
	}
	if code, _, _ := guardDo(t, srv, http.MethodGet, "/health", "",
		map[string]string{"Host": "evil.example"}); code != 421 {
		t.Fatalf("a read with a foreign host = %d, want 421", code)
	}
}

// TestGuardSecurityHeaders pins the headers every answer carries: the exact CSP of SPEC 7.5 and
// its three companions, on a page, a JSON answer, a not-found and the guards' own refusals.
func TestGuardSecurityHeaders(t *testing.T) {
	srv := startServer(t, nil)
	answers := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
	}{
		{"the page", http.MethodGet, "/", "", nil},
		{"a json answer", http.MethodGet, "/health", "", nil},
		{"a not-found", http.MethodGet, "/api/nope", "", map[string]string{"x-huddle-token": srv.token}},
		{"the host guard's refusal", http.MethodGet, "/health", "", map[string]string{"Host": "evil.example"}},
		{"the write guard's refusal", http.MethodPost, "/api/settings", "{}",
			map[string]string{"content-type": "application/json", "x-huddle-token": srv.token}},
	}
	for _, a := range answers {
		t.Run(a.name, func(t *testing.T) {
			code, header, _ := guardDo(t, srv, a.method, a.path, a.body, a.headers)
			if a.name == "the write guard's refusal" && code != 403 {
				t.Fatalf("the refusal = %d, want 403", code)
			}
			if got := header.Get("Content-Security-Policy"); got != ContentSecurityPolicy {
				t.Fatalf("csp = %q, want the exact policy", got)
			}
			for name, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "no-referrer",
				"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
			} {
				if got := header.Get(name); got != want {
					t.Fatalf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// TestGuardHubHost proves the home's config.toml hub.host is an allowed Host alongside loopback:
// the name the owner's network reaches the hub by (SPEC 7.5), while any other still gets 421.
func TestGuardHubHost(t *testing.T) {
	home := t.TempDir()
	config := "[hub]\nhost = \"hub.mine.example\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_HOME": home})
	if code, _, _ := guardDo(t, srv, http.MethodGet, "/health",
		"", map[string]string{"Host": "hub.mine.example"}); code != 200 {
		t.Fatalf("the config's hub.host = %d, want 200", code)
	}
	if code, _, _ := guardDo(t, srv, http.MethodGet, "/health",
		"", map[string]string{"Host": fmt.Sprintf("localhost:%d", srv.port)}); code != 200 {
		t.Fatalf("loopback = %d, want 200", code)
	}
	if code, _, _ := guardDo(t, srv, http.MethodGet, "/health",
		"", map[string]string{"Host": "other.example"}); code != 421 {
		t.Fatalf("another host = %d, want 421", code)
	}
}
