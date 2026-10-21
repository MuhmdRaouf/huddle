package store

import (
	"cmp"
	"math"
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// jsRound rounds the way JavaScript's Math.round does: halves go up, so 2.5 is 3 and
// -2.5 is -2.
func jsRound(x float64) float64 {
	return math.Floor(x + 0.5)
}

// Percentile is the linear-interpolation percentile of values (nearest-rank feels steppy
// on small samples); nil for no data.
func Percentile(values []int64, p float64) *int64 {
	if len(values) == 0 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.SortStableFunc(sorted, func(a, b int64) int { return cmp.Compare(a, b) })
	rank := (p / 100) * float64(len(sorted)-1)
	low := int(min(math.Floor(rank), float64(len(sorted)-1)))
	high := int(min(math.Ceil(rank), float64(len(sorted)-1)))
	lowValue := sorted[max(low, 0)]
	if low == high {
		return &lowValue
	}
	highValue := sorted[high]
	weight := rank - float64(low)
	value := int64(jsRound(float64(lowValue) + float64(highValue-lowValue)*weight))
	return &value
}

// MeanOf is the rounded mean; nil for no data.
func MeanOf(values []int64) *int64 {
	if len(values) == 0 {
		return nil
	}
	var total int64
	for _, value := range values {
		total += value
	}
	mean := int64(jsRound(float64(total) / float64(len(values))))
	return &mean
}

// stopErrors are the stop reasons the summary counts as errors.
var stopErrors = map[string]struct{}{
	"error":            {},
	"overloaded_error": {},
	"api_error":        {},
	"request_error":    {},
}

// isStopError says whether a stop reason is one the summary counts as an error.
func isStopError(stopReason *string) bool {
	if stopReason == nil {
		return false
	}
	_, bad := stopErrors[*stopReason]
	return bad
}

// Summarize is the dashboard's totals row over the records in view.
func Summarize(sessions []shared.SessionView, requests []*shared.RequestRecord, tools []*shared.ToolCallRecord, now int64) shared.Summary {
	var liveSessions, agents, errors int64
	for i := range sessions {
		if sessions[i].Live {
			liveSessions++
		}
		agents += int64(len(sessions[i].Agents))
	}
	tokens := shared.ZeroTokens
	for i := range requests {
		tokens = shared.AddTokens(tokens, requests[i].Tokens)
	}
	for i := range tools {
		if !tools[i].Ok {
			errors++
		}
	}
	for i := range requests {
		if isStopError(requests[i].StopReason) {
			errors++
		}
	}
	var startedAt *int64
	for i := range sessions {
		if sessions[i].StartedAt != nil && (startedAt == nil || *sessions[i].StartedAt < *startedAt) {
			startedAt = sessions[i].StartedAt
		}
	}
	latencies := latenciesOf(requests)
	return shared.Summary{
		Sessions:     int64(len(sessions)),
		LiveSessions: liveSessions,
		Agents:       agents,
		Requests:     int64(len(requests)),
		Tokens:       tokens,
		Errors:       errors,
		ToolCalls:    int64(len(tools)),
		LatencyP50:   Percentile(latencies, 50),
		LatencyP95:   Percentile(latencies, 95),
		StartedAt:    startedAt,
		Now:          now,
		CostUsd:      cost.CostOfAll(pricedRequests(requests)),
	}
}

// ToolCountRow is one tool's row in the tools table.
type ToolCountRow struct {
	Name     string `json:"name"`
	Count    int64  `json:"count"`
	Failures int64  `json:"failures"`
}

// ModelRows is one row per model in the models table, biggest token total first.
func ModelRows(requests []*shared.RequestRecord) []shared.ModelRow {
	type bucket struct {
		requests []*shared.RequestRecord
	}
	var order []string
	byModel := make(map[string]*bucket)
	for i := range requests {
		b, ok := byModel[requests[i].Model]
		if !ok {
			b = &bucket{}
			byModel[requests[i].Model] = b
			order = append(order, requests[i].Model)
		}
		b.requests = append(b.requests, requests[i])
	}
	rows := make([]shared.ModelRow, 0, len(order))
	for _, model := range order {
		b := byModel[model]
		rows = append(rows, shared.ModelRow{
			Model:      model,
			Provider:   shared.ProviderOf(model),
			Requests:   int64(len(b.requests)),
			Errors:     countErrors(b.requests),
			Tokens:     sumTokens(b.requests),
			LatencyP50: Percentile(latenciesOf(b.requests), 50),
			CostUsd:    cost.CostOfAll(pricedRequests(b.requests)),
		})
	}
	slices.SortStableFunc(rows, func(a, b shared.ModelRow) int {
		return cmp.Compare(shared.TotalTokens(b.Tokens), shared.TotalTokens(a.Tokens))
	})
	return rows
}

// UpstreamRows is one row per host in the upstreams table, biggest token total first: a
// router's "api.z.ai" and a session's "https://api.z.ai" are the same endpoint.
func UpstreamRows(requests []*shared.RequestRecord) []shared.UpstreamRow {
	type bucket struct {
		requests []*shared.RequestRecord
	}
	var order []string
	byHost := make(map[string]*bucket)
	for i := range requests {
		host := shared.UpstreamHost(requests[i].Upstream)
		b, ok := byHost[host]
		if !ok {
			b = &bucket{}
			byHost[host] = b
			order = append(order, host)
		}
		b.requests = append(b.requests, requests[i])
	}
	rows := make([]shared.UpstreamRow, 0, len(order))
	for _, host := range order {
		b := byHost[host]
		rows = append(rows, shared.UpstreamRow{
			Upstream: b.requests[0].Upstream,
			Host:     host,
			Requests: int64(len(b.requests)),
			Tokens:   sumTokens(b.requests),
			CostUsd:  cost.CostOfAll(pricedRequests(b.requests)),
		})
	}
	slices.SortStableFunc(rows, func(a, b shared.UpstreamRow) int {
		return cmp.Compare(shared.TotalTokens(b.Tokens), shared.TotalTokens(a.Tokens))
	})
	return rows
}

// ToolCounts counts tool calls per tool name, most used first, names breaking ties. Calls named
// "unknown" stay out: they are results whose tool_use was never read, not a tool, and older
// records still carrying the name must not reach the Top tools ranking under it.
func ToolCounts(tools []*shared.ToolCallRecord) []ToolCountRow {
	type tally struct {
		count    int64
		failures int64
	}
	var order []string
	byName := make(map[string]*tally)
	for i := range tools {
		if tools[i].Name == "unknown" {
			continue
		}
		t, ok := byName[tools[i].Name]
		if !ok {
			t = &tally{}
			byName[tools[i].Name] = t
			order = append(order, tools[i].Name)
		}
		t.count++
		if !tools[i].Ok {
			t.failures++
		}
	}
	rows := make([]ToolCountRow, 0, len(order))
	for _, name := range order {
		rows = append(rows, ToolCountRow{Name: name, Count: byName[name].count, Failures: byName[name].failures})
	}
	slices.SortStableFunc(rows, func(a, b ToolCountRow) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return jsfmt.LocaleCompare(a.Name, b.Name)
	})
	return rows
}

// latenciesOf collects the requests' measured latencies, leaving the unmeasured out.
func latenciesOf(requests []*shared.RequestRecord) []int64 {
	latencies := make([]int64, 0, len(requests))
	for i := range requests {
		if requests[i].LatencyMs != nil {
			latencies = append(latencies, *requests[i].LatencyMs)
		}
	}
	return latencies
}

// countErrors counts the requests whose stop reason is one of the error set's.
func countErrors(requests []*shared.RequestRecord) int64 {
	var errors int64
	for i := range requests {
		if isStopError(requests[i].StopReason) {
			errors++
		}
	}
	return errors
}

// sumTokens adds the requests' token counters together.
func sumTokens(requests []*shared.RequestRecord) shared.Tokens {
	tokens := shared.ZeroTokens
	for i := range requests {
		tokens = shared.AddTokens(tokens, requests[i].Tokens)
	}
	return tokens
}

// pricedRequests carries a record list's pricing fields to the cost reader; a record with
// no stop reason is one the stream may still be writing, whose opening numbers never
// price.
func pricedRequests(records []*shared.RequestRecord) []cost.PricedRequest {
	priced := make([]cost.PricedRequest, 0, len(records))
	for i := range records {
		priced = append(priced, cost.PricedRequest{
			Model:          records[i].Model,
			Ts:             records[i].Ts,
			Tokens:         records[i].Tokens,
			Upstream:       records[i].Upstream,
			StopReason:     records[i].StopReason,
			StopReasonNull: records[i].StopReason == nil,
			CacheWrite1h:   records[i].CacheWrite1h,
			Speed:          records[i].Speed,
			Geo:            records[i].Geo,
			ServiceTier:    records[i].ServiceTier,
		})
	}
	return priced
}
