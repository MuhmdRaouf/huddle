package ingest_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The ingest's memory behaviour as scenarios: what the watcher's heap lives through while
// radar catches up on a directory full of transcripts.

func TestIngestMemoryScenarios(t *testing.T) {
	bdd.Feature(t, "the hub stays light while radar catches up", func(f *bdd.F) {
		f.Scenario("fifty transcripts caught up at once grow the heap under a fixed bound", func(s *bdd.S) {
			var (
				e             testEnv
				st            *store.Store
				w             *ingest.Watcher // scenario-scoped: the watcher and its parser states must stay reachable while Then measures
				before, after runtime.MemStats
			)
			s.Given("50 transcript files of 200 requests with 4 KiB outputs, every 5th streamed over 2 writes", func(t *testing.T) {
				e = makeEnv(s.T) // the temp home must outlive the Given step, or the watcher watches an empty dir
				projects := filepath.Join(e.config, "projects", "-w-app")
				for file := range 50 {
					var document strings.Builder
					for entry := range 200 {
						text := strings.Repeat(fmt.Sprintf("f%02de%03d ", file, entry), 512) // 8 bytes a tile, 4 KiB a block
						requestID := fmt.Sprintf("req-f%02de%03d", file, entry)
						ts := watchT0 + int64(file)*1_000 + int64(entry)
						if entry%5 == 0 {
							// a stream mid-flight: two writes of one request, no stop_reason yet
							document.WriteString(jsonl(t, ingestAssistant(ts, requestID, text[:2048], false)))
							document.WriteString(jsonl(t, ingestAssistant(ts+1, requestID, text, false)))
							continue
						}
						document.WriteString(jsonl(t, ingestAssistant(ts, requestID, text, true)))
					}
					writeText(t, filepath.Join(projects, fmt.Sprintf("sess-%02d.jsonl", file)), document.String())
				}
			})
			s.When("the watcher catches up on all fifty", func(t *testing.T) {
				runtime.GC() // a clean baseline: the Given step's marshalling garbage must not count
				runtime.ReadMemStats(&before)
				st = newStore()
				w = ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
					Env:        e.env,
					Store:      st,
					SinceMs:    watchDay,
					IntervalMs: 3_600_000,
					RescanMs:   5_000,
					Now:        func() int64 { return watchT0 },
					Alive:      ownPidAlive,
				})
				t.Cleanup(w.Stop)
				// the catch-up parses 44 MB of jsonl; the race detector runs several times slower
				waitFor(t, "the catch-up", 60*time.Second, func() bool { return !w.CatchingUp() })
			})
			s.And("the catch-up really landed every request", func(t *testing.T) {
				if got := len(st.RequestsFor(store.RequestFilter{})); got != 10_000 {
					t.Fatalf("requests = %d, want 10,000 — the heap bound is empty otherwise", got)
				}
			})
			s.Then("the heap grew under 48 MiB", func(t *testing.T) {
				runtime.GC()
				runtime.ReadMemStats(&after)
				w.Stop() // measured; the watcher's states must stay live until here, not till the last use in When
				limit := uint64(48 << 20)
				if testkit.RaceEnabled {
					// the detector's own allocations ride along, so the bound scales and still pins
					limit *= 4
				}
				grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
				t.Logf("ingesting 50 transcripts grew the heap %d MiB of the %d MiB bound", grew>>20, limit>>20)
				if grew >= int64(limit) {
					t.Fatalf("ingesting 50 transcripts grew the heap %d MiB, want under %d MiB", grew>>20, limit>>20)
				}
			})
		})
	})
}

// ingestAssistant is one assistant entry carrying one text block; a false finished leaves the
// stop_reason null, an unfinished stream.
func ingestAssistant(ts int64, requestID, text string, finished bool) map[string]any {
	stopReason := any(nil)
	if finished {
		stopReason = "end_turn"
	}
	return map[string]any{
		"type": "assistant", "timestamp": iso(ts), "requestId": requestID,
		"message": map[string]any{
			"model": "claude-sonnet-5-5", "stop_reason": stopReason,
			"usage":   map[string]any{"input_tokens": 10, "output_tokens": 5},
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
}
