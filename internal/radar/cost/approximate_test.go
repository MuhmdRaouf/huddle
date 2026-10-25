package cost

import (
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// A model id the catalog does not know prices at its family's rate and says so: the longest
// dash-bounded prefix some catalog id shares picks the family, its newest id the rate; an id
// no prefix reaches bills at its provider's main model.
func TestUnknownModelPricesAtItsFamilyRateAndIsApproximate(t *testing.T) {
	million := shared.Tokens{Input: 1_000_000, Output: 1_000_000}

	t.Run("a made-up claude-opus id bills at the newest opus rate, flagged approximate", func(t *testing.T) {
		cost := RequestCost(PricedRequest{Model: "claude-opus-9-9", Tokens: million})
		if cost == nil {
			t.Fatal("claude-opus-9-9: no price")
		}
		if !cost.Approximate {
			t.Error("claude-opus-9-9: Approximate: got false want true")
		}
		closeTo(t, "claude-opus-9-9", float64(cost.USD), 4+20)
		if !slices.Contains(cost.Detail, "≈ claude-opus-5-5 rate") {
			t.Errorf("detail: got %v want to contain %q", cost.Detail, "≈ claude-opus-5-5 rate")
		}
	})

	t.Run("an id with no family prefix bills at its provider's main model", func(t *testing.T) {
		cost := RequestCost(PricedRequest{Model: "qwen9-max", Tokens: million})
		if cost == nil {
			t.Fatal("qwen9-max: no price")
		}
		want := RequestCost(PricedRequest{Model: "qwen3.8-max", Tokens: million})
		if !cost.Approximate || want == nil || cost.USD != want.USD {
			t.Errorf("qwen9-max: got %+v want qwen3.8-max's %+v, approximate", cost, want)
		}
	})

	t.Run("the upstream's offering bounds the family", func(t *testing.T) {
		cost := RequestCost(PricedRequest{Model: "zai/brand-new", Upstream: zai, Tokens: million})
		want := RequestCost(PricedRequest{Model: "glm-5.3", Tokens: million})
		if cost == nil || !cost.Approximate || want == nil || cost.USD != want.USD {
			t.Errorf("brand-new on z.ai: got %+v want glm-5.3's %+v, approximate", cost, want)
		}
	})

	t.Run("a catalog id is exact, and an id no provider claims stays unpriced", func(t *testing.T) {
		if cost := RequestCost(PricedRequest{Model: "claude-opus-5-5", Tokens: million}); cost == nil || cost.Approximate {
			t.Errorf("claude-opus-5-5: got %+v want an exact price", cost)
		}
		if cost := RequestCost(PricedRequest{Model: "not-a-model", Tokens: million}); cost != nil {
			t.Errorf("not-a-model: got %+v want nil", cost)
		}
	})

	t.Run("the costs tree marks a node approximate when a request behind it is", func(t *testing.T) {
		rows := []LedgerRow{
			{ID: "a", Ts: 10, Model: "claude-opus-9-9", SessionID: "s", AgentID: "main", Tokens: million},
			{ID: "b", Ts: 11, Model: "claude-opus-5-5", SessionID: "s", AgentID: "main", Tokens: million},
		}
		tree := AttributionTree(rows, LedgerNames{}, 0)
		if len(tree) != 1 || !tree[0].Approximate {
			t.Fatalf("repo node: got %+v want one approximate node", tree)
		}
		models := tree[0].Children[0].Children[0].Children
		for _, model := range models {
			if want := model.Key == "claude-opus-9-9"; model.Approximate != want {
				t.Errorf("model %s: Approximate: got %v want %v", model.Key, model.Approximate, want)
			}
		}
	})
}
