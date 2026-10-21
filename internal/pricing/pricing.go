// Package pricing holds the one price model huddle prices with: every provider offering the
// plugins bill against, the conditions each pricing page describes as data, and the math
// that prices a request off them.
package pricing

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// OfferingID is a provider offering: one API host and the model sheet behind it.
type OfferingID string

const (
	OfferingAnthropic OfferingID = "anthropic"
	OfferingZai       OfferingID = "zai"
	OfferingMoonshot  OfferingID = "moonshot"
	OfferingDeepseek  OfferingID = "deepseek"
	OfferingMinimax   OfferingID = "minimax"
	OfferingQwen      OfferingID = "qwen"
)

// Rates is a model's list rates in USD per million tokens.
type Rates struct {
	Input float64 `json:"input"`
	// Output is the per-million output rate.
	Output float64 `json:"output"`
	// CacheRead is a cache hit on previously written input.
	CacheRead float64 `json:"cacheRead"`
	// CacheWrite5m is a cache write at the 5-minute lifetime; 0 where the page lists no price.
	CacheWrite5m float64 `json:"cacheWrite5m"`
	// CacheWrite1h is a cache write at the 1-hour lifetime, where the page lists one.
	CacheWrite1h *float64 `json:"cacheWrite1h,omitempty"`
}

// Tier is rates that replace a model's base once prompt tokens pass Over.
type Tier struct {
	// Over is the prompt-token count (input + cache read + cache write) above which these
	// rates bill.
	Over  float64 `json:"over"`
	Rates Rates   `json:"rates"`
}

// PeakWindow is a UTC window that bills at peak: weekdays (0 = Sunday … 6 = Saturday) and
// the hour range [Start, End).
type PeakWindow struct {
	Days  []int `json:"days"`
	Start int   `json:"start"`
	End   int   `json:"end"`
}

// OffPeak is what a provider discounts when a request falls outside every peak window.
type OffPeak struct {
	Multiplier float64      `json:"multiplier"`
	Peak       []PeakWindow `json:"peak"`
}

// FastRates is input and output for a request that ran in fast mode; the cache rates derive
// from fast input.
type FastRates struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// ModelSheet is one model's sheet on its offering: the base rates and every condition the
// pricing page attaches to them.
type ModelSheet struct {
	Base    Rates      `json:"base"`
	Tiers   []Tier     `json:"tiers,omitempty"`
	OffPeak *OffPeak   `json:"offPeak,omitempty"`
	Fast    *FastRates `json:"fast,omitempty"`
	// Priority is the multiplier on every rate for the provider's priority service tier.
	Priority *float64 `json:"priority,omitempty"`
	// GeoUS is the multiplier on every rate when the request ran with inference_geo "us".
	GeoUS *float64 `json:"geoUs,omitempty"`
	// Verified is false when a figure was inferred rather than seen on the page; Note says which.
	Verified bool    `json:"verified"`
	Note     *string `json:"note,omitempty"`
}

// Offering is one offering: its host, where the figures came from, and every model sheet
// read off that page.
type Offering struct {
	ID OfferingID `json:"id"`
	// Host is the API host that serves these models, as OfferingOfHost matches it.
	Host string `json:"host"`
	// Source is the provider's pricing page the figures were read from.
	Source string `json:"source"`
	// Retrieved is the day (YYYY-MM-DD) they were read.
	Retrieved string                `json:"retrieved"`
	Models    map[string]ModelSheet `json:"models"`
}

// PricesRetrieved is the day (YYYY-MM-DD) the sheets were last read.
const PricesRetrieved = "2026-10-08"

const anthropicSource = "https://platform.claude.com/docs/en/about-claude/pricing"
const zaiSource = "https://docs.z.ai/guides/overview/pricing"
const kimiSource = "https://platform.kimi.ai/docs/pricing/chat"
const deepseekSource = "https://api-docs.deepseek.com/quick_start/pricing"
const minimaxSource = "https://platform.minimax.io/docs/guides/pricing-paygo"
const qwenSource = "https://www.alibabacloud.com/help/en/model-studio/model-pricing"

// deepseekOffPeak: peak is 01:00–04:00 and 06:00–10:00 UTC Monday to Friday; everything
// else bills at half.
var deepseekOffPeak = OffPeak{
	Multiplier: 0.5,
	Peak: []PeakWindow{
		{Days: []int{1, 2, 3, 4, 5}, Start: 1, End: 4},
		{Days: []int{1, 2, 3, 4, 5}, Start: 6, End: 10},
	},
}

const deepseekNote = "peak price; off-peak is half outside 01:00–04:00 and 06:00–10:00 UTC Mon–Fri; " +
	"Chinese public holidays are off-peak there but billed at peak here; cache write not listed"

const qwenNote = "input and output read on the page; cache rates inferred (10% and 125% of input), " +
	"the console has the real ones"

const zaiNote = "cache write not listed; cache storage is limited-time free"

func f64Ptr(value float64) *float64 { return &value }
func strPtr(text string) *string    { return &text }

// Offerings is every offering the plugins can bill against, international endpoints only.
var Offerings = []Offering{
	{
		ID:        OfferingAnthropic,
		Host:      "api.anthropic.com",
		Source:    anthropicSource,
		Retrieved: PricesRetrieved,
		Models: map[string]ModelSheet{
			"claude-fable-5-1": {
				Base:     Rates{Input: 10, Output: 50, CacheRead: 0.25, CacheWrite5m: 12.5, CacheWrite1h: f64Ptr(20)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-fable-5": {
				Base:     Rates{Input: 10, Output: 50, CacheRead: 1, CacheWrite5m: 12.5, CacheWrite1h: f64Ptr(20)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-opus-5-5": {
				Base:     Rates{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite5m: 5, CacheWrite1h: f64Ptr(8)},
				Fast:     &FastRates{Input: 8, Output: 40},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-opus-5": {
				Base:     Rates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite5m: 6.25, CacheWrite1h: f64Ptr(10)},
				Fast:     &FastRates{Input: 10, Output: 50},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-opus-4-8": {
				Base:     Rates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite5m: 6.25, CacheWrite1h: f64Ptr(10)},
				Fast:     &FastRates{Input: 10, Output: 50},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-opus-4-7": {
				Base:     Rates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite5m: 6.25, CacheWrite1h: f64Ptr(10)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-opus-4-6": {
				Base:     Rates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite5m: 6.25, CacheWrite1h: f64Ptr(10)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-opus-4-5": {
				Base:     Rates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite5m: 6.25, CacheWrite1h: f64Ptr(10)},
				Verified: true,
			},
			"claude-sonnet-5-5": {
				Base:     Rates{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite5m: 2.5, CacheWrite1h: f64Ptr(4)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-sonnet-5": {
				Base:     Rates{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite5m: 2.5, CacheWrite1h: f64Ptr(4)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-sonnet-4-6": {
				Base:     Rates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite5m: 3.75, CacheWrite1h: f64Ptr(6)},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
			"claude-sonnet-4-5": {
				Base:     Rates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite5m: 3.75, CacheWrite1h: f64Ptr(6)},
				Verified: true,
			},
			"claude-haiku-4-5": {
				Base:     Rates{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite5m: 1.25, CacheWrite1h: f64Ptr(2)},
				Verified: true,
			},
			"claude-haiku-5-5": {
				Base: Rates{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite5m: 0.125, CacheWrite1h: f64Ptr(0.2)},
				Tiers: []Tier{
					{
						Over:  100_000,
						Rates: Rates{Input: 0.5, Output: 2.5, CacheRead: 0.05, CacheWrite5m: 0.625, CacheWrite1h: f64Ptr(1)},
					},
				},
				GeoUS:    f64Ptr(1.1),
				Verified: true,
			},
		},
	},
	{
		ID:        OfferingZai,
		Host:      "api.z.ai",
		Source:    zaiSource,
		Retrieved: PricesRetrieved,
		Models: map[string]ModelSheet{
			"glm-5.3": {
				Base:     Rates{Input: 1.4, Output: 4.4, CacheRead: 0.26, CacheWrite5m: 0},
				Verified: true,
				Note:     strPtr(zaiNote),
			},
			"glm-5.3-flash": {
				Base:     Rates{Input: 0.15, Output: 0.5, CacheRead: 0.03, CacheWrite5m: 0},
				Verified: true,
				Note:     strPtr(zaiNote),
			},
			"glm-5.3-flashx": {
				Base:     Rates{Input: 0.37, Output: 1.25, CacheRead: 0.075, CacheWrite5m: 0},
				Verified: true,
				Note:     strPtr(zaiNote),
			},
		},
	},
	{
		ID:        OfferingMoonshot,
		Host:      "api.moonshot.ai",
		Source:    kimiSource,
		Retrieved: PricesRetrieved,
		Models: map[string]ModelSheet{
			"kimi-k3": {
				Base:     Rates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite5m: 3, CacheWrite1h: f64Ptr(6)},
				Verified: true,
				Note:     strPtr("cache write 3.00 is the default 5-minute lifetime; the 1-hour lifetime is 6.00"),
			},
			"kimi-k2.6": {
				Base:     Rates{Input: 0.95, Output: 4, CacheRead: 0.16, CacheWrite5m: 0},
				Verified: true,
				Note:     strPtr("cache write not listed"),
			},
		},
	},
	{
		ID:        OfferingDeepseek,
		Host:      "api.deepseek.com",
		Source:    deepseekSource,
		Retrieved: PricesRetrieved,
		Models: map[string]ModelSheet{
			"deepseek-flash": {
				Base:     Rates{Input: 0.3, Output: 1.2, CacheRead: 0.006, CacheWrite5m: 0},
				OffPeak:  &deepseekOffPeak,
				Verified: true,
				Note:     strPtr(deepseekNote),
			},
			"deepseek-v4-pro": {
				Base:     Rates{Input: 1.32, Output: 3.96, CacheRead: 0.044, CacheWrite5m: 0},
				OffPeak:  &deepseekOffPeak,
				Verified: true,
				Note:     strPtr(deepseekNote),
			},
		},
	},
	{
		ID:        OfferingMinimax,
		Host:      "api.minimax.io",
		Source:    minimaxSource,
		Retrieved: PricesRetrieved,
		Models: map[string]ModelSheet{
			"MiniMax-M3": {
				Base: Rates{Input: 0.3, Output: 1.2, CacheRead: 0.06, CacheWrite5m: 0},
				Tiers: []Tier{
					{Over: 512_000, Rates: Rates{Input: 0.6, Output: 2.4, CacheRead: 0.12, CacheWrite5m: 0}},
				},
				Priority: f64Ptr(1.5),
				Verified: true,
				Note: strPtr("up to 512k prompt tokens; above it 0.60 / 2.40 / 0.12; service_tier priority " +
					"is ×1.5; cache write not listed (passive caching is free)"),
			},
			"MiniMax-M2.7": {
				Base:     Rates{Input: 0.3, Output: 1.2, CacheRead: 0.06, CacheWrite5m: 0.375},
				Verified: true,
			},
			"MiniMax-M2.7-highspeed": {
				Base:     Rates{Input: 0.6, Output: 2.4, CacheRead: 0.06, CacheWrite5m: 0.375},
				Verified: true,
			},
		},
	},
	{
		ID:        OfferingQwen,
		Host:      "dashscope-intl.aliyuncs.com",
		Source:    qwenSource,
		Retrieved: PricesRetrieved,
		Models: map[string]ModelSheet{
			"qwen3.8-max": {
				Base:     Rates{Input: 2, Output: 6, CacheRead: 0.2, CacheWrite5m: 2.5},
				Verified: false,
				Note:     strPtr(qwenNote),
			},
			"qwen3.8-flash": {
				Base:     Rates{Input: 0.15, Output: 0.47, CacheRead: 0.015, CacheWrite5m: 0.19},
				Verified: false,
				Note:     strPtr(qwenNote),
			},
		},
	},
}

var bracketSuffix = regexp.MustCompile(`\[[^\]]*\]$`)
var datedSuffix = regexp.MustCompile(`[-@]\d{8}$`)

// canonicalModelID maps "zai/glm-5.3[1m]" to "glm-5.3": drop a provider prefix, a context
// bracket and a dated snapshot suffix, lowercased.
func canonicalModelID(model string) string {
	bare := bracketSuffix.ReplaceAllString(strings.ToLower(jsfmt.Trim(model)), "")
	unprefixed := bare
	if slash := strings.LastIndex(bare, "/"); slash >= 0 {
		unprefixed = bare[slash+1:]
	}
	return datedSuffix.ReplaceAllString(unprefixed, "")
}

// sheetRef is where a canonical id lives; several offerings could name the same id, so the
// list may hold more than one.
type sheetRef struct {
	offering Offering
	// id is the model as its offering's page spells it.
	id    string
	sheet ModelSheet
}

// byCanonicalID indexes every sheet under its canonical id, built once at start.
var byCanonicalID = indexOfferings(Offerings)

func indexOfferings(offerings []Offering) map[string][]sheetRef {
	index := make(map[string][]sheetRef)
	for _, offering := range offerings {
		for id, sheet := range offering.Models {
			key := canonicalModelID(id)
			index[key] = append(index[key], sheetRef{offering, id, sheet})
		}
	}
	return index
}

// resolveSheet is the model a request names on an offering; with no offering, a unique id
// across offerings decides.
func resolveSheet(offering *OfferingID, model string) *sheetRef {
	refs := byCanonicalID[canonicalModelID(model)]
	if len(refs) == 0 {
		return nil
	}
	if offering != nil {
		for i := range refs {
			if refs[i].offering.ID == *offering {
				return &refs[i]
			}
		}
		return nil
	}
	if len(refs) == 1 {
		return &refs[0]
	}
	return nil
}

// OfferingOfHost is the offering that serves a host, given as `https://host/…` or bare
// `host`; false when no offering has that host.
func OfferingOfHost(urlOrHost string) (OfferingID, bool) {
	trimmed := strings.ToLower(jsfmt.Trim(urlOrHost))
	var host string
	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", false
		}
		host = parsed.Hostname()
	} else {
		host = trimmed
		if slash := strings.Index(host, "/"); slash >= 0 {
			host = host[:slash]
		}
		if colon := strings.Index(host, ":"); colon >= 0 {
			host = host[:colon]
		}
	}
	for _, offering := range Offerings {
		if host == offering.Host {
			return offering.ID, true
		}
	}
	return "", false
}

// UniqueOffering is the one offering that serves a canonical model id, with the id as that
// offering's page spells it; false when no offering serves it or more than one does.
func UniqueOffering(model string) (id string, offering OfferingID, ok bool) {
	refs := byCanonicalID[canonicalModelID(model)]
	if len(refs) != 1 {
		return "", "", false
	}
	return refs[0].id, refs[0].offering.ID, true
}

// RatesOf is the base rates a model bills at on the offering its upstream host names —
// or, with no upstream, the one offering that serves the id — or false when no price
// covers it. Views that compare rates (a cache read against the input rate) read these;
// the per-request price itself stays with PriceRequestOf.
func RatesOf(model, upstream string) (Rates, bool) {
	var offering *OfferingID
	if upstream != "" {
		if id, ok := OfferingOfHost(upstream); ok {
			offering = &id
		}
	}
	ref := resolveSheet(offering, model)
	if ref == nil {
		return Rates{}, false
	}
	return ref.sheet.Base, true
}

// TokenUse is what a request used, as the spool and the job attempts record them.
type TokenUse struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cacheRead"`
	CacheWrite5m float64 `json:"cacheWrite5m"`
	CacheWrite1h float64 `json:"cacheWrite1h"`
}

// PriceRequest is one request to price: where it ran, what it used, and every condition the
// provider bills on.
type PriceRequest struct {
	// Offering is the offering that served the request, or nil when only the model id is
	// known.
	Offering *OfferingID `json:"offering"`
	Model    string      `json:"model"`
	Tokens   TokenUse    `json:"tokens"`
	// Ts is when the request ran, as a Unix epoch ms — off-peak windows are UTC.
	Ts int64 `json:"ts"`
	// Speed is fast mode when the provider names it so.
	Speed *string `json:"speed,omitempty"`
	// Geo is inference_geo; "us" bills Claude 4.6 and later at ×1.1.
	Geo *string `json:"geo,omitempty"`
	// ServiceTier is the provider's service tier; "priority" bills MiniMax at ×1.5.
	ServiceTier *string `json:"serviceTier,omitempty"`
}

// PriceResult is what a request cost and, in short words, every condition that shaped the sum.
type PriceResult struct {
	USD    float64  `json:"usd"`
	Detail []string `json:"detail"`
}

var offeringLabel = map[OfferingID]string{
	OfferingAnthropic: "Anthropic list",
	OfferingZai:       "Z.ai list",
	OfferingMoonshot:  "Moonshot list",
	OfferingDeepseek:  "DeepSeek list",
	OfferingMinimax:   "MiniMax list",
	OfferingQwen:      "Qwen list",
}

func inPeak(ts int64, peak []PeakWindow) bool {
	at := time.UnixMilli(ts).UTC()
	day := int(at.Weekday())
	hour := at.Hour()
	return slices.ContainsFunc(peak, func(window PeakWindow) bool {
		return hour >= window.Start && hour < window.End && slices.Contains(window.Days, day)
	})
}

func tierLabel(tokens float64) string {
	if tokens >= 1_000_000 {
		return jsfmt.JSNumber(tokens/1_000_000) + "M"
	}
	return jsfmt.JSNumber(tokens/1000) + "k"
}

func isFast(speed *string) bool {
	return speed != nil && strings.ToLower(jsfmt.Trim(*speed)) == "fast"
}

// tierOver is the tier a request's prompt tokens land it in, or nil for the base rates.
func tierOver(sheet ModelSheet, promptTokens float64) *Tier {
	var tier *Tier
	for i := range sheet.Tiers {
		if promptTokens > sheet.Tiers[i].Over {
			tier = &sheet.Tiers[i]
		}
	}
	return tier
}

// billedRates is the rates a request bills, before any condition multiplies them.
type billedRates struct {
	input     float64
	output    float64
	cacheRead float64
	write5m   float64
	write1h   float64
}

// billedFrom is the sheet rates as billed; a page with no 1-hour rate bills the 1-hour
// tokens at the 5-minute one.
func billedFrom(rates Rates) billedRates {
	write1h := rates.CacheWrite5m
	if rates.CacheWrite1h != nil {
		write1h = *rates.CacheWrite1h
	}
	return billedRates{
		input:     rates.Input,
		output:    rates.Output,
		cacheRead: rates.CacheRead,
		write5m:   rates.CacheWrite5m,
		write1h:   write1h,
	}
}

// billedFast is fast mode's rates: the page prices input/output only, so the cache rates
// keep their ratios of input.
func billedFast(fast FastRates, rates Rates) billedRates {
	cacheRead := 0.0
	if rates.Input != 0 {
		cacheRead = (fast.Input * rates.CacheRead) / rates.Input
	}
	return billedRates{
		input:     fast.Input,
		output:    fast.Output,
		cacheRead: cacheRead,
		write5m:   1.25 * fast.Input,
		write1h:   2 * fast.Input,
	}
}

// priceCondition is one sheet condition the request tripped: its multiplier and the short
// words that name it in Detail.
type priceCondition struct {
	multiplier float64
	label      string
}

// conditionsOf lists every sheet condition the request trips.
func conditionsOf(sheet ModelSheet, request PriceRequest) []priceCondition {
	conditions := make([]priceCondition, 0, 3)
	if sheet.Priority != nil && request.ServiceTier != nil &&
		strings.ToLower(jsfmt.Trim(*request.ServiceTier)) == "priority" {
		conditions = append(conditions, priceCondition{*sheet.Priority, "priority ×" + jsfmt.JSNumber(*sheet.Priority)})
	}
	if sheet.OffPeak != nil && !inPeak(request.Ts, sheet.OffPeak.Peak) {
		conditions = append(conditions, priceCondition{
			sheet.OffPeak.Multiplier, "off-peak ×" + jsfmt.JSNumber(sheet.OffPeak.Multiplier),
		})
	}
	if sheet.GeoUS != nil && request.Geo != nil && strings.ToLower(jsfmt.Trim(*request.Geo)) == "us" {
		conditions = append(conditions, priceCondition{*sheet.GeoUS, "US geo ×" + jsfmt.JSNumber(*sheet.GeoUS)})
	}
	return conditions
}

// PriceRequestOf is what one request cost at the sheet's rates, with each applied condition
// named in Detail; nil when the table does not know the model.
func PriceRequestOf(request PriceRequest) *PriceResult {
	ref := resolveSheet(request.Offering, request.Model)
	if ref == nil {
		return nil
	}
	detail := []string{offeringLabel[ref.offering.ID]}

	// Prompt tokens are what the provider tiers on: everything the request put in front of
	// the model.
	tokens := request.Tokens
	promptTokens := tokens.Input + tokens.CacheRead + tokens.CacheWrite5m + tokens.CacheWrite1h
	tier := tierOver(ref.sheet, promptTokens)
	if tier != nil {
		detail = append(detail, "tier over "+tierLabel(tier.Over))
	}
	rates := ref.sheet.Base
	if tier != nil {
		rates = tier.Rates
	}

	fast := ref.sheet.Fast
	if !isFast(request.Speed) {
		fast = nil
	}
	if fast != nil {
		detail = append(detail, "fast mode")
	}
	billed := billedFrom(rates)
	if fast != nil {
		billed = billedFast(*fast, rates)
	}

	for _, condition := range conditionsOf(ref.sheet, request) {
		billed.input *= condition.multiplier
		billed.output *= condition.multiplier
		billed.cacheRead *= condition.multiplier
		billed.write5m *= condition.multiplier
		billed.write1h *= condition.multiplier
		detail = append(detail, condition.label)
	}

	var total float64
	total += PerMillion(tokens.Input, billed.input)
	total += PerMillion(tokens.Output, billed.output)
	total += PerMillion(tokens.CacheRead, billed.cacheRead)
	total += PerMillion(tokens.CacheWrite5m, billed.write5m)
	if tokens.CacheWrite1h > 0 {
		total += PerMillion(tokens.CacheWrite1h, billed.write1h)
		detail = append(detail, "1 h cache writes")
	}

	return &PriceResult{USD: total, Detail: detail}
}

// PerMillion prices one token count at its per-million rate; the explicit conversion rounds
// the product, which per the Go spec prevents fusing it into the caller's addition (JS
// rounds every * and + on its own).
func PerMillion(count, rate float64) float64 {
	return float64((count / 1_000_000) * rate)
}
