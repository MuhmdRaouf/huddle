package server

// A server whose registry never opened: every channel works, and each route that wanted the
// registry refuses with its own line — the hooks outbox their reports, the owner's lists and
// actions wait for the next start, and the pairing ceremonies say the store is unavailable.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startServerWithoutRegistry starts a child server whose hub.db cannot open, so the server runs
// with no registry and no pairing core.
func startServerWithoutRegistry(t *testing.T) *childServer {
	t.Helper()
	home := tempDir("huddle-broken-home-")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hub.db"), []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	return startServer(t, map[string]string{"HUDDLE_HOME": home})
}

func TestARouteWithoutItsRegistryRefusesWithItsOwnLine(t *testing.T) {
	srv := startServerWithoutRegistry(t)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"the needs list", "GET", "/api/needs", ""},
		{"a snooze", "POST", "/api/needs/1/snooze", `{"until":"15m"}`},
		{"an answer", "POST", "/api/sessions/s-1/answer", `{"text":"hi"}`},
		{"a take", "POST", "/api/sessions/s-1/messages", `{}`},
		{"a send", "POST", "/api/send", `{"sessions":["s-1"],"text":"hi"}`},
		{"a push ack", "POST", "/api/registry/push-ack", `{"channel":"c","as":"m"}`},
		{"a report", "POST", "/api/registry/report",
			`{"session_id":"s-1","last_event":"Stop","last_event_at":"2026-10-16T12:00:00.000Z"}`},
		{"the session list", "GET", "/api/registry/sessions", ""},
		{"one session", "GET", "/api/registry/sessions/s-1", ""},
		{"the live list", "GET", "/api/registry/live", ""},
		{"a permission item", "POST", "/api/needs", `{"session_id":"s","tool_name":"Bash","tool_input":{}}`},
		{"a decision", "POST", "/api/needs/1/decide", `{"decision":"deny"}`},
		{"a poll", "GET", "/api/needs/1/wait?timeout=1", ""},
		{"the audit log", "GET", "/api/audit", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{"x-huddle-token": srv.token}
			if tc.method == http.MethodPost {
				headers["content-type"] = "application/json"
			}
			code, _, body, err := srv.request(bg, tc.method, tc.path, tc.body, headers)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if code != 500 || !strings.Contains(body, "the registry's database is unavailable") {
				t.Fatalf("%s = %d %s, want the registry's 500", tc.name, code, body)
			}
		})
	}
}

func TestAPairingCeremonyWithoutItsStoreSaysSo(t *testing.T) {
	srv := startServerWithoutRegistry(t)
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{"a pairing begin", "/api/pair/begin", `{"token":"A234567890BCDEFGHIJKLMNOPQR","name":"iPhone"}`},
		{"a pairing finish", "/api/pair/finish", `{"token":"A234567890BCDEFGHIJKLMNOPQR","response":{}}`},
		{"a sign-in begin", "/api/login/begin", `{}`},
		{"a sign-in finish", "/api/login/finish", `{"response":{}}`},
		{"a step-up begin", "/api/stepup/begin", `{}`},
		{"a step-up finish", "/api/stepup/finish", `{"response":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := srv.post(t, tc.path, tc.body, nil)
			if code != 500 || !strings.Contains(out, "the pairing store is unavailable") {
				t.Fatalf("%s = %d %s, want the pairing store's 500", tc.name, code, out)
			}
		})
	}
	// the channels still serve: the health answer is a 200, a join works, a message flows
	if code, _, body := srv.get(t, "/health", nil); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("/health = %d %s, want the server healthy without its registry", code, body)
	}
	if code, out := srv.post(t, "/api/channels", `{"name":"solo"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	if code, out := srv.op(t, "solo", "join", "me", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	if code, out := srv.op(t, "solo", "send", "me", `{"to":"me","msg":"still here"}`); code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
}

func TestAReportToARegistrylessServerCarriesNoRow(t *testing.T) {
	srv := startServerWithoutRegistry(t)
	// a report whose fields are torn is refused before the registry is even asked
	code, out := srv.post(t, "/api/registry/report", `{"session_id":"s-1"}`, nil)
	if code != 400 || !strings.Contains(out, "a report needs session_id, last_event and last_event_at") {
		t.Fatalf("a torn report = %d %s, want the 400", code, out)
	}
	// the health answer still names this home, so a restart finds it
	code, _, body := srv.get(t, "/health", nil)
	if code != 200 {
		t.Fatalf("/health = %d", code)
	}
	if home := bodyMap(t, body)["home"]; home == "" {
		t.Errorf("the health answer names no home: %s", body)
	}
}
