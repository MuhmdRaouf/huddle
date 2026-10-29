package ingest

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// An id-less transcript copy (a subagent entry) joins its route line's ledger row: history
// pairs the two by closest fit, and the copy's facts file under the request id the paired
// call carries — never a second row under the copy's own message id.

// idlessLedger opens the case's ledger and answers it with its file's path, so the row
// count reads straight off the file.
func idlessLedger(t *testing.T) (*ledger.Store, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ledger.db")
	led, err := ledger.Open(file)
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	return led, file
}

// idlessWatcher is a manual watcher with the ledger attached, its clock standing at t1.
func idlessWatcher(t *testing.T, led *ledger.Store) *Watcher {
	t.Helper()
	home := t.TempDir()
	env := domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
	}
	w := StartWatcher(t.Context(), WatcherOptions{
		Env:        env,
		Store:      store.CreateStore(time.Now),
		Ledger:     led,
		SinceMs:    24 * 3_600_000,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        func() int64 { return t1 },
		Alive:      func(int64) bool { return false },
		NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
	})
	t.Cleanup(w.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for w.CatchingUp() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the initial ingest")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return w
}

// idlessRouteLine is one route line for the subagent call: s9's agent-sub1 on glm-5.3,
// within the match window of the transcript line the same case feeds. The request id rides
// as the request-id header when one is named, a prompt hash when it is not.
func idlessRouteLine(t *testing.T, ts int64, requestID string) string {
	t.Helper()
	line := map[string]any{
		"ts": iso(ts), "event": "route", "session_id": "s9", "agent_id": "agent-sub1",
		"model": "glm-5.3", "upstream": "http://127.0.0.1:8787", "route": "provider",
		"status": 200, "latency_ms": 42,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20},
	}
	if requestID != "" {
		line["headers"] = map[string]string{"request-id": requestID}
	} else {
		line["prompt_hash"] = "hash7sub1"
	}
	data, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the route line: %v", err)
	}
	return string(data)
}

// idlessSubagentLine is one subagent assistant entry with no requestId: the record rides
// the message's own id, one text block, the transcript's own stop reason and usage.
func idlessSubagentLine(ts int64) map[string]any {
	return map[string]any{
		"type": "assistant", "timestamp": iso(ts), "sessionId": "s9",
		"message": map[string]any{
			"role": "assistant", "id": "msg_sub1", "model": "glm-5.3",
			"stop_reason": "end_turn",
			"content":     []any{map[string]any{"type": "text", "text": "Scouting the suite."}},
			"usage":       map[string]any{"input_tokens": 80, "output_tokens": 22},
		},
	}
}

// ledgerIDs answers every row id the ledger file holds, sorted.
func ledgerIDs(t *testing.T, file string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatalf("open the ledger file: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT id FROM ledger_requests ORDER BY id")
	if err != nil {
		t.Fatalf("read the ledger rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan the ledger row: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the ledger rows: %v", err)
	}
	return ids
}

// checkJoinedRow pins the one row both sources read: the route's request id, the
// transcript's own what, stop reason and arrival mark, and no second row.
func checkJoinedRow(t *testing.T, led *ledger.Store, file string) {
	t.Helper()
	row, err := led.Get(t.Context(), "req_sub_1")
	if err != nil {
		t.Fatalf("read the joined row: %v", err)
	}
	if row == nil {
		t.Fatalf("no ledger row for req_sub_1, rows = %v", ledgerIDs(t, file))
	}
	if !row.InTranscript {
		t.Error("InTranscript = false, want the copy's facts on the route's row")
	}
	if row.What == nil || *row.What != "→ text" {
		t.Errorf("What = %v, want the transcript's → text", row.What)
	}
	if row.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want the transcript's end_turn", row.StopReason)
	}
	ids := ledgerIDs(t, file)
	if len(ids) != 1 || ids[0] != "req_sub_1" {
		t.Errorf("ledger rows = %v, want exactly req_sub_1", ids)
	}
}

func TestTheWatcherFilesAnIdlessCopyWithItsRoute(t *testing.T) {
	subagentFile := FileIdentity{SessionID: "s9", AgentID: strp("agent-sub1"), Kind: shared.KindSubagent}

	t.Run("route line first", func(t *testing.T) {
		led, file := idlessLedger(t)
		w := idlessWatcher(t, led)
		w.ingestSpoolText(idlessRouteLine(t, t1, "req_sub_1"))
		w.applyEmit(feedFile(NewTranscriptState(), idlessSubagentLine(t2), subagentFile))
		checkJoinedRow(t, led, file)
	})

	t.Run("transcript first", func(t *testing.T) {
		led, file := idlessLedger(t)
		w := idlessWatcher(t, led)
		w.applyEmit(feedFile(NewTranscriptState(), idlessSubagentLine(t2), subagentFile))
		w.ingestSpoolText(idlessRouteLine(t, t1, "req_sub_1"))
		checkJoinedRow(t, led, file)
	})

	t.Run("a header-less route holds the row alone, InTranscript false", func(t *testing.T) {
		led, file := idlessLedger(t)
		w := idlessWatcher(t, led)
		w.ingestSpoolText(idlessRouteLine(t, t1, ""))
		w.applyEmit(feedFile(NewTranscriptState(), idlessSubagentLine(t2), subagentFile))
		if len(w.pendingCopies) != 0 {
			t.Errorf("pending copies = %d, want none: the copy folded into a header-less route is not kept", len(w.pendingCopies))
		}
		ids := ledgerIDs(t, file)
		if len(ids) != 1 {
			t.Fatalf("ledger rows = %v, want exactly the route's", ids)
		}
		if strings.HasPrefix(ids[0], "msg") {
			t.Errorf("row id = %q, want the route's hash row, not the copy's message id", ids[0])
		}
		row, err := led.Get(t.Context(), ids[0])
		if err != nil || row == nil {
			t.Fatalf("read the row of %s: %+v, %v", ids[0], row, err)
		}
		if row.InTranscript {
			t.Error("InTranscript = true, want false: the copy has nothing to file under")
		}
	})
}
