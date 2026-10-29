package ingest

import (
	"encoding/json"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// What the watcher hands the ledger: each source's own facts about a request, for
// Store.AddRoute, Store.AddTranscript and Store.AddHarness.

// routeFactsOf is a route line's facts: the event decoded from the line's own text, priced
// with Radar's table. False when the text is not a route event. The spool is data, so the
// facts keep only the allow-listed headers, whatever wrote the line.
func routeFactsOf(text string, line shared.SpoolLine, model string, ts int64) (ledger.RouteFacts, bool) {
	var event domain.RouteEvent
	if err := json.Unmarshal([]byte(text), &event); err != nil {
		return ledger.RouteFacts{}, false
	}
	if event.Headers != nil {
		kept := map[string]string{}
		for name, value := range *event.Headers {
			if domain.CapturedHeader(name) {
				kept[name] = value
			}
		}
		if len(kept) == 0 {
			event.Headers = nil
		} else {
			event.Headers = &kept
		}
	}
	return ledger.RouteFacts{Event: event, PriceUSD: routePriceUSD(line, model, ts)}, true
}

// routePriceUSD prices the line's usage the way the routes view prices a record: 0 while the
// answer is still streaming or the table knows no price.
func routePriceUSD(line shared.SpoolLine, model string, ts int64) float64 {
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

// usageOf is a record's usage map in the shape the provider's usage block has: the four
// counters, the thinking count and the billing conditions, each riding only when the record
// has it, so one reader in the ledger handles every source.
func usageOf(request shared.RequestRecord) domain.RouteUsage {
	usage := domain.RouteUsage{
		"input_tokens":                request.Tokens.Input,
		"output_tokens":               request.Tokens.Output,
		"cache_read_input_tokens":     request.Tokens.CacheRead,
		"cache_creation_input_tokens": request.Tokens.CacheWrite,
	}
	if request.Tokens.Thinking != nil {
		usage["output_tokens_details"] = map[string]any{"thinking_tokens": *request.Tokens.Thinking}
	}
	if request.CacheWrite1h != nil {
		usage["cache_creation"] = map[string]any{"ephemeral_1h_input_tokens": *request.CacheWrite1h}
	}
	if request.Speed != nil {
		usage["speed"] = *request.Speed
	}
	if request.Geo != nil {
		usage["inference_geo"] = *request.Geo
	}
	if request.ServiceTier != nil {
		usage["service_tier"] = *request.ServiceTier
	}
	return usage
}

// transcriptFactsOf is what one transcript request record says about its request; false when
// it carries no API request id.
func transcriptFactsOf(request shared.RequestRecord) (ledger.TranscriptFacts, bool) {
	if request.RequestID == "" {
		return ledger.TranscriptFacts{}, false
	}
	facts := ledger.TranscriptFacts{
		RequestID: request.RequestID,
		Ts:        request.Ts,
		Model:     request.Model,
		What:      request.What,
		Totals:    request.Totals,
		SessionID: request.SessionID,
		AgentID:   request.AgentID,
		Via:       request.Via,
		Usage:     usageOf(request),
	}
	if request.StopReason != nil {
		facts.StopReason = *request.StopReason
	}
	return facts, true
}

// harnessFactsOf is what one harness record (codex, opencode) says about its call; false for
// a record that names no id or that Claude made.
func harnessFactsOf(record shared.RequestRecord) (ledger.HarnessFacts, bool) {
	if record.ID == "" ||
		(record.Harness != shared.HarnessCodex && record.Harness != shared.HarnessOpencode) {
		return ledger.HarnessFacts{}, false
	}
	return ledger.HarnessFacts{
		ID:             record.ID,
		Harness:        record.Harness,
		SessionID:      record.SessionID,
		AgentID:        record.AgentID,
		Model:          record.Model,
		Ts:             record.Ts,
		Usage:          usageOf(record),
		HarnessCostUSD: record.CostUSD,
	}, true
}
