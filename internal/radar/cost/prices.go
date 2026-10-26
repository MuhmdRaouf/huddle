package cost

import (
	"regexp"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// PricedRequest is the parts of a request the price reads: a full record carries all of
// them, a ledger row the stored ones.
type PricedRequest struct {
	Model    string
	Ts       int64
	Tokens   shared.Tokens
	Upstream string
	// StopReason is the request's stop reason once it finished. A record whose stop reason
	// is null carries StopReasonNull instead: while it also has no output the request is
	// still streaming and its numbers are the opening estimate, which never prices. A
	// stored row that never carried a stop reason leaves both unset and prices as it stands.
	StopReason     *string
	StopReasonNull bool
	// CacheWrite1h is the part of Tokens.CacheWrite the request wrote to the 1-hour cache.
	CacheWrite1h *int64
	// Speed is how fast the provider ran it ("fast" bills fast-mode rates).
	Speed *string
	// Geo is the provider's inference region ("us" bills Claude at ×1.1).
	Geo *string
	// ServiceTier is the provider's service tier ("priority" bills MiniMax at ×1.5).
	ServiceTier *string
}

var contextSuffix = regexp.MustCompile(`\[[^\]]*\]$`)

// NormalizeModel maps "zai/glm-5.3[1m]" to "glm-5.3": drop a provider prefix and a
// context-window suffix, lowercased.
func NormalizeModel(model string) string {
	bare := contextSuffix.ReplaceAllString(jsscalar.Trim(model), "")
	unprefixed := bare
	if slash := strings.LastIndex(bare, "/"); slash >= 0 {
		unprefixed = bare[slash+1:]
	}
	return strings.ToLower(unprefixed)
}

// OfferingHostOf is the host the model's own provider offering names ("glm-5.3" →
// "https://api.z.ai"), or false for a model no plugin offering serves — Anthropic's own and
// unknown ids stay unnamed, since a claude call without a recorded upstream went direct and
// must keep saying so.
func OfferingHostOf(model string) (string, bool) {
	id := NormalizeModel(model)
	if id == "" {
		return "", false
	}
	host := ""
	for _, offering := range Offerings {
		if offering.ID == OfferingAnthropic {
			continue
		}
		serves := false
		for key := range offering.Models {
			if NormalizeModel(key) == id {
				serves = true
				break
			}
		}
		if !serves {
			continue
		}
		if host != "" {
			// two offerings serve the same id: neither is the honest answer
			return "", false
		}
		host = "https://" + offering.Host
	}
	return host, host != ""
}

// RequestCost is the estimated USD for one request at its offering's list price, with every
// condition that shaped the sum named in Detail; nil when nothing prices it. The record's
// 1-hour cache writes bill at the 1-hour rate and the rest of the writes at the 5-minute
// one. An upstream the table does not know (a direct call, a router echo) falls back to the
// model id alone, priced when exactly one offering names it.
func RequestCost(r PricedRequest) *PriceResult {
	// A request still streaming (its stop reason null, no output yet) is never priced: its
	// numbers are the opening estimate. A stored row that never carried a stop reason is
	// not a request in flight and prices as it stands.
	if r.StopReasonNull && r.Tokens.Output == 0 {
		return nil
	}
	write1h := 0.0
	if r.CacheWrite1h != nil {
		write1h = float64(*r.CacheWrite1h)
	}
	write5m := float64(r.Tokens.CacheWrite) - write1h
	if write5m < 0 {
		write5m = 0
	}
	var offering *OfferingID
	if r.Upstream != "" {
		if id, ok := OfferingOfHost(r.Upstream); ok {
			offering = &id
		}
	}
	return PriceRequestOf(PriceRequest{
		Offering: offering,
		Model:    r.Model,
		Tokens: TokenUse{
			Input:        float64(r.Tokens.Input),
			Output:       float64(r.Tokens.Output),
			CacheRead:    float64(r.Tokens.CacheRead),
			CacheWrite5m: write5m,
			CacheWrite1h: write1h,
		},
		Ts:          r.Ts,
		Speed:       r.Speed,
		Geo:         r.Geo,
		ServiceTier: r.ServiceTier,
	})
}

// AddCost sums costs where nil means "nothing in here had a price"; a mix sums the priced
// part.
func AddCost(a, b *shared.USD) *shared.USD {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	sum := *a + *b
	return &sum
}

// CostFamily is which family a model id prices under: GLM's (glm-…), Claude's (claude-,
// opus, sonnet, haiku), else other.
type CostFamily string

const (
	FamilyClaude CostFamily = "claude"
	FamilyGLM    CostFamily = "glm"
	FamilyOther  CostFamily = "other"
)

// CostSplit is the cost of some requests split by model family, for views that show
// "X Claude / Y GLM"; computed beside CostOfAll over the very same requests, so the split
// and the total never disagree.
type CostSplit struct {
	Claude *shared.USD `json:"claude"`
	GLM    *shared.USD `json:"glm"`
	Other  *shared.USD `json:"other"`
}

// CostFamilyOf says which family a model id prices under.
func CostFamilyOf(model string) CostFamily {
	id := NormalizeModel(model)
	if strings.HasPrefix(id, "glm") {
		return FamilyGLM
	}
	if strings.HasPrefix(id, "claude") || strings.Contains(id, "opus") ||
		strings.Contains(id, "sonnet") || strings.Contains(id, "haiku") {
		return FamilyClaude
	}
	return FamilyOther
}

// CostByFamily splits a request list's cost by model family; a family stays nil until
// something of it prices.
func CostByFamily(requests []PricedRequest) CostSplit {
	var split CostSplit
	for i := range requests {
		cost := RequestCost(requests[i])
		if cost == nil {
			continue
		}
		switch CostFamilyOf(requests[i].Model) {
		case FamilyClaude:
			split.Claude = AddCost(split.Claude, &cost.USD)
		case FamilyGLM:
			split.GLM = AddCost(split.GLM, &cost.USD)
		default:
			split.Other = AddCost(split.Other, &cost.USD)
		}
	}
	return split
}

// CostOfAll is the total estimated cost of some requests: nil when none of them was priced.
func CostOfAll(requests []PricedRequest) *shared.USD {
	var total *shared.USD
	for i := range requests {
		cost := RequestCost(requests[i])
		if cost == nil {
			continue
		}
		total = AddCost(total, &cost.USD)
	}
	return total
}

// pluginPrefixes maps a model id's prefix to the provider plugin that serves it.
var pluginPrefixes = []struct {
	prefix string
	plugin string
}{
	{"glm-", "zai"},
	{"kimi-", "kimi"},
	{"deepseek-", "deepseek"},
	{"minimax-", "minimax"},
	{"qwen", "qwen"},
}

// PluginOf is the provider plugin that serves a model ("zai" for glm-5.3), or false for
// Anthropic and anything else.
func PluginOf(model string) (string, bool) {
	id := NormalizeModel(model)
	for _, entry := range pluginPrefixes {
		if strings.HasPrefix(id, entry.prefix) {
			return entry.plugin, true
		}
	}
	return "", false
}

// KnownPlugins is the plugin names the dashboard knows, in a stable order (the budget scope
// choices before any data).
var KnownPlugins = []string{"zai", "kimi", "deepseek", "minimax", "qwen"}

// flashOf is the cheaper sibling each main model has in the table, for the model advisor.
var flashOf = map[string]string{
	"glm-5.3":         "glm-5.3-flash",
	"deepseek-v4-pro": "deepseek-flash",
	"qwen3.8-max":     "qwen3.8-flash",
}

// FlashOf is the flash-sized model a main model could hand small work to, or false when
// there is none.
func FlashOf(model string) (string, bool) {
	flash, ok := flashOf[NormalizeModel(model)]
	return flash, ok
}

var smallModel = regexp.MustCompile(`flash|haiku|highspeed`)

// IsSmallModel says whether a model is itself the small one (flash, haiku, highspeed).
func IsSmallModel(model string) bool {
	return smallModel.MatchString(NormalizeModel(model))
}
