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
// The watcher here takes small chunks and the files hold many of them, so the pass the folder
// arming asks for still leaves chunks behind unless it drains the file.
func TestWatcherDrainsAFileBiggerThanOneChunk(t *testing.T) {
	const chunkBytes = 4_096
	const padBytes = 400
	const lines = 100 // about 40 KiB, ten chunks
	pad := strings.Repeat("x", padBytes)
	if lines*padBytes <= 2*chunkBytes {
		t.Fatalf("the file is %d bytes, want more than two %d byte chunks", lines*padBytes, chunkBytes)
	}
	smallChunks := func(options *ingest.WatcherOptions) { options.ChunkBytes = chunkBytes }

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
		manualWatcher(t, e.env, st, smallChunks)
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
		manualWatcher(t, e.env, st, smallChunks)
		if got := len(requestIDs(t, st, store.RequestFilter{Limit: ptr(int64(lines + 1))})); got != lines {
			t.Errorf("requests after the first pass = %d, want %d", got, lines)
		}
	})
}
