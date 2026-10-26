package convo

// Feature: the live footer knows the current turn — it counts the elapsed time from the last
// typed prompt's own timestamp while no turn_duration has followed it, and it stops counting
// once one has. The permission mode it shows is the last permission-mode line's.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheTurnReaderKnowsTheCurrentTurn(t *testing.T) {
	bdd.Feature(t, "The live footer knows the current turn", func(f *bdd.F) {
		f.Scenario("a turn still running counts from the prompt that started it", func(s *bdd.S) {
			var path string
			var facts TurnFacts
			s.Given("a transcript whose last prompt has a turn_duration behind an earlier prompt only", func(t *testing.T) {
				lines := []string{
					`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"user","content":"first prompt"}}`,
					`{"type":"system","subtype":"turn_duration","durationMs":99000,"isMeta":false,"timestamp":"2026-10-26T10:01:39.000Z"}`,
					`{"type":"permission-mode","permissionMode":"acceptEdits","sessionId":"s-1"}`,
					`{"type":"user","uuid":"u2","timestamp":"2026-10-26T10:05:00.000Z","message":{"role":"user","content":"second prompt"}}`,
					`{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"s-1"}`,
				}
				path = filepath.Join(s.T.TempDir(), "transcript.jsonl")
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the turn reader reads it", func(t *testing.T) {
				read, err := Turn(t.Context(), path)
				if err != nil {
					t.Fatalf("turn: %v", err)
				}
				facts = read
			})
			s.Then("the turn runs from the second prompt, ended by nothing, under the last mode", func(t *testing.T) {
				want := TurnFacts{StartedAt: "2026-10-26T10:05:00.000Z", PermissionMode: "bypassPermissions"}
				if facts != want {
					t.Errorf("facts = %+v, want %+v", facts, want)
				}
			})
		})

		f.Scenario("a turn_duration after the last prompt ends the turn", func(s *bdd.S) {
			var path string
			var facts TurnFacts
			s.Given("a transcript whose last prompt is followed by a turn_duration line", func(t *testing.T) {
				lines := []string{
					`{"type":"user","uuid":"u1","timestamp":"2026-10-26T10:00:00.000Z","message":{"role":"user","content":"the only prompt"}}`,
					`{"type":"assistant","uuid":"a1","timestamp":"2026-10-26T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`,
					`{"type":"system","subtype":"turn_duration","durationMs":99000,"isMeta":false,"timestamp":"2026-10-26T10:01:39.000Z"}`,
				}
				path = filepath.Join(s.T.TempDir(), "transcript.jsonl")
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the turn reader reads it", func(t *testing.T) {
				read, err := Turn(t.Context(), path)
				if err != nil {
					t.Fatalf("turn: %v", err)
				}
				facts = read
			})
			s.Then("the turn counts as ended, 99000 ms from its prompt", func(t *testing.T) {
				want := TurnFacts{StartedAt: "2026-10-26T10:00:00.000Z", Ended: true, DurationMS: 99000}
				if facts != want {
					t.Errorf("facts = %+v, want %+v", facts, want)
				}
			})
		})
	})
}
