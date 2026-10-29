package ledger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// routeTsLayout is how the hash-named ledger ids spell their ts: the router's own
// rendering of an event's moment, UTC with milliseconds. It must keep matching
// internal/core/router/crashlog.go's isoNow, the spelling every live hash-named id was
// built with.
const routeTsLayout = "2006-01-02T15:04:05.000Z07:00"

// matchMs is how far apart a history row's ts and a ledger row's started_at may sit for
// rule 5 to call them one call: the window a call's two copies pair inside. It must equal
// internal/radar/store/merge.go's MatchMs, the twin migrate_test.go pins it against.
const matchMs = 10_000

// Match is what the ledger already knows of one history row: the id the row files under,
// whether the ledger holds that call already, the rule that decided it, and for rule 5 the
// ledger row that claimed it.
type Match struct {
	ID        string
	InLedger  bool
	Rule      int    // 1..6, the rules below
	ClaimedBy string // rule 5 only: the ledger id of the row within the window; "" otherwise
}

// MatchOf names the ledger id a history row files under and whether the ledger holds it.
// The rules apply in order; the first that fits the row decides.
func (s *Store) MatchOf(ctx context.Context, h HistoryRow) (Match, error) {
	if err := ctx.Err(); err != nil {
		return Match{}, fmt.Errorf("match the history row %s: %w", h.ID, err)
	}
	switch {
	case strings.HasPrefix(h.ID, "codex:"), strings.HasPrefix(h.ID, "opencode:"):
		// 1. a harness record files under its own id
		return s.ownRow(ctx, h.ID, 1)
	case h.Headers["request-id"] != "":
		// 2. the request-id header names the call, whether the ledger filed it under
		//    the header as its id or only named the row with it
		header := h.Headers["request-id"]
		held, err := s.ledgerHoldsID(ctx, header)
		if err != nil {
			return Match{}, err
		}
		if !held {
			row, err := s.ByAnthropicID(ctx, header)
			if err != nil {
				return Match{}, err
			}
			held = row != nil
		}
		return Match{ID: header, InLedger: held, Rule: 2}, nil
	case strings.HasPrefix(h.ID, "req_"):
		// 3. a transcript request id with no header files under its own id
		return s.ownRow(ctx, h.ID, 3)
	case strings.HasPrefix(h.ID, "route:") && h.PromptHash != "":
		// 4. a route-only row keeps the route's own ts, so the hash-named id the live
		//    filing built is spelled again from the row
		return s.ownRow(ctx, hashID(h.PromptHash, h.Ts), 4)
	case h.Route != "" && h.PromptHash != "":
		// 5. a merged copy-first row carries the copy's ts, so the hash-named id can
		//    only be searched for inside the pairing window
		return s.matchWindow(ctx, h)
	default:
		// 6. nothing names the call: the row files under its own history id
		return s.ownRow(ctx, h.ID, 6)
	}
}

// ownRow answers the match that files the row under id, for the rules that name the id
// exactly: InLedger is whether the ledger holds that id.
func (s *Store) ownRow(ctx context.Context, id string, rule int) (Match, error) {
	held, err := s.ledgerHoldsID(ctx, id)
	if err != nil {
		return Match{}, err
	}
	return Match{ID: id, InLedger: held, Rule: rule}, nil
}

// ledgerHoldsID answers whether the ledger holds a request row under id.
func (s *Store) ledgerHoldsID(ctx context.Context, id string) (bool, error) {
	found, err := s.db.Get("SELECT id FROM ledger_requests WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("match the history row %s: %w", id, err)
	}
	return found != nil, nil
}

// matchWindow answers rule 5, a merged copy-first row: history kept the transcript copy's
// ts, so the ledger row the call filed under — named by the route's own ts — shows only
// inside the pairing window: the same prompt hash, session and model, within matchMs, the
// closest first and the smallest id breaking a tie.
func (s *Store) matchWindow(ctx context.Context, h HistoryRow) (Match, error) {
	session := h.SessionID
	if session == "" {
		session = shared.OutsideSession
	}
	found, err := s.db.Get(
		"SELECT id FROM ledger_requests "+
			"WHERE prompt_hash = ? AND session_id = ? AND model = ? AND ABS(started_at - ?) <= ? "+
			"ORDER BY ABS(started_at - ?), id LIMIT 1",
		h.PromptHash, session, h.Model, h.Ts, matchMs, h.Ts)
	if err != nil {
		return Match{}, fmt.Errorf("match the history row %s: %w", h.ID, err)
	}
	match := Match{ID: h.ID, Rule: 5}
	if found != nil {
		match.InLedger = true
		match.ClaimedBy = textColumn(found, "id")
	}
	return match, nil
}

// hashID spells the ledger id a hash-named row files under: the prompt hash joined to the
// ts in the router's own rendering.
func hashID(hash string, ts int64) string {
	return hash + ":" + time.UnixMilli(ts).UTC().Format(routeTsLayout)
}

// HistoryFactsOf builds the facts the row files when the ledger lacks it, under id.
func HistoryFactsOf(h HistoryRow, id string) HistoryFacts {
	facts := HistoryFacts{
		ID:             id,
		Ts:             h.Ts,
		NodeID:         h.NodeID,
		SessionID:      h.SessionID,
		AgentID:        h.AgentID,
		ParentAgentID:  h.ParentAgentID,
		Model:          h.Model,
		Upstream:       h.Upstream,
		LatencyMs:      h.LatencyMs,
		TtfbMs:         h.TtfbMs,
		TtftMs:         h.TtftMs,
		Input:          h.Input,
		Output:         h.Output,
		CacheRead:      h.CacheRead,
		CacheWrite:     h.CacheWrite,
		CacheWrite1h:   h.CacheWrite1h,
		Speed:          h.Speed,
		Geo:            h.Geo,
		ServiceTier:    h.ServiceTier,
		StopReason:     h.StopReason,
		Error:          h.Error,
		Fallback:       h.Fallback,
		Provider:       h.Provider,
		Route:          h.Route,
		Via:            h.Via,
		Status:         h.Status,
		Harness:        h.Harness,
		HarnessCostUSD: h.CostUSD,
		What:           h.What,
		Thinking:       h.Thinking,
		PromptHash:     h.PromptHash,
		Headers:        h.Headers,
		Totals:         h.Totals,
		// a route event's or harness record's id marks its own source; history holds any
		// other row because a transcript copy carried it
		FromTranscript: !strings.HasPrefix(h.ID, "route:") &&
			!strings.HasPrefix(h.ID, "codex:") && !strings.HasPrefix(h.ID, "opencode:"),
	}
	facts.PriceUSD = historyPriceUSD(h)
	return facts
}

// historyPriceUSD prices the row's usage the way the routes view prices a record: a stored
// row prices as it stands, and a model the table does not know prices 0.
func historyPriceUSD(h HistoryRow) float64 {
	var stopReason *string
	if h.StopReason != "" {
		stopReason = &h.StopReason
	}
	priced := cost.RequestCost(cost.PricedRequest{
		Model:        h.Model,
		Ts:           h.Ts,
		Tokens:       shared.Tokens{Input: h.Input, Output: h.Output, CacheRead: h.CacheRead, CacheWrite: h.CacheWrite},
		Upstream:     h.Upstream,
		StopReason:   stopReason,
		CacheWrite1h: h.CacheWrite1h,
		Speed:        h.Speed,
		Geo:          h.Geo,
		ServiceTier:  h.ServiceTier,
	})
	if priced == nil {
		return 0
	}
	return float64(priced.USD)
}
