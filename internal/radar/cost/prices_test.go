package cost

import (
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

var millionInput = shared.Tokens{Input: 1_000_000}

const zai = "https://api.z.ai"

// priced is the suite's base request — a live record's pricing fields, its stop reason
// null — which each case reshapes through shape.
func priced(shape func(*PricedRequest)) PricedRequest {
	r := PricedRequest{
		Model:          "claude-sonnet-5-5",
		Upstream:       "https://api.anthropic.com",
		Ts:             1_000,
		Tokens:         shared.Tokens{Input: 10, Output: 5},
		StopReasonNull: true,
	}
	shape(&r)
	return r
}

// ended marks a request finished with a stop reason.
func ended(r *PricedRequest) {
	r.StopReasonNull = false
	reason := "end_turn"
	r.StopReason = &reason
}

func TestPrices(t *testing.T) {
	t.Run("normalizes ids and finds rows case-insensitively", func(t *testing.T) {
		if got := NormalizeModel("zai/GLM-5.3[1m]"); got != "glm-5.3" {
			t.Errorf(`NormalizeModel("zai/GLM-5.3[1m]"): got %q want "glm-5.3"`, got)
		}
		if got := NormalizeModel("minimax-m3"); got != "minimax-m3" {
			t.Errorf(`NormalizeModel("minimax-m3"): got %q want "minimax-m3"`, got)
		}
	})

	t.Run("prices a claude request with its 1 h writes, fast mode and US geo", func(t *testing.T) {
		// 1 M in, 1 M out, 1 M cache read, 1 M 5 m + 1 M 1 h writes: fast rates 8/40, cache 0.4/10/16, then ×1.1
		cost := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "claude-opus-5-5"
			r.Ts = ms("2026-10-08T12:00:00Z")
			r.Tokens = shared.Tokens{Input: 1e6, Output: 1e6, CacheRead: 1e6, CacheWrite: 2e6}
			writes1h := int64(1e6)
			r.CacheWrite1h = &writes1h
			r.Speed = str("fast")
			r.Geo = str("us")
		}))
		if cost == nil {
			t.Fatal("claude-opus-5-5: no price")
		}
		closeTo(t, "claude fast us", float64(cost.USD), 81.84)
		want := []string{"Anthropic list", "fast mode", "US geo ×1.1", "1 h cache writes"}
		if !slices.Equal(cost.Detail, want) {
			t.Errorf("detail: got %v want %v", cost.Detail, want)
		}
	})

	t.Run("prices a zai request by its upstream host and every token kind at its own rate", func(t *testing.T) {
		// stopReason set: an in-flight record (no stop reason, no output) is never priced
		cost := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "glm-5.3"
			r.Upstream = zai
			r.Tokens = millionInput
			ended(r)
		}))
		if cost == nil {
			t.Fatal("glm-5.3: no price")
		}
		closeTo(t, "glm-5.3", float64(cost.USD), 1.4)
		if !slices.Equal(cost.Detail, []string{"Z.ai list"}) {
			t.Errorf("detail: got %v want [Z.ai list]", cost.Detail)
		}

		kinds := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "glm-5.3"
			r.Upstream = zai
			r.Tokens = shared.Tokens{Output: 1e6, CacheRead: 1e6, CacheWrite: 1e6}
		}))
		if kinds == nil {
			t.Fatal("glm-5.3 token kinds: no price")
		}
		closeTo(t, "glm-5.3 token kinds", float64(kinds.USD), 4.66)

		writes := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "kimi-k3"
			r.Upstream = "https://api.moonshot.ai"
			r.Tokens = shared.Tokens{CacheWrite: 1e6}
			ended(r)
		}))
		if writes == nil {
			t.Fatal("kimi-k3 writes: no price")
		}
		closeTo(t, "kimi-k3 writes", float64(writes.USD), 3)
	})

	t.Run("prices a deepseek request off-peak at half", func(t *testing.T) {
		// Saturday 12:00 UTC: outside both weekday peak windows, so the 1.32 input rate bills at half
		cost := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "deepseek-v4-pro"
			r.Upstream = "https://api.deepseek.com"
			r.Ts = ms("2026-10-10T12:00:00Z")
			r.Tokens = millionInput
			ended(r)
		}))
		if cost == nil {
			t.Fatal("deepseek-v4-pro: no price")
		}
		closeTo(t, "deepseek off-peak", float64(cost.USD), 0.66)
		want := []string{"DeepSeek list", "off-peak ×0.5"}
		if !slices.Equal(cost.Detail, want) {
			t.Errorf("detail: got %v want %v", cost.Detail, want)
		}
	})

	t.Run("leaves what nothing prices at null", func(t *testing.T) {
		if got := RequestCost(priced(func(r *PricedRequest) { r.Model = "not-a-model" })); got != nil {
			t.Errorf("not-a-model: got %v want nil", got)
		}
		if got := RequestCost(priced(func(r *PricedRequest) { r.Model = "claude-x" })); got != nil {
			t.Errorf("claude-x: got %v want nil", got)
		}
	})

	t.Run("never prices a request still streaming: no stop reason and no output yet", func(t *testing.T) {
		if got := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "glm-5.3"
			r.Upstream = zai
			r.Tokens = millionInput
		})); got != nil {
			t.Errorf("still streaming: got %v want nil", got)
		}
		// the same numbers once the request has finished do price
		finished := RequestCost(priced(func(r *PricedRequest) {
			r.Model = "glm-5.3"
			r.Upstream = zai
			r.Tokens = millionInput
			ended(r)
		}))
		if finished == nil {
			t.Fatal("finished glm-5.3: no price")
		}
		closeTo(t, "finished glm-5.3", float64(finished.USD), 1.4)
	})

	t.Run("sums costs, treating null as nothing priced", func(t *testing.T) {
		if got := AddCost(nil, nil); got != nil {
			t.Errorf("addCost(null, null): got %v want nil", *got)
		}
		one, two := shared.USD(1), shared.USD(2)
		if got := AddCost(nil, &one); got == nil || *got != 1 {
			t.Errorf("addCost(null, 1): got %v want 1", got)
		}
		if got := AddCost(&two, nil); got == nil || *got != 2 {
			t.Errorf("addCost(2, null): got %v want 2", got)
		}
		if got := AddCost(&one, &two); got == nil || *got != 3 {
			t.Errorf("addCost(1, 2): got %v want 3", got)
		}
		if got := CostOfAll(nil); got != nil {
			t.Errorf("costOfAll([]): got %v want nil", *got)
		}
		total := CostOfAll([]PricedRequest{
			{Model: "glm-5.3", Upstream: zai, Tokens: millionInput},
			{Model: "claude-x", Tokens: millionInput},
		})
		if total == nil {
			t.Fatal("costOfAll([glm, claude-x]): no price")
		}
		closeTo(t, "costOfAll mixed", float64(*total), 1.4)
	})

	t.Run("splits a request list's cost by family, each family null until something of it prices", func(t *testing.T) {
		families := map[string]CostFamily{
			"zai/glm-5.3[1m]": FamilyGLM,
			"claude-opus-5-5": FamilyClaude,
			"Opus-5.5":        FamilyClaude, // a sonnet/haiku/opus id reads as Claude's
			"kimi-k3":         FamilyOther,
			"not-a-model":     FamilyOther,
		}
		for model, want := range families {
			if got := CostFamilyOf(model); got != want {
				t.Errorf("costFamilyOf(%q): got %q want %q", model, got, want)
			}
		}

		requests := []PricedRequest{
			{Model: "claude-opus-5-5", Upstream: "https://api.anthropic.com", Tokens: millionInput},
			{Model: "glm-5.3", Upstream: zai, Tokens: millionInput},
			{Model: "kimi-k3", Upstream: "https://api.moonshot.ai", Tokens: millionInput},
			{Model: "not-a-model", Tokens: millionInput}, // unpriced: no family, no sum
		}
		split := CostByFamily(requests)
		if split.Claude == nil || *split.Claude <= 0 {
			t.Errorf("split.claude: got %v want > 0", split.Claude)
		}
		if split.GLM == nil {
			t.Fatal("split.glm: no price")
		}
		closeTo(t, "split.glm", float64(*split.GLM), 1.4)
		if split.Other == nil || *split.Other <= 0 {
			t.Errorf("split.other: got %v want > 0", split.Other)
		}
		// the parts and costOfAll price the very same requests, so they always agree
		sum := AddCost(AddCost(split.Claude, split.GLM), split.Other)
		if sum == nil {
			t.Fatal("the split's sum: no price")
		}
		total := CostOfAll(requests[:3])
		if total == nil {
			t.Fatal("costOfAll([opus, glm, kimi]): no price")
		}
		closeTo(t, "split sum", float64(*sum), float64(*total))

		for name, split := range map[string]CostSplit{
			"unpriced": CostByFamily([]PricedRequest{{Model: "not-a-model", Tokens: millionInput}}),
			"empty":    CostByFamily(nil),
		} {
			if split.Claude != nil || split.GLM != nil || split.Other != nil {
				t.Errorf("costByFamily(%s): got %+v want all null", name, split)
			}
		}
	})

	t.Run("prices a stored row through its model id alone, with no upstream to read", func(t *testing.T) {
		cost := RequestCost(PricedRequest{Model: "claude-opus-5-5", Tokens: millionInput})
		if cost == nil {
			t.Fatal("claude-opus-5-5 by its id alone: no price")
		}
		// the id is unique across offerings, so it prices at the base rate with no condition
		closeTo(t, "claude by id alone", float64(cost.USD), 4)
		if !slices.Equal(cost.Detail, []string{"Anthropic list"}) {
			t.Errorf("detail: got %v want [Anthropic list]", cost.Detail)
		}
	})

	t.Run("names the host of the offering that serves a model", func(t *testing.T) {
		for model, want := range map[string]string{
			"glm-5.3":         "https://api.z.ai",
			"kimi-k3":         "https://api.moonshot.ai",
			"MiniMax-M3":      "https://api.minimax.io",
			"zai/glm-5.3[1m]": "https://api.z.ai",
		} {
			if got, ok := OfferingHostOf(model); !ok || got != want {
				t.Errorf("offeringHostOf(%q): got (%q, %v) want (%q, true)", model, got, ok, want)
			}
		}
		for _, model := range []string{"", "claude-opus-5", "not-a-model"} {
			if got, ok := OfferingHostOf(model); ok {
				t.Errorf("offeringHostOf(%q): got %q want none", model, got)
			}
		}
	})

	t.Run("maps models to plugins and to their flash siblings", func(t *testing.T) {
		plugins := map[string]string{
			"glm-5.3":        "zai",
			"kimi-k3":        "kimi",
			"deepseek-flash": "deepseek",
			"MiniMax-M3":     "minimax",
			"qwen3.8-max":    "qwen",
		}
		for model, want := range plugins {
			if got, ok := PluginOf(model); !ok || got != want {
				t.Errorf("pluginOf(%q): got %q want %q", model, got, want)
			}
		}
		if got, ok := PluginOf("claude-opus-5"); ok {
			t.Errorf(`pluginOf("claude-opus-5"): got %q want null`, got)
		}
		if got, ok := FlashOf("glm-5.3"); !ok || got != "glm-5.3-flash" {
			t.Errorf(`flashOf("glm-5.3"): got %q want "glm-5.3-flash"`, got)
		}
		if got, ok := FlashOf("claude-opus-5"); ok {
			t.Errorf(`flashOf("claude-opus-5"): got %q want null`, got)
		}
		for model, want := range map[string]bool{
			"glm-5.3-flash":  true,
			"claude-haiku-5": true,
			"glm-5.3":        false,
		} {
			if got := IsSmallModel(model); got != want {
				t.Errorf("isSmallModel(%q): got %v want %v", model, got, want)
			}
		}
	})
}

// TestNormalizeModelTrimsJavaScriptWhitespace: model.trim() is JavaScript's trim, which drops
// the BOM and keeps NEL.
func TestNormalizeModelTrimsJavaScriptWhitespace(t *testing.T) {
	cases := []struct{ model, want string }{
		{"\ufeffzai/glm-5.3[1m]\u00a0", "glm-5.3"},
		{"\u0085glm-5.3", "\u0085glm-5.3"}, // NEL is not JavaScript whitespace
	}
	for _, c := range cases {
		if got := NormalizeModel(c.model); got != c.want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}
