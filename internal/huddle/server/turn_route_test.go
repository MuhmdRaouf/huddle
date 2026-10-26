package server

// The session turn route (turn_route.go): one GET naming what the session's transcript says
// about its current turn — and, when the turn started, the main agent's output tokens radar
// holds since that start. Every test runs its own server child against its own transcript, and
// reuses the report and transcript writers the tasks route tests hold.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
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

// TestTheSessionTurnRouteAnswersTheTurnsOutputTokens: a running turn carries the main agent's
// output tokens radar recorded since the turn started — the current turn's replies summed, an
// earlier turn's reply left out. The child's radar tab ingests the transcripts under its own
// CLAUDE_CONFIG_DIR, so the seed lands through the tab's watcher, the way a live session feeds
// it.
func TestTheSessionTurnRouteAnswersTheTurnsOutputTokens(t *testing.T) {
	config := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config, "projects", "huddle"), 0o755); err != nil {
		t.Fatalf("make the projects dir: %v", err)
	}
	srv := startServer(t, map[string]string{"CLAUDE_CONFIG_DIR": config})

	now := time.Now()
	minutesAgo := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	prompt := func(text string, at time.Time) string {
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`,
			jsscalar.ISOTime(at), text)
	}
	// the fields radar's transcript reader needs to record one request: an assistant entry
	// with a timestamp, a model, a message id and a usage block holding output_tokens
	reply := func(id string, output int64, at time.Time) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"test-model",`+
			`"usage":{"input_tokens":10,"output_tokens":%d}}}`, jsscalar.ISOTime(at), id, output)
	}
	lines := []string{
		// the earlier turn: its prompt and its one reply, summed by no later answer
		prompt("start the work", minutesAgo(3)),
		reply("msg-early", 111, minutesAgo(2).Add(-30*time.Second)),
		// the current turn: its prompt and its two replies
		prompt("keep going", minutesAgo(1)),
		reply("msg-a", 120, now.Add(-40*time.Second)),
		reply("msg-b", 34, now.Add(-20*time.Second)),
	}
	path := filepath.Join(config, "projects", "huddle", "s-tokens.jsonl")
	tmp := path + ".tmp"
	whole := ""
	for _, line := range lines {
		whole += line + "\n"
	}
	if err := os.WriteFile(tmp, []byte(whole), 0o644); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("land the transcript: %v", err)
	}
	reportTasksSession(t, srv, "s-tokens", "", "", "", path)

	// the watcher's pass lands the requests in the tab's store within a few seconds; until
	// then the route answers 0, an empty request list being a real answer
	deadline := time.Now().Add(10 * time.Second)
	var body string
	for {
		code, _, out := srv.get(t, "/api/sessions/s-tokens/turn", nil)
		if code != 200 {
			t.Fatalf("turn: %d %s", code, out)
		}
		body = out
		if got, _ := bodyMap(t, body)["output_tokens"].(float64); got == 154 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output_tokens never reached 154; the turn answer = %s", body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	page := bodyMap(t, body)
	if page["started_at"] != jsscalar.ISOTime(minutesAgo(1)) {
		t.Errorf("started_at = %v, want the current prompt's timestamp", page["started_at"])
	}
	if page["ended"] != false {
		t.Errorf("ended = %v, want false: no turn_duration follows the last prompt", page["ended"])
	}
	if got, _ := page["output_tokens"].(float64); got != 154 {
		t.Errorf("output_tokens = %v, want 154: the two current replies' sum, the earlier turn's reply left out", page["output_tokens"])
	}
}
