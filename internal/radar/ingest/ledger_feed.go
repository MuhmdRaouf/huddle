package ingest

import (
	"encoding/json"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// What the watcher hands the ledger: each source's own facts about a request, for
// Store.AddRoute and Store.AddTranscript.

// routeFactsOf is a route line's facts: the event decoded from the line's own text, priced
// with Radar's table. False when the text is not a route event.
func routeFactsOf(text string, line shared.SpoolLine, model string, ts int64) (ledger.RouteFacts, bool) {
	var event domain.RouteEvent
	if err := json.Unmarshal([]byte(text), &event); err != nil {
		return ledger.RouteFacts{}, false
	}
	return ledger.RouteFacts{Event: event, CostUSD: routeCostUSD(line, model, ts)}, true
}

// routeCostUSD prices the line's usage the way the routes view prices a record: 0 while the
// answer is still streaming or the table knows no price.
func routeCostUSD(line shared.SpoolLine, model string, ts int64) float64 {
	record := RouteRequest(line, model, ts)
	priced := cost.RequestCost(cost.PricedRequest{
		Model:          model,
		Ts:             ts,
		Tokens:         record.Tokens,
		Upstream:       record.Upstream,
		StopReason:     record.StopReason,
		StopReasonNull: record.StopReason == nil,
	})
	if priced == nil {
		return 0
	}
	return float64(priced.USD)
}

// transcriptFactsOf is what one transcript request record says about its request; false when
// it carries no API request id. The billing conditions ride in the usage map in the shape
// the provider's usage block has, so one reader in the ledger handles both sources.
func transcriptFactsOf(request shared.RequestRecord) (ledger.TranscriptFacts, bool) {
	if request.RequestID == "" {
		return ledger.TranscriptFacts{}, false
	}
	facts := ledger.TranscriptFacts{
		RequestID: request.RequestID,
		Model:     request.Model,
		Usage: domain.RouteUsage{
			"input_tokens":                request.Tokens.Input,
			"output_tokens":               request.Tokens.Output,
			"cache_read_input_tokens":     request.Tokens.CacheRead,
			"cache_creation_input_tokens": request.Tokens.CacheWrite,
		},
	}
	if request.CacheWrite1h != nil {
		facts.Usage["cache_creation"] = map[string]any{"ephemeral_1h_input_tokens": *request.CacheWrite1h}
	}
	if request.Speed != nil {
		facts.Usage["speed"] = *request.Speed
	}
	if request.Geo != nil {
		facts.Usage["inference_geo"] = *request.Geo
	}
	if request.ServiceTier != nil {
		facts.Usage["service_tier"] = *request.ServiceTier
	}
	if request.StopReason != nil {
		facts.StopReason = *request.StopReason
	}
	return facts, true
}
