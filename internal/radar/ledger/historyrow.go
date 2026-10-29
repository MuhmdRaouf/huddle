package ledger

import (
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// historyRow answers the row one history.db request row's facts build alone: each field as
// the row stored it, under the same defaults and gradings the live sources' rules apply.
func historyRow(h HistoryFacts) Row {
	status, statusErr := strconv.ParseInt(h.Status, 10, 64)
	row := Row{
		ID:             h.ID,
		AnthropicID:    h.Headers["request-id"],
		StartedAt:      h.Ts,
		TtfbMs:         h.TtfbMs,
		TtftMs:         h.TtftMs,
		DurationMs:     h.LatencyMs,
		Harness:        h.Harness,
		Model:          h.Model,
		FallbackFrom:   h.Fallback,
		Upstream:       h.Upstream,
		SessionID:      h.SessionID,
		AgentID:        h.AgentID,
		ParentAgentID:  h.ParentAgentID,
		Status:         status,
		ErrorCode:      h.Error,
		StopReason:     h.StopReason,
		Input:          h.Input,
		Output:         h.Output,
		CacheRead:      h.CacheRead,
		CacheWrite:     h.CacheWrite,
		CacheWrite1h:   h.CacheWrite1h,
		Speed:          h.Speed,
		Geo:            h.Geo,
		ServiceTier:    h.ServiceTier,
		Provider:       h.Provider,
		Via:            h.Via,
		What:           h.What,
		Thinking:       h.Thinking,
		Headers:        h.Headers,
		Totals:         h.Totals,
		NodeID:         h.NodeID,
		PriceUSD:       h.PriceUSD,
		HarnessCostUSD: h.HarnessCostUSD,
		PromptHash:     h.PromptHash,
		InTranscript:   h.FromTranscript,
	}
	switch {
	case h.Route == "anthropic":
		row.Lane = "anthropic"
	case h.Route == "provider":
		if h.Via != nil {
			row.Lane = "provider:" + *h.Via
		} else {
			row.Lane = "provider"
		}
	case h.Route != "":
		row.Lane = h.Route
	}
	if row.Harness == "" {
		row.Harness = "claude"
	}
	if row.SessionID == "" {
		row.SessionID = shared.OutsideSession
	}
	// the store files an untagged call under the main agent (store.go waitRouteLocked)
	if row.AgentID == "" {
		row.AgentID = "main"
	}
	if row.Provider == "" {
		row.Provider = shared.ProviderOf(h.Model)
	}
	if h.Input > 0 || h.Output > 0 || h.CacheRead > 0 || h.CacheWrite > 0 {
		row.UsageSource = "history"
	}
	if h.Status != "" {
		switch {
		case status >= 200 && status < 300:
			row.Outcome = "ok"
		case h.Route == "refused":
			row.Outcome = "refused"
		case (status == 0 || status == 502 || status == 503) && h.Error != "":
			row.Outcome = "upstream_failed"
		default:
			row.Outcome = "http_error"
		}
	}
	// ADR-5 point 4: history files a failed route's status text as its stop reason, while the
	// row keeps stop_reason for the model and tells the failure by status, outcome and error
	// code — the agreement the ledger/history differential holds both stores to.
	if row.StopReason == h.Status && (statusErr != nil || status < 200 || status >= 300) {
		row.StopReason = ""
	}
	return row
}

// fillFromHistory folds history facts into a row the live sources built: history fills only
// the fields they left empty, live sources ruling every field they speak for.
func fillFromHistory(row *Row, h HistoryFacts) {
	hr := historyRow(h)
	if row.StartedAt == 0 {
		row.StartedAt = hr.StartedAt
	}
	if row.DurationMs == 0 {
		row.DurationMs = hr.DurationMs
	}
	if row.TtfbMs == nil {
		row.TtfbMs = hr.TtfbMs
	}
	if row.TtftMs == nil {
		row.TtftMs = hr.TtftMs
	}
	if row.Lane == "" {
		row.Lane = hr.Lane
	}
	if row.Model == "" {
		row.Model = hr.Model
	}
	if row.Upstream == "" {
		row.Upstream = hr.Upstream
	}
	if row.ParentAgentID == "" {
		row.ParentAgentID = hr.ParentAgentID
	}
	if row.FallbackFrom == "" {
		row.FallbackFrom = hr.FallbackFrom
	}
	if row.PromptHash == "" {
		row.PromptHash = hr.PromptHash
	}
	if row.CacheWrite1h == nil {
		row.CacheWrite1h = hr.CacheWrite1h
	}
	if row.Speed == nil {
		row.Speed = hr.Speed
	}
	if row.Geo == nil {
		row.Geo = hr.Geo
	}
	if row.ServiceTier == nil {
		row.ServiceTier = hr.ServiceTier
	}
	if row.Provider == "" {
		row.Provider = hr.Provider
	}
	if row.Via == nil {
		row.Via = hr.Via
	}
	if row.What == nil {
		row.What = hr.What
	}
	if row.Thinking == nil {
		row.Thinking = hr.Thinking
	}
	if len(row.Headers) == 0 {
		row.Headers = hr.Headers
	}
	if row.NodeID == "" {
		row.NodeID = hr.NodeID
	}
	if row.HarnessCostUSD == nil {
		row.HarnessCostUSD = hr.HarnessCostUSD
	}
	if row.PriceUSD == 0 {
		row.PriceUSD = hr.PriceUSD
	}
	if row.UsageSource == "" {
		row.Input = hr.Input
		row.Output = hr.Output
		row.CacheRead = hr.CacheRead
		row.CacheWrite = hr.CacheWrite
		row.UsageSource = hr.UsageSource
	}
	if row.Outcome == "" {
		row.Outcome = hr.Outcome
		row.Status = hr.Status
		row.ErrorCode = hr.ErrorCode
	}
	if row.StopReason == "" {
		row.StopReason = hr.StopReason
	}
}
