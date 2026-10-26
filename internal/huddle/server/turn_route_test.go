package server

// The session turn route (turn_route.go): one GET naming what the session's transcript says
// about its current turn. Every test runs its own server child against its own transcript, and
// reuses the report and transcript writers the tasks route tests hold.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTheSessionTurnRouteAnswersTheTranscriptFacts(t *testing.T) {
	srv := startServer(t, nil)
	path := writeTasksTranscript(t,
		`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"user","content":"start the work"}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":99000,"isMeta":false,"timestamp":"2026-10-26T10:01:39.000Z"}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"s-turn"}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-10-26T10:02:00.000Z","message":{"role":"user","content":"keep going"}}`,
	)
	reportTasksSession(t, srv, "s-turn", "", "", "", path)

	code, _, body := srv.get(t, "/api/sessions/s-turn/turn", nil)
	if code != 200 {
		t.Fatalf("turn: %d %s", code, body)
	}
	page := bodyMap(t, body)
	if page["started_at"] != "2026-10-26T10:02:00.000Z" {
		t.Errorf("started_at = %v, want the last prompt's timestamp", page["started_at"])
	}
	if page["ended"] != false {
		t.Errorf("ended = %v, want false: no turn_duration follows the last prompt", page["ended"])
	}
	if _, held := page["duration_ms"]; held {
		t.Errorf("duration_ms = %v, want no field on a running turn", page["duration_ms"])
	}
	if page["permission_mode"] != "bypassPermissions" {
		t.Errorf("permission_mode = %v, want the last permission-mode line's mode", page["permission_mode"])
	}
}

func TestTheSessionTurnRouteRefusesAnUnknownSession(t *testing.T) {
	srv := startServer(t, nil)
	code, _, body := srv.get(t, "/api/sessions/no-such/turn", nil)
	if code != 404 || !strings.Contains(body, "no such session") {
		t.Errorf("an unknown session = %d %s, want 404 no such session", code, body)
	}
}

func TestTheSessionTurnRouteAnswersEndedFalseWithoutATranscript(t *testing.T) {
	srv := startServer(t, nil)
	reportTasksSession(t, srv, "s-bare", "", "", "", "")
	reportTasksSession(t, srv, "s-gone", "", "", "", filepath.Join(t.TempDir(), "missing.jsonl"))

	for _, id := range []string{"s-bare", "s-gone"} {
		code, _, body := srv.get(t, "/api/sessions/"+id+"/turn", nil)
		if code != 200 {
			t.Fatalf("%s turn: %d %s", id, code, body)
		}
		if got := strings.TrimSpace(body); got != `{"ended":false}` {
			t.Errorf("%s turn = %s, want {\"ended\":false}", id, got)
		}
	}
}
