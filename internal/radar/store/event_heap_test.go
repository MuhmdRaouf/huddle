package store

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

// The timeline holds a hook line by its bytes: the API serves the payload from them, so the
// parsed copy of the line's text (a 3 KB prompt a hook wrote) would be a second copy of the
// largest part of the line. The retained heap is the collected live bytes after the lines
// landed, against the collected baseline from before them. Not parallel: no other heap
// reader may run in this process while it measures.
func TestTheTimelineHoldsAHookLinesTextOnce(t *testing.T) {
	const count = 500
	limit := 1.6
	if testkit.RaceEnabled {
		limit *= 2
	}
	st := newTestStore()
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	encoded := 0
	for i := range count {
		raw := fmt.Sprintf(`{"ts":"2026-10-14T08:00:00.%03dZ","event":"UserPromptSubmit","session_id":"s%03d","prompt":"%s"}`,
			i%1000, i, strings.Repeat(fmt.Sprintf("prompt %04d words; ", i), 160))
		var line shared.SpoolLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		encoded += len(raw)
		st.AddSpoolLine(line)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	perLine := (after.HeapAlloc - before.HeapAlloc) / count
	ceiling := uint64(float64(encoded/count) * limit)
	t.Logf("%d lines of %d bytes are held in %d B/line (ceiling %d B/line)", count, encoded/count, perLine, ceiling)
	if perLine >= ceiling {
		t.Errorf("%d lines of %d bytes are held in %d B/line, want under %.1fx the bytes (%d B/line)",
			count, encoded/count, perLine, limit, ceiling)
	}
	runtime.KeepAlive(st)
}
