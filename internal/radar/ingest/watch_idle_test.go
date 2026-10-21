package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// A transcript tail's idle compaction on a fake clock: quiet past idleCompact, the parser's
// windows are emptied once; a tail that grew keeps them.

func TestTailIdleCompaction(t *testing.T) {
	home := t.TempDir()
	env := domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
	}
	path := filepath.Join(env["CLAUDE_CONFIG_DIR"], "projects", "-w-app", "sess-a.jsonl")
	writeTranscript := func(t *testing.T, entries ...map[string]any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer file.Close()
		for _, entry := range entries {
			line, err := json.Marshal(entry)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			file.Write(line)
			file.Write([]byte{'\n'})
		}
	}
	// an unfinished stream: no stop_reason, so its window stays open
	unfinished := func(requestID string, ts int64) map[string]any {
		return map[string]any{
			"type": "assistant", "timestamp": iso(ts), "requestId": requestID,
			"message": map[string]any{
				"model": "claude-sonnet-5-5", "stop_reason": nil,
				"content": []any{map[string]any{"type": "text", "text": "half an answer"}},
			},
		}
	}

	writeTranscript(t, unfinished("req-0", t0), unfinished("req-1", t0+1), unfinished("req-2", t0+2))
	var clock atomic.Int64
	clock.Store(t0)
	st := store.CreateStore(time.Now)
	w := StartWatcher(t.Context(), WatcherOptions{
		Env:        env,
		Store:      st,
		SinceMs:    24 * 3_600_000,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        clock.Load,
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
	tail := func(t *testing.T) *fileTail {
		t.Helper()
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.transcriptTails.files[path]
	}
	poll := func(advanceMs int64) {
		t.Helper()
		clock.Add(advanceMs)
		w.PollOnce()
	}

	if got := tail(t); got == nil || got.parser.streamOutput.len() != 3 || got.compacted {
		t.Fatalf("after the first pass the tail = %+v with %d windows, want 3 windows un-compacted", tail(t), got.parser.streamOutput.len())
	}

	t.Run("a quiet tail under the idle window keeps its windows", func(t *testing.T) {
		poll(9 * 60_000) // 9 minutes quiet: under idleCompact
		got := tail(t)
		if got.compacted || got.parser.streamOutput.len() != 3 {
			t.Errorf("after 9 quiet minutes compacted=%v windows=%d, want untouched", got.compacted, got.parser.streamOutput.len())
		}
	})

	t.Run("a tail idle past the idle window is compacted once", func(t *testing.T) {
		poll(2 * 60_000) // 11 minutes quiet: past idleCompact
		got := tail(t)
		if !got.compacted || got.parser.streamOutput.len() != 0 {
			t.Errorf("after 11 quiet minutes compacted=%v windows=%d, want compacted once and empty", got.compacted, got.parser.streamOutput.len())
		}
		poll(idleCompact.Milliseconds()) // still quiet: the once stands, nothing re-compacts
		got = tail(t)
		if !got.compacted || got.parser.streamOutput.len() != 0 {
			t.Errorf("after a further idle poll compacted=%v windows=%d, want the one compaction to stand", got.compacted, got.parser.streamOutput.len())
		}
	})

	t.Run("a tail that grew keeps its windows", func(t *testing.T) {
		writeTranscript(t, unfinished("req-3", t0+3))
		poll(1_000) // the growth lands while the clock is far past the old idle mark
		got := tail(t)
		if got.compacted || got.parser.streamOutput.len() != 1 {
			t.Errorf("after a growth compacted=%v windows=%d, want re-armed with the new stream", got.compacted, got.parser.streamOutput.len())
		}
		poll(idleCompact.Milliseconds() + 60_000) // quiet again past the window
		got = tail(t)
		if !got.compacted || got.parser.streamOutput.len() != 0 {
			t.Errorf("after the grown tail went idle compacted=%v windows=%d, want compacted", got.compacted, got.parser.streamOutput.len())
		}
	})
}
