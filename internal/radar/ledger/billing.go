package ledger

import (
	"encoding/json"
	"math"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The billing columns history carries: the four usage conditions, read out of the same
// usage map the counters come from under the same rules history applies — a whole count
// above zero, a non-empty word — plus the provider and the via, the route naming both when
// it saw the request and the transcript's model naming the provider when it alone did.

// billing fills the six billing columns of row from the sources.
func billing(s Sources, row *Row) {
	counters, _ := chosenUsage(s)
	row.CacheWrite1h = cacheWrite1hOf(counters)
	row.Speed = wordOf(counters, "speed")
	row.Geo = wordOf(counters, "inference_geo")
	row.ServiceTier = wordOf(counters, "service_tier")

	if s.Route != nil {
		event := s.Route.Event
		provider := shared.ProviderOf(event.Model)
		if provider == "other" && event.Plugin != "" {
			provider = shared.PluginLabel(event.Plugin)
		}
		row.Provider = provider
		via := event.Plugin
		if via == "" {
			via = "router"
		}
		row.Via = &via
	} else if s.Transcript != nil || s.Harness != nil {
		row.Provider = shared.ProviderOf(row.Model)
	}
}

// cacheWrite1hOf answers the 1-hour cache writes the usage's cache_creation object holds, a
// whole number above zero; nil when the object says anything else or nothing.
func cacheWrite1hOf(counters domain.RouteUsage) *int64 {
	creation, ok := counters["cache_creation"].(map[string]any)
	if !ok {
		return nil
	}
	if whole, ok := wholeAboveZero(creation["ephemeral_1h_input_tokens"]); ok {
		return &whole
	}
	return nil
}

// wordOf answers the non-empty string the key holds; nil for anything else.
func wordOf(counters domain.RouteUsage, key string) *string {
	if word, ok := counters[key].(string); ok && word != "" {
		return &word
	}
	return nil
}

// wholeAboveZero answers the whole number above zero the value holds, in any of the shapes
// a JSON decode or a Go build hands over; false for anything else.
func wholeAboveZero(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return wholeOfFloat(number)
	case int64:
		return number, number > 0
	case int:
		return int64(number), number > 0
	case json.Number:
		parsed, err := number.Float64()
		if err != nil {
			return 0, false
		}
		return wholeOfFloat(parsed)
	}
	return 0, false
}

// wholeOfFloat answers n as a whole number above zero; a fraction or a zero or less is no
// count at all.
func wholeOfFloat(n float64) (int64, bool) {
	if n > 0 && n == math.Trunc(n) {
		return int64(n), true
	}
	return 0, false
}
