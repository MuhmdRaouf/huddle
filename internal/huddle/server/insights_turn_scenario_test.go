package server

// The long_turn tip is one session's fact and the huddle's: it reaches the owner's insights
// answer and that session's own answer, and no other session's answer carries it.

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheLongTurnTipKeepsToItsOwnSession(t *testing.T) {
	bdd.Feature(t, "a turn past 45 minutes with edits and no commit earns one long_turn tip", func(f *bdd.F) {
		f.Scenario("a session prompted fifty minutes ago, edited a file and committed nothing", func(s *bdd.S) {
			var (
				fix   *subagentFixture
				owner insightsAnswer
				want  []tipView
			)
			s.Given("the registry holds a turn fifty minutes old with one edit, and a session whose turn is five minutes old", func(t *testing.T) {
				fix = newSubagentFixture(s.T)
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				stamp := func(ago time.Duration) string {
					return fix.clock.Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z")
				}
				for _, rep := range []hub.Report{
					{SessionID: "s-turn", LastEvent: "UserPromptSubmit", LastEventAt: stamp(50 * time.Minute)},
					{SessionID: "s-turn", LastEvent: "PostToolUse", LastEventAt: stamp(49 * time.Minute), Tool: "Edit", ToolTarget: "main.go"},
					{SessionID: "s-other", LastEvent: "UserPromptSubmit", LastEventAt: stamp(5 * time.Minute)},
				} {
					if err := fix.reg.Report(context.Background(), rep); err != nil {
						t.Fatalf("Report(%s %s): %v", rep.SessionID, rep.LastEvent, err)
					}
				}
			})
			s.When("the owner reads the insights card", func(t *testing.T) {
				owner = fix.insights("")
			})
			s.Then("the answer carries s-turn's tip and no other", func(t *testing.T) {
				want = []tipView{{
					ID:       fmt.Sprintf("long_turn:s-turn:%d", fix.clock.Add(-50*time.Minute).Unix()),
					Rule:     "long_turn",
					Session:  "s-turn",
					Sentence: "This turn has edited 1 file over 50 min with no commit. A good point to commit what works.",
					Count:    1,
				}}
				if !reflect.DeepEqual(owner.Tips, want) {
					t.Errorf("the owner's tips = %+v, want %+v", owner.Tips, want)
				}
			})
			s.And("s-turn's own answer carries it too, and s-other's answer carries none", func(t *testing.T) {
				if mine := fix.insights("s-turn"); !reflect.DeepEqual(mine.Tips, want) {
					t.Errorf("s-turn's tips = %+v, want %+v", mine.Tips, want)
				}
				if other := fix.insights("s-other"); len(other.Tips) != 0 {
					t.Errorf("s-other's tips = %+v, want none", other.Tips)
				}
			})
		})
	})
}
