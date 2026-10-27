package server

// The model_oversized tip is the owner's fact alone: radar's advisor rates a model's subagent
// runs flash-sized, the owner's insights answer carries the tip, and the session's own answer
// carries nothing about it.

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// flashSizedRun records one subagent request on a main model, small enough that radar's own
// advisor counts it as flash-sized work.
func (f *subagentFixture) flashSizedRun(session, agent string) {
	f.t.Helper()
	f.tab.Store.AddRequest(shared.RequestRecord{
		ID: "req-" + session + "-" + agent, SessionID: session, AgentID: agent,
		Model: "glm-5.3", Upstream: "https://api.z.ai",
		Ts:     f.clock.UnixMilli(),
		Tokens: shared.Tokens{Input: 10_000, Output: 500},
	}, nil)
}

func TestTheModelOversizedTipIsTheOwnersAlone(t *testing.T) {
	bdd.Feature(t, "a model whose subagent runs look flash-sized earns the owner one model_oversized tip", func(f *bdd.F) {
		f.Scenario("three flash-sized runs on glm-5.3", func(s *bdd.S) {
			var (
				fix   *subagentFixture
				owner insightsAnswer
			)
			s.Given("the mounted radar tab holds three flash-sized runs on glm-5.3", func(t *testing.T) {
				fix = newSubagentFixture(s.T)
				if err := insights.WriteSettings(insights.SettingsFile(fix.home),
					insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
					t.Fatal(err)
				}
				fix.flashSizedRun("s-model", "w-one")
				fix.flashSizedRun("s-model", "w-two")
				fix.flashSizedRun("s-model", "w-three")
			})
			s.When("the owner reads the insights card", func(t *testing.T) {
				owner = fix.insights("")
			})
			s.Then("the answer carries the model's tip, naming the model, its flash sibling and the saving", func(t *testing.T) {
				if len(owner.Tips) != 1 {
					t.Fatalf("the owner's tips = %+v, want the one model_oversized tip", owner.Tips)
				}
				tip := owner.Tips[0]
				if tip.ID != "model_oversized:glm-5.3:2026-10-25" || tip.Rule != "model_oversized" ||
					tip.Session != "" || tip.Count != 3 {
					t.Errorf("the tip = %+v, want model_oversized:glm-5.3:2026-10-25 for the whole huddle at 3 runs", tip)
				}
				if !strings.HasPrefix(tip.Sentence, "3 subagent runs on `glm-5.3` looked small enough for `glm-5.3-flash`; they would have cost $") ||
					!strings.HasSuffix(tip.Sentence, " less.") {
					t.Errorf("the sentence = %q, want the saving figure between its fixed ends", tip.Sentence)
				}
			})
			s.And("the session's own answer carries no tip about models", func(t *testing.T) {
				mine := fix.insights("s-model")
				for _, tip := range mine.Tips {
					if tip.Rule == "model_oversized" {
						t.Errorf("the session's tips = %+v, want none about models", mine.Tips)
						return
					}
				}
			})
		})
	})
}
