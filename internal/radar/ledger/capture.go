package ledger

import (
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The capture columns history carries: what the call was, the thinking tokens the chosen
// usage breaks out, the allow-listed response headers and the result-event totals flag.

// capture fills the four capture columns of row from the sources: what and totals name the
// transcript's word, thinking reads the usage map chosenUsage answers, and the headers come
// from the route with the allow-list applied again — the spool is data, so facts filed
// before the list reached the ledger feed may hold other names.
func capture(s Sources, row *Row) {
	counters, _ := chosenUsage(s)
	row.Thinking = thinkingOf(counters)
	if s.Route != nil && s.Route.Event.Headers != nil {
		kept := map[string]string{}
		for name, value := range *s.Route.Event.Headers {
			if domain.CapturedHeader(name) {
				kept[name] = value
			}
		}
		if len(kept) > 0 {
			row.Headers = kept
		}
	}
	if s.Transcript != nil {
		row.What = s.Transcript.What
		row.Totals = s.Transcript.Totals
	}
}

// thinkingOf answers the reasoning tokens the usage's output_tokens_details object holds, a
// whole number above zero; nil when the object says anything else or nothing.
func thinkingOf(counters domain.RouteUsage) *int64 {
	details, ok := counters["output_tokens_details"].(map[string]any)
	if !ok {
		return nil
	}
	if whole, ok := wholeAboveZero(details["thinking_tokens"]); ok {
		return &whole
	}
	return nil
}
