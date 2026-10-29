package shared

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

// TestSpoolLineRetainsEncodedBytes proves a decoded spool line keeps its encoded bytes,
// not the parse tree it was read from: the store holds every non-route line as an event
// payload, so a tree would ride along whole and cost several times the line's own bytes.
// The retained heap is the collected live bytes after the decode, against the collected
// baseline from before it. HeapAlloc, the measure the other memory scenarios use: it
// counts exactly the bytes the lines keep. Not parallel: no other heap reader runs in
// this process while it measures.
func TestSpoolLineRetainsEncodedBytes(t *testing.T) {
	const count = 2000
	limit := 2.5
	if testkit.RaceEnabled {
		// the detector's own allocations ride along, the way the store's memory scenarios
		// scale their bounds under -race
		limit *= 2
	}
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	lines, encoded := decodeHookLines(t, count)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	retained := after.HeapAlloc - before.HeapAlloc

	perLine := retained / count
	perEncoded := uint64(encoded / count)
	ceiling := uint64(float64(perEncoded) * limit)
	t.Logf("%d lines of %d encoded bytes retain %d B/line (ceiling %d B/line)", count, perEncoded, perLine, ceiling)
	if perLine >= ceiling {
		t.Errorf("%d lines of %d encoded bytes retain %d B/line, want under %.1fx the encoded bytes (%d B/line)",
			count, perEncoded, perLine, limit, ceiling)
	}
	runtime.KeepAlive(lines)
}

// decodeHookLines decodes count non-route spool lines and reports their encoded length.
func decodeHookLines(t *testing.T, count int) (lines []SpoolLine, encoded int) {
	t.Helper()
	for i := 0; i < count; i++ {
		var line SpoolLine
		if err := json.Unmarshal(subagentStopSpoolLine(i), &line); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		raw, err := line.MarshalJSON()
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		encoded += len(raw)
		lines = append(lines, line)
	}
	return lines, encoded
}

// subagentStopSpoolLine builds one SubagentStop hook line, the most common non-route line
// a spool holds: its background tasks ride in a key no field declares, nested objects and
// arrays the store parks in Extra, beside the declared tool fields and strings. About
// 3.2 KB, unique per i, near the spool's average line.
func subagentStopSpoolLine(i int) []byte {
	var tasks strings.Builder
	for n := range 2 {
		log := strings.Repeat(fmt.Sprintf("ok pkg%04d task %d module builds clean; ", i, n), 33)
		fmt.Fprintf(&tasks,
			`{"id":"task-%04d-%02d","kind":"build","status":"done","log":"%s","attempts":[1,2,3],`+
				`"meta":{"exit":0,"secs":42.5,"tags":["go","build"]}},`, i, n, log)
	}
	return fmt.Appendf(nil,
		`{"ts":"2026-10-14T08:00:%02d.%03dZ","event":"SubagentStop","session_id":"sess-%04d","agent_id":"w%02d",`+
			`"cwd":"/Users/raouf/work/huddle","transcript_path":"/Users/raouf/work/huddle/sess-%04d.jsonl",`+
			`"tool_name":"Bash","tool_use_id":"toolu_%04d",`+
			`"tool_input":{"command":"go build ./...","timeout":120000},`+
			`"background_tasks":{"tasks":[%s]},"pid":4242,"ppid":4241}`,
		i%60, i%1000, i, i%100, i, i, strings.TrimRight(tasks.String(), ","))
}
