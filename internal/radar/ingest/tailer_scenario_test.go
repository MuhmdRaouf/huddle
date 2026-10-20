package ingest_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTailingATranscriptScenarios(t *testing.T) {
	bdd.Feature(t, "tailing a transcript", func(f *bdd.F) {
		f.Scenario("a line still being written waits for its newline", func(s *bdd.S) {
			var (
				path  string
				state ingest.TailState
				got   ingest.TailResult
			)
			s.Given("a file holding one finished line and one still being written", func(t *testing.T) {
				path = writeText(t, filepath.Join(makeEnv(s.T).home, "transcript.jsonl"), `{"a":1}`+"\n"+`{"b":`)
				state = ingest.OpenTail(path)
			})
			s.When("the file is tailed", func(t *testing.T) {
				got = ingest.TailFile(&state, ingest.TailChunkBytes)
			})
			s.Then("only the finished line comes back and the rest waits as the partial", func(t *testing.T) {
				if !slices.Equal(got.Lines, []string{`{"a":1}`}) {
					t.Errorf("lines = %q, want [{\"a\":1}]", got.Lines)
				}
				if state.Partial != `{"b":` {
					t.Errorf("partial = %q, want {\"b\":", state.Partial)
				}
			})
			s.When("the rest of the line arrives and the file is tailed again", func(t *testing.T) {
				appendText(t, path, "2}\n")
				got = ingest.TailFile(&state, ingest.TailChunkBytes)
			})
			s.Then("the waiting line is delivered whole", func(t *testing.T) {
				if !slices.Equal(got.Lines, []string{`{"b":2}`}) {
					t.Errorf("lines = %q, want [{\"b\":2}]", got.Lines)
				}
			})
		})

		f.Scenario("a file that shrank is read again from its start", func(s *bdd.S) {
			var (
				path  string
				state ingest.TailState
				got   ingest.TailResult
			)
			s.Given("a transcript of two lines that was already tailed", func(t *testing.T) {
				path = writeText(t, filepath.Join(makeEnv(s.T).home, "transcript.jsonl"), "first\nsecond\n")
				state = ingest.OpenTail(path)
				ingest.TailFile(&state, ingest.TailChunkBytes)
			})
			s.When("the file is replaced by one shorter line and tailed", func(t *testing.T) {
				writeText(t, path, "fresh\n")
				got = ingest.TailFile(&state, ingest.TailChunkBytes)
			})
			s.Then("the result says restarted and returns that one line", func(t *testing.T) {
				if !got.Restarted {
					t.Errorf("restarted = false, want true")
				}
				if !slices.Equal(got.Lines, []string{"fresh"}) {
					t.Errorf("lines = %q, want [fresh]", got.Lines)
				}
			})
		})
	})
}
