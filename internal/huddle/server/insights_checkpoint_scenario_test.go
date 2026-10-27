package server

// The context_checkpoint tip is one live session's fact and the huddle's: the registry's own
// rows say the session is deep in its window with nothing fresh committed. The owner's answer
// carries the tip, the session's own answer carries it, and another session's answer does not.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheContextCheckpointTipKeepsToItsOwnSession(t *testing.T) {
	bdd.Feature(t, "a session past 70% context with no commit and no handoff earns one context_checkpoint tip", func(f *bdd.F) {
		f.Scenario("a session reported at 72% whose newest Bash run is no commit", func(s *bdd.S) {
			var (
				fix   *subagentFixture
				owner insightsAnswer
				want  []tipView
			)
			s.Given("the registry holds a session at 72% with nothing committed and a session at 75% that just committed", func(t *testing.T) {
				fix = newSubagentFixture(s.T)
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				stamp := func(ago time.Duration) string {
					return fix.clock.Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z")
				}
				for _, rep := range []hub.Report{
					// s-check: past 70%, reported a minute ago, its only Bash run builds
					{SessionID: "s-check", LastEvent: "PostToolUse", LastEventAt: stamp(5 * time.Minute), Tool: "Bash", ToolTarget: "go build ./..."},
					{SessionID: "s-check", LastEvent: "UserPromptSubmit", LastEventAt: stamp(time.Minute), ContextPercent: 72},
					// s-other: past 70% too, but its commit ten minutes ago is fresh
					{SessionID: "s-other", LastEvent: "PostToolUse", LastEventAt: stamp(10 * time.Minute), Tool: "Bash", ToolTarget: "git commit -m wip"},
					{SessionID: "s-other", LastEvent: "UserPromptSubmit", LastEventAt: stamp(2 * time.Minute), ContextPercent: 75},
				} {
					if err := fix.reg.Report(context.Background(), rep); err != nil {
						t.Fatalf("Report(%s %s): %v", rep.SessionID, rep.LastEvent, err)
					}
				}
			})
			s.When("the owner reads the insights card", func(t *testing.T) {
				owner = fix.insights("")
			})
			s.Then("the answer carries s-check's tip and no other", func(t *testing.T) {
				want = []tipView{{
					ID:       "context_checkpoint:s-check",
					Rule:     "context_checkpoint",
					Session:  "s-check",
					Sentence: "Context is at 72% with nothing committed yet. A good point to finish this step and commit.",
					Count:    1,
				}}
				if !reflect.DeepEqual(owner.Tips, want) {
					t.Errorf("the owner's tips = %+v, want %+v", owner.Tips, want)
				}
			})
			s.And("s-check's own answer carries it too, and s-other's answer carries none", func(t *testing.T) {
				if mine := fix.insights("s-check"); !reflect.DeepEqual(mine.Tips, want) {
					t.Errorf("s-check's tips = %+v, want %+v", mine.Tips, want)
				}
				if other := fix.insights("s-other"); len(other.Tips) != 0 {
					t.Errorf("s-other's tips = %+v, want none", other.Tips)
				}
			})
		})
	})
}
