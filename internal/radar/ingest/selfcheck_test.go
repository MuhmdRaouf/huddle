package ingest_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
)

// callLine is one assistant transcript line: a content block of the call message id names,
// carrying that call's usage the way Claude Code repeats it on every block.
func callLine(ts time.Time, messageID, model string, input, output, cacheRead, cacheWrite int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"assistant","sessionId":"s1","message":{"id":%q,"model":%q,`+
		`"content":[{"type":"text","text":"part"}],"usage":{"input_tokens":%d,"output_tokens":%d,`+
		`"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`+"\n",
		ts.UTC().Format(time.RFC3339Nano), messageID, model, input, output, cacheRead, cacheWrite)
}

func TestTranscriptSumsCountEachMessageIdOnce(t *testing.T) {
	world := makeEnv(t)
	projects := filepath.Join(world.config, "projects")
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	at := day.Add(9 * time.Hour)

	// one API call written as three assistant lines, one per content block
	main := ""
	for range 3 {
		main += callLine(at, "msg_a", "claude-opus-5-5", 100, 20, 1000, 50)
	}
	// a second call, and lines that are not today's model calls: a synthetic message, yesterday
	main += callLine(at.Add(time.Minute), "msg_b", "glm-5.3", 10, 2, 0, 0)
	main += callLine(at, "msg_s", ingest.SyntheticModel, 9, 9, 9, 9)
	main += callLine(day.Add(-time.Minute), "msg_y", "claude-opus-5-5", 7, 7, 7, 7)
	main += `{"timestamp":"` + at.Format(time.RFC3339) + `","type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	writeText(t, filepath.Join(projects, "-repo", "s1.jsonl"), main)
	// a fork copies msg_a into a subagent file: still one call
	writeText(t, filepath.Join(projects, "-repo", "s1", "subagents", "agent-x.jsonl"),
		callLine(at, "msg_a", "claude-opus-5-5", 100, 20, 1000, 50))

	sums, err := ingest.SumTranscripts([]string{projects, filepath.Join(world.home, "absent")}, day)
	if err != nil {
		t.Fatalf("SumTranscripts: %v", err)
	}
	want := ingest.DaySums{Requests: 2, Input: 110, Output: 22, CacheRead: 1000, CacheWrite: 50}
	if sums != want {
		t.Fatalf("sums = %+v, want %+v", sums, want)
	}
}
