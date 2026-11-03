package report

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pricing"
)

// DefaultProvider is the provider a preview picks when it is enabled.
const DefaultProvider = "zai"

// NoProviderNote is the one line a preview carries when no provider is enabled.
const NoProviderNote = "No provider is enabled, so there is nothing to run Insights on. Set one up first, like /zai:setup."

// bytesPerToken is how many bytes of the payload's JSON one token covers. Keys and numbers
// split into short tokens, so the figure runs low and the estimate errs high.
const bytesPerToken = 3

// ProviderChoice is one enabled provider and the models a run may use on it; the first model is
// its default.
type ProviderChoice struct {
	Provider string   `json:"provider"`
	Name     string   `json:"name"`
	Models   []string `json:"models"`
}

// LabelMap is the local map the preview shows: each label the payload carries and the repo path
// or session id it stands for. The payload itself never holds the right-hand side.
type LabelMap struct {
	Repos    []LabelEntry `json:"repos"`
	Sessions []LabelEntry `json:"sessions"`
}

// Preview is what a Run would send, built without a model call: the provider and model, the
// window, the counts, the estimated input and its price, and the label map.
type Preview struct {
	Provider string           `json:"provider,omitempty"`
	Model    string           `json:"model,omitempty"`
	Choices  []ProviderChoice `json:"choices"`
	Note     string           `json:"note,omitempty"`
	Window   WindowView       `json:"window"`
	Sessions int              `json:"sessions"`
	Requests int              `json:"requests"`
	// InputTokens estimates the payload's tokens, and InputCostUSD prices them at the model's
	// input rate. Output is not estimated, so the figure is the input's cost alone; it is nil
	// when no price covers the model.
	InputTokens  int64    `json:"inputTokens"`
	InputCostUSD *float64 `json:"inputCostUsd,omitempty"`
	Labels       LabelMap `json:"labels"`
}

// UnknownChoiceError says a preview asked for a provider or model that is not enabled.
type UnknownChoiceError struct{ Provider, Model string }

func (e *UnknownChoiceError) Error() string {
	if e.Model == "" {
		return fmt.Sprintf("provider %q is not enabled", e.Provider)
	}
	return fmt.Sprintf("model %q is not one of the enabled models", e.Model)
}

// NoProviderPreview is the preview of a machine with no enabled provider.
func NoProviderPreview() Preview {
	return Preview{Choices: []ProviderChoice{}, Note: NoProviderNote, Labels: emptyLabelMap()}
}

// NewPreview describes the payload a Run on the chosen provider and model would send. An empty
// provider takes DefaultProvider when it is enabled, else the first choice; an empty model takes
// the provider's first. now dates the price.
func NewPreview(s Structure, labels *Labels, choices []ProviderChoice, provider, model string, now time.Time) (Preview, error) {
	picked, model, err := choose(choices, provider, model)
	if err != nil {
		return Preview{}, err
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return Preview{}, fmt.Errorf("size the payload: %w", err)
	}
	tokens := int64((len(payload) + bytesPerToken - 1) / bytesPerToken)
	return Preview{
		Provider:     picked.Provider,
		Model:        model,
		Choices:      choices,
		Window:       s.Window,
		Sessions:     s.Totals.Sessions,
		Requests:     s.Totals.Requests,
		InputTokens:  tokens,
		InputCostUSD: inputCost(model, tokens, now),
		Labels:       LabelMap{Repos: labels.Repos(), Sessions: labels.Sessions()},
	}, nil
}

func choose(choices []ProviderChoice, provider, model string) (ProviderChoice, string, error) {
	if len(choices) == 0 {
		return ProviderChoice{}, "", &UnknownChoiceError{Provider: provider, Model: model}
	}
	if provider == "" {
		provider = choices[0].Provider
		for _, c := range choices {
			if c.Provider == DefaultProvider {
				provider = DefaultProvider
			}
		}
	}
	for _, c := range choices {
		if c.Provider != provider {
			continue
		}
		if model == "" {
			return c, c.Models[0], nil
		}
		for _, m := range c.Models {
			if m == model {
				return c, m, nil
			}
		}
		return ProviderChoice{}, "", &UnknownChoiceError{Provider: provider, Model: model}
	}
	return ProviderChoice{}, "", &UnknownChoiceError{Provider: provider}
}

func inputCost(model string, tokens int64, now time.Time) *float64 {
	priced := pricing.PriceRequestOf(pricing.PriceRequest{
		Model:  model,
		Tokens: pricing.TokenUse{Input: float64(tokens)},
		Ts:     now.UnixMilli(),
	})
	if priced == nil {
		return nil
	}
	return &priced.USD
}

func emptyLabelMap() LabelMap { return LabelMap{Repos: []LabelEntry{}, Sessions: []LabelEntry{}} }
