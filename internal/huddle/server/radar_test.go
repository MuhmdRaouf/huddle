// The radar tab as the served server runs it: /api/radar answers radar's routes on huddle's own
// port behind huddle's credential, radar's health path answers there openly while huddle owns
// the lock, and the lock names the server process so a standalone radar reuses it. Huddle's own
// API keeps its credential guard.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRadarTabServed(t *testing.T) {
	srv := startServer(t, nil)

	t.Run("radar's routes answer under /api/radar on this port", func(t *testing.T) {
		code, _, body := srv.get(t, "/api/radar/api/health", nil)
		if code != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("the tab's health = %d %s", code, body)
		}
		code, _, body = srv.get(t, "/api/radar/api/summary", nil)
		if code != 200 || !strings.Contains(body, `"summary":`) {
			t.Fatalf("the tab's summary = %d %s", code, body)
		}
		// an unknown radar route keeps radar's own 404, routes and all
		code, _, body = srv.get(t, "/api/radar/api/nope", nil)
		if code != 404 || !strings.Contains(body, "no such route: /api/nope") {
			t.Fatalf("the tab's unknown route = %d %s", code, body)
		}
	})

	t.Run("radar's health path answers here: the lock names this server", func(t *testing.T) {
		code, _, body := srv.get(t, "/api/health", nil)
		if code != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("/api/health = %d %s, want radar's answer", code, body)
		}
		record, err := os.ReadFile(filepath.Join(srv.radarHome, "server.json"))
		if err != nil {
			t.Fatalf("the lock never landed: %v", err)
		}
		var lock struct {
			Pid   int64  `json:"pid"`
			Port  int    `json:"port"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(record, &lock); err != nil {
			t.Fatalf("the lock = %s", record)
		}
		if lock.Pid != int64(srv.cmd.Process.Pid) {
			t.Fatalf("the lock names pid %d, want the server's %d", lock.Pid, srv.cmd.Process.Pid)
		}
		if lock.Port != srv.port {
			t.Fatalf("the lock names port %d, want the server's %d", lock.Port, srv.port)
		}
		if lock.Token == "" || lock.Token == srv.token {
			t.Fatalf("the lock's tab token = %q, want one distinct from the server's root", lock.Token)
		}
	})

	t.Run("the lock's tab token reads radar's API: the proxying huddle's credential", func(t *testing.T) {
		record, err := os.ReadFile(filepath.Join(srv.radarHome, "server.json"))
		if err != nil {
			t.Fatalf("the lock never landed: %v", err)
		}
		var lock struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(record, &lock); err != nil || lock.Token == "" {
			t.Fatalf("the lock = %s (%v), want a tab token", record, err)
		}
		// the token alone — no member's credential — reads radar here, the way a huddle that
		// proxies to this owner asks
		code, _, body, err := srv.request(context.Background(), http.MethodGet, "/api/radar/api/summary", "",
			map[string]string{"x-huddle-token": lock.Token})
		if err != nil || code != 200 || !strings.Contains(body, `"summary":`) {
			t.Fatalf("the tab-token radar summary = %d %s (%v)", code, body, err)
		}
		// any other word is refused like any other credential
		code, _, _, err = srv.request(context.Background(), http.MethodGet, "/api/radar/api/summary", "",
			map[string]string{"x-huddle-token": lock.Token + "x"})
		if err != nil || code != 401 {
			t.Fatalf("the near-miss tab token = %d (%v), want 401", code, err)
		}
		// the token is radar's, not huddle's: it opens no other route
		code, _, _, err = srv.request(context.Background(), http.MethodGet, "/api/channels", "",
			map[string]string{"x-huddle-token": lock.Token})
		if err != nil || code != 401 {
			t.Fatalf("the tab token on huddle's own API = %d (%v), want 401", code, err)
		}
	})

	t.Run("huddle's own API keeps its credential guard", func(t *testing.T) {
		code, _, _, err := srv.request(context.Background(), http.MethodGet, "/api/channels", "", nil)
		if err != nil || code != 401 {
			t.Fatalf("the uncredentialed channels = %d (%v), want 401", code, err)
		}
		code, _, _, err = srv.request(context.Background(), http.MethodGet, "/api/radar/api/summary", "",
			map[string]string{"Host": "evil.example"})
		if err != nil || code != 421 {
			t.Fatalf("a foreign host through the tab = %d (%v), want the guard's 421", code, err)
		}
	})

	t.Run("the tab takes the writes this server's own guard takes", func(t *testing.T) {
		hub := startServer(t, map[string]string{"HUDDLE_HOSTS": "hub.example.test:8443"})
		budgets := `{"budgets":[{"id":"a","scope":"total","period":"week","limitUsd":3,"action":"warn"}]}`
		write := func(origin string) int {
			code, _, _, err := hub.request(context.Background(), http.MethodPut,
				"/api/radar/api/budgets", budgets, map[string]string{
					"content-type":   "application/json",
					"Origin":         origin,
					"x-huddle-token": hub.token,
				})
			if err != nil {
				t.Fatalf("the budget write from %s: %v", origin, err)
			}
			return code
		}
		// the host HUDDLE_HOSTS names is one this server answers, so its own write guard takes
		// the origin spelled with it — and the tab takes that write too
		if code := write("https://hub.example.test:8443"); code != 200 {
			t.Fatalf("the write from the named host's origin = %d, want 200", code)
		}
		if code := write("https://evil.example"); code != 403 {
			t.Fatalf("the write from a foreign origin = %d, want 403", code)
		}
		// the loopback origin radar would take alone still passes: the server's own list has it
		if code := write(fmt.Sprintf("http://127.0.0.1:%d", hub.port)); code != 200 {
			t.Fatalf("the write from the loopback origin = %d, want 200", code)
		}
	})

	t.Run("radar's API answers only a request with huddle's credential; health stays open", func(t *testing.T) {
		// a browser with no credential is told to sign in, the same answer huddle's own API gives
		code, _, body, err := srv.request(context.Background(), http.MethodGet, "/api/radar/api/summary", "", nil)
		if err != nil || code != 401 {
			t.Fatalf("the uncredentialed radar summary = %d (%v), want 401", code, err)
		}
		parsed := bodyMap(t, body)
		if parsed["signin"] != true {
			t.Fatalf("the uncredentialed radar summary = %s, want huddle's sign-in answer", body)
		}
		// a session holding a credential that no longer works is told how to get a new one
		code, _, body, err = srv.request(context.Background(), http.MethodGet, "/api/radar/api/summary", "",
			map[string]string{"x-huddle-token": "stale"})
		if err != nil || code != 401 {
			t.Fatalf("the stale-token radar summary = %d (%v), want 401", code, err)
		}
		if parsed = bodyMap(t, body); parsed["signin"] != nil {
			t.Fatalf("the stale-token radar summary = %s, want huddle's session answer", body)
		}
		// with the credential, the tab answers; the health path radar's CLI probes needs none
		code, _, body, err = srv.request(context.Background(), http.MethodGet, "/api/radar/api/summary", "",
			map[string]string{"x-huddle-token": srv.token})
		if err != nil || code != 200 || !strings.Contains(body, `"summary":`) {
			t.Fatalf("the credentialed radar summary = %d %s (%v)", code, body, err)
		}
		code, _, body, err = srv.request(context.Background(), http.MethodGet, "/api/health", "", nil)
		if err != nil || code != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("the uncredentialed health = %d %s (%v), want radar's open answer", code, body, err)
		}
	})
}
