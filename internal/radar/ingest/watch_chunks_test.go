package ingest_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// A pass reads a file chunk by chunk and takes the next chunk at once: the fallback scan runs
// every 30 s, so a backlog of n chunks left one chunk to a pass would take n scans to drain.
// The files here hold more than two chunks, so the pass the folder arming asks for still leaves
// a chunk behind.
func TestWatcherDrainsAFileBiggerThanOneChunk(t *testing.T) {
	const padBytes = 8_000
	const lines = 1_200 // about 9.6 MiB, past two TailChunkBytes
	pad := strings.Repeat("x", padBytes)
	if int64(lines*padBytes) <= 2*ingest.TailChunkBytes {
		t.Fatalf("the file is %d bytes, want more than two %d byte chunks", lines*padBytes, ingest.TailChunkBytes)
	}

	t.Run("a spool file in the first pass", func(t *testing.T) {
		e := makeEnv(t)
		var entries []any
		for i := 0; i < lines; i++ {
			entries = append(entries, map[string]any{
				"ts": iso(watchT0 + int64(i)), "event": "UserPromptSubmit",
				"session_id": "s1", "prompt": pad,
			})
		}
		writeText(t, spoolFilePath(t, e.env), jsonl(t, entries...))
		st := newStore()
		manualWatcher(t, e.env, st)
		if got := len(eventKinds(t, st, lines+1)); got != lines {
			t.Errorf("events after the first pass = %d, want %d", got, lines)
		}
	})

	t.Run("a transcript in the first pass", func(t *testing.T) {
		e := makeEnv(t)
		var entries []any
		for i := 0; i < lines; i++ {
			entry := assistantLine(watchT0+int64(i), fmt.Sprintf("req-%d", i))
			entry["pad"] = pad
			entries = append(entries, entry)
		}
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t, entries...))
		st := newStore()
		manualWatcher(t, e.env, st)
		if got := len(requestIDs(t, st, store.RequestFilter{Limit: ptr(int64(lines + 1))})); got != lines {
			t.Errorf("requests after the first pass = %d, want %d", got, lines)
		}
	})
}
