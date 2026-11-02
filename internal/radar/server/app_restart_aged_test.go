package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// agedCall is a call made three days before the restart, its route line in the spool and its
// transcript copy under the claude config.
type agedCall struct {
	at         time.Time
	transcript string
}

// seedAgedCall writes the call's spool line and transcript, the transcript's mtime at mtime.
func seedAgedCall(t *testing.T, world testEnv, at time.Time, requestID string, routeUsage map[string]any) agedCall {
	t.Helper()
	route := w1RouteLine(t, at, requestID, "", "claude-sonnet-5-5", "anthropic", routeUsage, nil)
	writeText(t, filepath.Join(world.state, "spool", at.UTC().Format("2006-01-02")+".jsonl"), route+"\n")
	copyLine, err := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": isoOf(at.Add(40 * time.Millisecond).UnixMilli()),
		"requestId": requestID, "sessionId": "s1",
		"message": map[string]any{
			"role": "assistant", "model": "claude-sonnet-5-5", "stop_reason": "end_turn",
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 30},
			"content": []any{map[string]any{"type": "text", "text": "done"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal the transcript copy: %v", err)
	}
	path := writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1.jsonl"), string(copyLine)+"\n")
	return agedCall{at: at, transcript: path}
}

// historyRequestRows lists the id and total tokens of every request row the history file holds.
func historyRequestRows(t *testing.T, env domain.EnvLookup) map[string]int64 {
	t.Helper()
	db, err := history.OpenDB(paths.HistoryPath(t.Context(), env))
	if err != nil {
		t.Fatalf("open the history file: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.All("SELECT id, input + output + cache_read + cache_write AS total FROM requests")
	if err != nil {
		t.Fatalf("read the requests: %v", err)
	}
	held := make(map[string]int64, len(rows))
	for _, row := range rows {
		id, _ := row["id"].(string)
		total, _ := row["total"].(int64)
		held[id] = total
	}
	return held
}

// A call made three days ago, paired and kept by an earlier run, stays one row with its
// usage when the hub restarts: the spool lines of the last seven days are read again, and a
// transcript modified inside the since window is read again too.
func TestARestartKeepsAnOldPairedCallAsOneRow(t *testing.T) {
	cases := []struct {
		name         string
		transcriptRe bool
		routeUsage   map[string]any
	}{
		{"its transcript read again", true, map[string]any{"input_tokens": 120, "output_tokens": 30}},
		{"its transcript left alone", false, map[string]any{"input_tokens": 120, "output_tokens": 30}},
		{"a route line with no usage, its transcript read again", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := makeEnv(t)
			calledAt := time.Now().Add(-72 * time.Hour)
			call := seedAgedCall(t, world, calledAt, "req_aged_1", tc.routeUsage)
			mtime := calledAt.Add(time.Minute)
			if err := os.Chtimes(call.transcript, mtime, mtime); err != nil {
				t.Fatalf("set the transcript time: %v", err)
			}

			firstDay := func() time.Time { return calledAt.Add(2 * time.Minute) }
			first := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs, Now: firstDay})
			waitFor(t, "the first run's pass", 10*time.Second, func() bool { return !first.Watcher.CatchingUp() })
			first.Stop()
			before := historyRequestRows(t, world.env)
			if len(before) != 1 {
				t.Fatalf("the first run left %d request rows, want 1: %v", len(before), before)
			}

			if tc.transcriptRe {
				now := time.Now()
				if err := os.Chtimes(call.transcript, now, now); err != nil {
					t.Fatalf("touch the transcript: %v", err)
				}
			}
			second := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
			waitFor(t, "the second run's pass", 10*time.Second, func() bool { return !second.Watcher.CatchingUp() })
			second.Stop()
			after := historyRequestRows(t, world.env)
			if fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("the request rows after the restart: got %v, want the %v the first run kept", after, before)
			}
		})
	}
}
