package radartab

// The model advice the owner's insights card reads: one line per row of radar's own advisor
// report, with the flash sibling and the two costs carried over.

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// adviceRun records one subagent request the way the advisor's own fixtures do: a small read
// only run, on the model and upstream the price table knows.
func adviceRun(id, agent, model, upstream string) shared.RequestRecord {
	return shared.RequestRecord{
		ID: id, SessionID: "s-adv", AgentID: agent, Model: model, Upstream: upstream,
		Ts:     1_000,
		Tokens: shared.Tokens{Input: 10_000, Output: 500},
	}
}

// adviceTool records the one read-only call a run made, all green.
func adviceTool(id, agent string) shared.ToolCallRecord {
	return shared.ToolCallRecord{
		ID: "tool-" + id, SessionID: "s-adv", AgentID: strPtr(agent), Name: "Read",
		StartedAt: 1_000, Ok: true,
	}
}

func TestModelAdvice(t *testing.T) {
	t.Run("a tab with no insights answers none", func(t *testing.T) {
		var dead *Tab
		if got := dead.ModelAdvice(); got != nil {
			t.Errorf("a nil tab's advice = %+v, want nil", got)
		}
		if got := (&Tab{}).ModelAdvice(); got != nil {
			t.Errorf("a tab without insights' advice = %+v, want nil", got)
		}
	})
	t.Run("the advisor's rows reach the owner, one advice per row, in the report's order", func(t *testing.T) {
		env := tempEnv(t)
		tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
		if !tab.Owns {
			t.Fatal("the mount did not take the lock")
		}
		// three flash-sized runs on glm-5.3, one on a model with no sibling, one main-agent
		// request that is no run, and one run on the flash model itself, which is small already
		for _, id := range []string{"r1", "r2", "r3"} {
			tab.Store.AddRequest(adviceRun(id, "w-"+id, "glm-5.3", "https://api.z.ai"), nil)
			tab.Store.AddToolCall(adviceTool(id, "w-"+id))
		}
		tab.Store.AddRequest(adviceRun("r4", "w-r4", "claude-opus-5", ""), nil)
		tab.Store.AddToolCall(adviceTool("r4", "w-r4"))
		tab.Store.AddRequest(adviceRun("r5", "main", "glm-5.3", "https://api.z.ai"), nil)
		tab.Store.AddRequest(adviceRun("r6", "w-r6", "glm-5.3-flash", "https://api.z.ai"), nil)

		got := tab.ModelAdvice()
		if len(got) != 2 {
			t.Fatalf("the advice = %+v, want 2 rows", got)
		}
		glm, opus := got[0], got[1]
		if glm.Model != "glm-5.3" || glm.Flash != "glm-5.3-flash" || glm.Runs != 3 {
			t.Errorf("glm row = %+v, want glm-5.3 flash glm-5.3-flash 3 runs", glm)
		}
		if glm.CostUsd == nil {
			t.Errorf("glm row: the cost is nil, want the three runs' own price")
		}
		if glm.FlashCostUsd == nil {
			t.Errorf("glm row: the flash cost is nil, want the sibling's price")
		}
		if *glm.CostUsd <= *glm.FlashCostUsd {
			t.Errorf("glm row: cost %.4f not above flash cost %.4f, want the main model the pricier one",
				*glm.CostUsd, *glm.FlashCostUsd)
		}
		if opus.Model != "claude-opus-5" || opus.Flash != "" || opus.Runs != 1 {
			t.Errorf("opus row = %+v, want claude-opus-5 with no sibling and 1 run", opus)
		}
		if opus.FlashCostUsd != nil {
			t.Errorf("opus row: flash cost %v, want nil with no sibling", *opus.FlashCostUsd)
		}
	})
	t.Run("an empty store answers no rows", func(t *testing.T) {
		env := tempEnv(t)
		tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
		if got := tab.ModelAdvice(); len(got) != 0 {
			t.Errorf("an empty store's advice = %+v, want none", got)
		}
	})
}
