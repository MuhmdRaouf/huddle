package report

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var glmAndKimi = []ProviderChoice{
	{Provider: "kimi", Name: "Moonshot Kimi", Models: []string{"kimi-k3", "kimi-k3-flash"}},
	{Provider: "zai", Name: "Z.ai GLM", Models: []string{"glm-5.3", "glm-5.3-flash"}},
}

func preview(t *testing.T, choices []ProviderChoice, provider, model string) Preview {
	t.Helper()
	s, labels := build(t, fixture(), LastDays(t0, 7))
	p, err := NewPreview(s, labels, choices, provider, model, t0)
	if err != nil {
		t.Fatalf("NewPreview: %v", err)
	}
	return p
}

func TestPreviewDefaultsToGLMWhenItIsEnabled(t *testing.T) {
	p := preview(t, glmAndKimi, "", "")
	if p.Provider != "zai" || p.Model != "glm-5.3" {
		t.Fatalf("default = %s %s, want zai glm-5.3", p.Provider, p.Model)
	}
}

func TestPreviewDefaultsToTheFirstProviderWithoutGLM(t *testing.T) {
	p := preview(t, glmAndKimi[:1], "", "")
	if p.Provider != "kimi" || p.Model != "kimi-k3" {
		t.Fatalf("default = %s %s, want kimi kimi-k3", p.Provider, p.Model)
	}
}

func TestPreviewTakesAnotherEnabledProviderAndModel(t *testing.T) {
	p := preview(t, glmAndKimi, "kimi", "kimi-k3-flash")
	if p.Provider != "kimi" || p.Model != "kimi-k3-flash" {
		t.Fatalf("choice = %s %s, want kimi kimi-k3-flash", p.Provider, p.Model)
	}
	if len(p.Choices) != 2 {
		t.Fatalf("choices = %+v, want both enabled providers", p.Choices)
	}
}

func TestPreviewRefusesAProviderOrModelThatIsNotEnabled(t *testing.T) {
	s, labels := build(t, fixture(), LastDays(t0, 7))
	for _, ask := range [][2]string{{"deepseek", ""}, {"zai", "glm-9"}, {"", "glm-5.3-flash-x"}} {
		_, err := NewPreview(s, labels, glmAndKimi, ask[0], ask[1], t0)
		var unknown *UnknownChoiceError
		if !errors.As(err, &unknown) {
			t.Errorf("provider %q model %q: err = %v, want an UnknownChoiceError", ask[0], ask[1], err)
		}
	}
}

func TestPreviewNamesTheWindowAndTheCounts(t *testing.T) {
	p := preview(t, glmAndKimi, "", "")
	if p.Window.Days != 7 || p.Sessions != 3 || p.Requests != 5 {
		t.Fatalf("window %+v sessions %d requests %d, want 7 days, 3 sessions, 5 requests", p.Window, p.Sessions, p.Requests)
	}
}

func TestPreviewEstimatesTheInputFromThePayloadItSends(t *testing.T) {
	s, labels := build(t, fixture(), LastDays(t0, 7))
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPreview(s, labels, glmAndKimi, "", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64((len(payload) + bytesPerToken - 1) / bytesPerToken); p.InputTokens != want {
		t.Fatalf("input tokens = %d, want %d for %d payload bytes", p.InputTokens, want, len(payload))
	}
	if p.InputCostUSD == nil || *p.InputCostUSD <= 0 {
		t.Fatalf("cost = %v, want a price for glm-5.3", p.InputCostUSD)
	}
}

func TestPreviewLeavesTheCostOutForAModelNoPriceCovers(t *testing.T) {
	choices := []ProviderChoice{{Provider: "zai", Name: "Z.ai GLM", Models: []string{"no-such-model"}}}
	if p := preview(t, choices, "", ""); p.InputCostUSD != nil {
		t.Fatalf("cost = %v, want none for an unpriced model", *p.InputCostUSD)
	}
}

func TestPreviewLabelMapShowsLabelsAndNeverSessionText(t *testing.T) {
	p := preview(t, glmAndKimi, "", "")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "prompt") || strings.Contains(string(raw), "response") {
		t.Fatalf("the preview carries session text: %s", raw)
	}
	if len(p.Labels.Repos) != 2 || p.Labels.Repos[0].Label != "repo-1" || !strings.Contains(p.Labels.Repos[0].Value, "proj-a") {
		t.Fatalf("repo labels = %+v, want repo-1 for proj-a first", p.Labels.Repos)
	}
	if len(p.Labels.Sessions) != 3 || p.Labels.Sessions[0] != (LabelEntry{Label: "session-1", Value: "s1"}) {
		t.Fatalf("session labels = %+v, want session-1 for s1 first", p.Labels.Sessions)
	}
}

func TestNoProviderPreviewSaysSoInOneLine(t *testing.T) {
	p := NoProviderPreview()
	if p.Note == "" || strings.Contains(p.Note, "\n") {
		t.Fatalf("note = %q, want one line", p.Note)
	}
	if len(p.Choices) != 0 || p.Provider != "" || p.Model != "" {
		t.Fatalf("preview = %+v, want no provider and no model", p)
	}
}
