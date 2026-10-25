package ingest_test

import (
	"fmt"
	"io/fs"
	"math"
	"os"
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

func TestTranscriptSumsKeepTheLastCopyOfEachCall(t *testing.T) {
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	at := day.Add(9 * time.Hour)
	// Claude Code writes the first block lines while the answer still streams, so their
	// output_tokens is partial; the last line carries the call's final usage
	streaming := callLine(at, "msg_a", "claude-opus-5-5", 100, 1, 1000, 50)
	middle := callLine(at.Add(400*time.Millisecond), "msg_a", "claude-opus-5-5", 100, 1, 1000, 50)
	final := callLine(at.Add(900*time.Millisecond), "msg_a", "claude-opus-5-5", 100, 512, 1000, 50)
	want := ingest.DaySums{Requests: 1, Input: 100, Output: 512, CacheRead: 1000, CacheWrite: 50}

	for name, text := range map[string]string{
		"in the order written": streaming + middle + final,
		"the final line first": final + streaming + middle,
	} {
		t.Run(name, func(t *testing.T) {
			projects := filepath.Join(makeEnv(t).config, "projects")
			writeText(t, filepath.Join(projects, "-repo", "s1.jsonl"), text)
			sums, err := ingest.SumTranscripts([]string{projects}, day)
			if err != nil {
				t.Fatalf("SumTranscripts: %v", err)
			}
			if sums != want {
				t.Fatalf("sums = %+v, want %+v", sums, want)
			}
		})
	}
}

func TestTranscriptSumsOfARealShapeDayDriftUnderOnePercent(t *testing.T) {
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	projects := filepath.Join(makeEnv(t).config, "projects")
	copyTree(t, filepath.Join("testdata", "selfcheck"), projects, day.Add(10*time.Hour))

	sums, err := ingest.SumTranscripts([]string{projects}, day)
	if err != nil {
		t.Fatalf("SumTranscripts: %v", err)
	}
	// the final line of each of the fixture's three calls
	want := ingest.DaySums{Requests: 3, Input: 13, Output: 1882, CacheRead: 125800, CacheWrite: 1870}
	if drift := worstDriftPct(sums, want); drift >= 1 {
		t.Fatalf("drift = %.2f%%, sums = %+v, want %+v", drift, sums, want)
	}
}

// worstDriftPct is the largest per-counter |got - want| / want, as a percent.
func worstDriftPct(got, want ingest.DaySums) float64 {
	pairs := [][2]int64{
		{got.Requests, want.Requests},
		{got.Input, want.Input},
		{got.Output, want.Output},
		{got.CacheRead, want.CacheRead},
		{got.CacheWrite, want.CacheWrite},
	}
	worst := 0.0
	for _, pair := range pairs {
		worst = max(worst, math.Abs(float64(pair[0]-pair[1]))/float64(pair[1])*100)
	}
	return worst
}

// copyTree copies the fixture tree under src to dst and stamps every file with modTime, so the
// day's mtime filter reads it the same whenever the test runs.
func copyTree(t *testing.T, src, dst string, modTime time.Time) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := writeText(t, filepath.Join(dst, rel), string(data))
		return os.Chtimes(target, modTime, modTime)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}
