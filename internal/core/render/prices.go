package render

// The one price source every cost the plugins print comes from: `/<p>:usage`, the job reports,
// the wait lines and the subagent hook. The price model itself lives once in internal/pricing;
// render keeps its exported price API as thin adapters over it, plus the flat table the five
// provider plugins price from.

import (
	"github.com/MuhmdRaouf/huddle/internal/pricing"
)

// The price model's types, as pricing holds them.
type (
	OfferingID   = pricing.OfferingID
	Rates        = pricing.Rates
	Tier         = pricing.Tier
	PeakWindow   = pricing.PeakWindow
	OffPeak      = pricing.OffPeak
	FastRates    = pricing.FastRates
	ModelSheet   = pricing.ModelSheet
	Offering     = pricing.Offering
	TokenUse     = pricing.TokenUse
	PriceRequest = pricing.PriceRequest
)

const (
	OfferingAnthropic = pricing.OfferingAnthropic
	OfferingZai       = pricing.OfferingZai
	OfferingMoonshot  = pricing.OfferingMoonshot
	OfferingDeepseek  = pricing.OfferingDeepseek
	OfferingMinimax   = pricing.OfferingMinimax
	OfferingQwen      = pricing.OfferingQwen

	// PricesRetrieved is the day the sheets were last read, printed once by `/<p>:usage`.
	PricesRetrieved = pricing.PricesRetrieved
)

// Offerings is every offering the plugins can bill against, international endpoints only.
var Offerings = pricing.Offerings

// OfferingOfHost is the offering that serves a host, given as `https://host/…` or bare `host`;
// false when no offering has that host.
func OfferingOfHost(urlOrHost string) (OfferingID, bool) { return pricing.OfferingOfHost(urlOrHost) }

// PriceResult is what a request cost and, in short words, every condition that shaped the sum.
type PriceResult = pricing.PriceResult

// PriceRequestOf is what one request cost at the sheet's rates, with each applied condition
// named in Detail; nil when the table does not know the model.
func PriceRequestOf(request PriceRequest) *PriceResult { return pricing.PriceRequestOf(request) }

// PriceEntry is one sourced row of the flat table.
type PriceEntry struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	// Cached input read (a cache hit).
	CacheRead float64 `json:"cacheRead"`
	// Cache creation at the base rate; 0 where the provider charges nothing or lists no price.
	CacheWrite float64 `json:"cacheWrite"`
	// The provider's pricing page the figures were read from.
	Source string `json:"source"`
	// The day (YYYY-MM-DD) they were read.
	Retrieved string `json:"retrieved"`
	// False when a figure was inferred rather than seen on the page; Note says which.
	Verified bool    `json:"verified"`
	Note     *string `json:"note,omitempty"`
}

// PriceTable is the flat table the five provider plugins price from: every catalog id they ship,
// at its lowest list tier. Claude is priced through PriceRequestOf alone, so a claude-* id stays
// unpriced here.
var PriceTable = priceTableOf(pricing.Offerings)

// priceTableOf builds the flat table from the offerings, Anthropic's own ids aside.
func priceTableOf(offerings []pricing.Offering) map[string]PriceEntry {
	table := make(map[string]PriceEntry)
	for _, offering := range offerings {
		if offering.ID == OfferingAnthropic {
			continue
		}
		for id, sheet := range offering.Models {
			table[id] = PriceEntry{
				Input:      sheet.Base.Input,
				Output:     sheet.Base.Output,
				CacheRead:  sheet.Base.CacheRead,
				CacheWrite: sheet.Base.CacheWrite5m,
				Source:     offering.Source,
				Retrieved:  offering.Retrieved,
				Verified:   sheet.Verified,
				Note:       sheet.Note,
			}
		}
	}
	return table
}

// PriceOf is one model's flat-table row; false for a model the table does not know (a claude id,
// an env override).
func PriceOf(model string) (PriceEntry, bool) {
	id, offering, ok := pricing.UniqueOffering(model)
	if !ok || offering == OfferingAnthropic {
		return PriceEntry{}, false
	}
	entry, ok := PriceTable[id]
	return entry, ok
}

// TokenCounts is token counts, as the spool and the job attempts record them.
type TokenCounts struct {
	InputTokens      float64 `json:"inputTokens"`
	OutputTokens     float64 `json:"outputTokens"`
	CacheReadTokens  float64 `json:"cacheReadTokens"`
	CacheWriteTokens float64 `json:"cacheWriteTokens"`
}

// CostUsdOf is what a model's tokens cost at its list price; an unknown model costs nothing rather
// than the wrong sum. Cache writes bill as 5-minute writes at the base (peak, pre-tier) rates —
// the flat table has no request to read conditions off; PriceRequestOf is the one that does.
func CostUsdOf(model string, tokens TokenCounts) float64 {
	price, ok := PriceOf(model)
	if !ok {
		return 0
	}
	var total float64
	total += pricing.PerMillion(tokens.InputTokens, price.Input)
	total += pricing.PerMillion(tokens.OutputTokens, price.Output)
	total += pricing.PerMillion(tokens.CacheReadTokens, price.CacheRead)
	total += pricing.PerMillion(tokens.CacheWriteTokens, price.CacheWrite)
	return total
}

// EstimateText is a cost as the reports print it: `est. $0.0123`, or "unpriced" for a model the
// table does not know.
func EstimateText(model string, tokens TokenCounts) string {
	if _, ok := PriceOf(model); !ok {
		return "unpriced"
	}
	return "est. " + Usd(CostUsdOf(model, tokens))
}
