package server

// Hooks stale: a running session whose hook commands went dead keeps writing its transcript while
// no report reaches the registry. The registry reads the transcript beside the reports: a session
// whose transcript is newer than its last report by more than 2 minutes while its pid lives reads
// hooks_stale, a live transcript with no registry row gets a row of its own, and a session that
// keeps reporting never shows the flag. The pids here are this test process's own: alive, and
// owned by the test.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// appendTranscript appends one user line stamped now to the session's transcript under
// <claude>/projects, the way Claude Code writes it.
func appendTranscript(t *testing.T, claude, id, cwd string) {
	t.Helper()
	dir := filepath.Join(claude, "projects", "-w-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("make the transcript dir: %v", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open the transcript: %v", err)
	}
	defer file.Close()
	line := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":%q,"message":{"role":"user","content":"go on"},"timestamp":%q}`,
		id, cwd, jsscalar.ISOTime(time.Now()))
	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatalf("append the transcript: %v", err)
	}
}

// staleRow is the part of a registry row this test reads.
type staleRow struct {
	SessionID  string `json:"session_id"`
	Cwd        string `json:"cwd"`
	State      string `json:"state"`
	HooksStale bool   `json:"hooks_stale"`
}

// listedRows reads /api/registry/sessions by session id.
func listedRows(t *testing.T, srv *childServer) map[string]staleRow {
	t.Helper()
	code, _, out := srv.get(t, "/api/registry/sessions", nil)
	if code != 200 {
		t.Fatalf("list sessions = %d %s", code, out)
	}
	var answer struct {
		Sessions []staleRow `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatalf("list sessions: %v in %s", err, out)
	}
	rows := map[string]staleRow{}
	for _, row := range answer.Sessions {
		rows[row.SessionID] = row
	}
	return rows
}

func TestASessionWhoseTranscriptMovesWithoutReportsReadsHooksStale(t *testing.T) {
	claude := t.TempDir()
	pid := os.Getpid()
	// Claude Code's own registry names the transcript-only session's pid: <claude>/sessions/<pid>.json
	if err := os.MkdirAll(filepath.Join(claude, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf(`{"pid":%d,"sessionId":"s-unseen","cwd":"/w/unseen","startedAt":%d}`, pid, time.Now().UnixMilli())
	if err := os.WriteFile(filepath.Join(claude, "sessions", fmt.Sprintf("%d.json", pid)), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, map[string]string{"CLAUDE_CONFIG_DIR": claude})

	// s-quiet reported once, fifteen minutes ago, and its transcript kept moving since
	quiet := fmt.Sprintf(`{"session_id":"s-quiet","cwd":"/w/quiet","pid":%d,"last_event":"PostToolUse","last_event_at":%q}`,
		pid, isoAgo(15*time.Minute))
	if code, out := srv.post(t, "/api/registry/report", quiet, nil); code != 200 {
		t.Fatalf("post the quiet session's report: %d %s", code, out)
	}
	// s-talking reports as its transcript moves
	talking := fmt.Sprintf(`{"session_id":"s-talking","cwd":"/w/talking","pid":%d,"last_event":"PostToolUse","last_event_at":%q}`,
		pid, isoAgo(time.Second))
	if code, out := srv.post(t, "/api/registry/report", talking, nil); code != 200 {
		t.Fatalf("post the talking session's report: %d %s", code, out)
	}
	for _, id := range []string{"s-quiet", "s-talking", "s-unseen"} {
		appendTranscript(t, claude, id, "/w/"+id[2:])
		appendTranscript(t, claude, id, "/w/"+id[2:])
	}

	var rows map[string]staleRow
	if !until(20*time.Second, func() bool {
		rows = listedRows(t, srv)
		return rows["s-quiet"].HooksStale && rows["s-unseen"].HooksStale
	}) {
		t.Fatalf("the rows never read hooks stale: %+v", rows)
	}

	t.Run("a reported session whose transcript outruns its reports reads hooks stale, not stuck", func(t *testing.T) {
		if got := rows["s-quiet"]; got.State != "working" {
			t.Errorf("s-quiet = %+v, want working while its transcript moves", got)
		}
		code, _, out := srv.get(t, "/api/registry/sessions/s-quiet", nil)
		if code != 200 {
			t.Fatalf("the session = %d %s", code, out)
		}
		var detail staleRow
		if err := json.Unmarshal([]byte(out), &detail); err != nil {
			t.Fatal(err)
		}
		if !detail.HooksStale || detail.State != "working" {
			t.Errorf("the detail = %+v, want hooks stale and working, the list's own reading", detail)
		}
	})

	t.Run("a transcript with no registry row gets a row from its cwd and session id", func(t *testing.T) {
		got := rows["s-unseen"]
		if got.Cwd != "/w/unseen" || got.State != "working" {
			t.Errorf("s-unseen = %+v, want its transcript's cwd and working", got)
		}
	})

	t.Run("a session that keeps reporting never shows the flag", func(t *testing.T) {
		got, listed := rows["s-talking"]
		if !listed || got.HooksStale {
			t.Errorf("s-talking = %+v (listed %v), want listed without the flag", got, listed)
		}
	})
}
