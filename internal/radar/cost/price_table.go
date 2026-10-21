// Package cost prices requests at the list prices internal/pricing keeps and maps model ids
// to the plugins, families and flash siblings the views read.
package cost

import (
	"github.com/MuhmdRaouf/huddle/internal/pricing"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
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

	// PricesRetrieved is the day the sheets were last read.
	PricesRetrieved = pricing.PricesRetrieved
)

// Offerings is every offering the plugins can bill against, international endpoints only.
var Offerings = pricing.Offerings

// OfferingOfHost is the offering that serves a host, given as `https://host/…` or bare
// `host`; false when no offering has that host.
func OfferingOfHost(urlOrHost string) (OfferingID, bool) { return pricing.OfferingOfHost(urlOrHost) }

// RatesOf is the base rates a model bills at on the offering its upstream host names — or,
// with no upstream, the one offering that serves the id — or false when no price covers it.
func RatesOf(model, upstream string) (Rates, bool) { return pricing.RatesOf(model, upstream) }

// PriceResult is what a request cost and, in short words, every condition that shaped the
// sum; USD is the radar USD type the views carry.
type PriceResult struct {
	USD    shared.USD `json:"usd"`
	Detail []string   `json:"detail"`
}

// PriceRequestOf is what one request cost at the sheet's rates, with each applied condition
// named in Detail; nil when the table does not know the model.
func PriceRequestOf(request PriceRequest) *PriceResult {
	result := pricing.PriceRequestOf(request)
	if result == nil {
		return nil
	}
	return &PriceResult{USD: shared.USD(result.USD), Detail: result.Detail}
}
