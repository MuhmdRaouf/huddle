package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// Store is the ledger's SQLite file.
type Store struct{ db DB }

// Open opens or creates the file (creating its 0700 directory first, the file chmodded
// 0600) and applies the schema.
func Open(file string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("create the directory of %s: %w", file, err)
	}
	db, err := openDB(file)
	if err != nil {
		return nil, err
	}
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(file, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chmod %s: %w", file, err)
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error {
	return s.db.Close()
}

// AddRoute files one route event's facts and re-reconciles its row with what the ledger
// already heard about the same request. A route the ledger cannot name (no request-id
// header and no prompt hash) is skipped.
func (s *Store) AddRoute(ctx context.Context, facts RouteFacts) error {
	row, _ := Reconcile(Sources{Route: &facts})
	if row.ID == "" {
		return nil
	}
	return s.file(ctx, row.ID, &facts, nil)
}

// AddTranscript files what a transcript says about the request its RequestID names and
// re-reconciles that row. An empty RequestID is skipped.
func (s *Store) AddTranscript(ctx context.Context, facts TranscriptFacts) error {
	if facts.RequestID == "" {
		return nil
	}
	return s.file(ctx, facts.RequestID, nil, &facts)
}

// file joins the new facts with whatever the file already holds about the id, then writes
// both the facts row and the reconciled ledger row. The watcher calls this from its one
// goroutine, so the read and the write need no transaction.
func (s *Store) file(ctx context.Context, id string, route *RouteFacts, transcript *TranscriptFacts) error {
	facts, err := factsOf(ctx, s.db, id)
	if err != nil {
		return err
	}
	if route != nil {
		facts.Route = route // a later route fact replaces the earlier one
	}
	if transcript != nil {
		keepSticky(facts.Transcript, transcript)
		facts.Transcript = transcript // a later transcript fact replaces the earlier one
	}
	row, _ := Reconcile(facts) // one caller's source is always set, so this always names a row
	if err := s.writeFacts(ctx, id, facts); err != nil {
		return err
	}
	if err := s.Upsert(ctx, row); err != nil {
		return fmt.Errorf("file the ledger facts %s: %w", id, err)
	}
	return nil
}

// keepSticky folds a held transcript fact into the next copy: history's Refresh keeps the
// fuller what and a result's totals on later copies.
func keepSticky(held, next *TranscriptFacts) {
	if held == nil || next == nil {
		return
	}
	if next.What == nil {
		next.What = held.What
	}
	if held.Totals {
		next.Totals = true
	}
}

// factsOf reads the facts the file already holds for the id through db — the store's or the
// transaction's; an empty Sources when none.
func factsOf(ctx context.Context, db DB, id string) (Sources, error) {
	facts := Sources{}
	if err := ctx.Err(); err != nil {
		return facts, fmt.Errorf("read the ledger facts %s: %w", id, err)
	}
	found, err := db.Get("SELECT route, transcript FROM ledger_facts WHERE id = ?", id)
	if err != nil {
		return facts, fmt.Errorf("read the ledger facts %s: %w", id, err)
	}
	if found == nil {
		return facts, nil
	}
	if text := textColumn(found, "route"); text != "" {
		var route RouteFacts
		if err := json.Unmarshal([]byte(text), &route); err != nil {
			return facts, fmt.Errorf("read the ledger facts %s: %w", id, err)
		}
		facts.Route = &route
	}
	if text := textColumn(found, "transcript"); text != "" {
		var transcript TranscriptFacts
		if err := json.Unmarshal([]byte(text), &transcript); err != nil {
			return facts, fmt.Errorf("read the ledger facts %s: %w", id, err)
		}
		facts.Transcript = &transcript
	}
	return facts, nil
}

// writeFacts replaces the id's facts row with the sources now known.
func (s *Store) writeFacts(ctx context.Context, id string, facts Sources) error {
	route, transcript := "", ""
	if facts.Route != nil {
		text, err := json.Marshal(facts.Route)
		if err != nil {
			return fmt.Errorf("file the ledger facts %s: %w", id, err)
		}
		route = string(text)
	}
	if facts.Transcript != nil {
		text, err := json.Marshal(facts.Transcript)
		if err != nil {
			return fmt.Errorf("file the ledger facts %s: %w", id, err)
		}
		transcript = string(text)
	}
	if err := s.db.Run("INSERT OR REPLACE INTO ledger_facts (id, route, transcript) VALUES (?, ?, ?)",
		id, route, transcript); err != nil {
		return fmt.Errorf("file the ledger facts %s: %w", id, err)
	}
	return nil
}

// Upsert inserts the row, or replaces the one its ID names.
func (s *Store) Upsert(ctx context.Context, row Row) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("upsert the ledger row %s: %w", row.ID, err)
	}
	return upsertRow(s.db, row)
}

// VALUES mirrors the column list above, one ? per column, in order.
const upsertQuery = "INSERT OR REPLACE INTO ledger_requests (" +
	"id, anthropic_id, started_at, ttfb_ms, ttft_ms, duration_ms, lane, harness, " +
	"model, fallback_from, upstream, session_id, agent_id, parent_agent_id, prompt_id, turn_index, " +
	"prev_request, query_source, outcome, status, error_code, stop_reason, input, output, " +
	"cache_read, cache_write, cache_write_1h, speed, geo, service_tier, provider, via, " +
	"what, thinking, headers, totals, node_id, " +
	"usage_source, price_usd, harness_cost_usd, ratelimit_5h, " +
	"ratelimit_7d, prompt_hash, in_transcript, reconciled) VALUES (" +
	"?, ?, ?, ?, ?, ?, ?, ?, " +
	"?, ?, ?, ?, ?, ?, ?, ?, " +
	"?, ?, ?, ?, ?, ?, ?, ?, " +
	"?, ?, ?, ?, ?, ?, ?, ?, " +
	"?, ?, ?, ?, ?, " +
	"?, ?, ?, ?, " +
	"?, ?, ?, ?)"

// upsertRow inserts the row through db, or replaces the one its ID names, stamped with the
// version of the rules that built it.
func upsertRow(db DB, row Row) error {
	var headers any
	if len(row.Headers) > 0 {
		if text, err := domain.MarshalCompactJSON(row.Headers); err == nil {
			headers = string(text)
		}
	}
	err := db.Run(upsertQuery,
		row.ID, row.AnthropicID, row.StartedAt, row.TtfbMs, row.TtftMs, row.DurationMs,
		row.Lane, row.Harness, row.Model, row.FallbackFrom, row.Upstream,
		row.SessionID, row.AgentID, row.ParentAgentID, row.PromptID, row.TurnIndex,
		row.PrevRequest, row.QuerySource, row.Outcome, row.Status, row.ErrorCode, row.StopReason,
		row.Input, row.Output, row.CacheRead, row.CacheWrite,
		row.CacheWrite1h, row.Speed, row.Geo, row.ServiceTier, row.Provider, row.Via,
		row.What, row.Thinking, headers, boolColumn(row.Totals), row.NodeID,
		row.UsageSource,
		row.PriceUSD, row.HarnessCostUSD, row.Ratelimit5h, row.Ratelimit7d,
		row.PromptHash, boolColumn(row.InTranscript), ReconcileVersion)
	if err != nil {
		return fmt.Errorf("upsert the ledger row %s: %w", row.ID, err)
	}
	return nil
}

// Get answers the row the id names, nil when the file holds none.
func (s *Store) Get(ctx context.Context, id string) (*Row, error) {
	return s.read(ctx, "id = ?", id)
}

// ByAnthropicID answers the row the request-id header names, nil when the file holds none.
func (s *Store) ByAnthropicID(ctx context.Context, id string) (*Row, error) {
	return s.read(ctx, "anthropic_id = ?", id)
}

// Prune cuts the ledger back to the days given: every request row started before the
// cutoff — now minus that many days — goes, with the facts rows filed under the same ids.
// Zero or less keeps every row. A row the file heard of without a started_at stays. The
// request rows go first and their facts follow: an in-flight request files its facts before
// its row, so an orphan facts row is a shape the file already holds, while an orphan request
// row would sit in the dashboard with nothing behind it.
func (s *Store) Prune(ctx context.Context, now time.Time, days int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	where := "WHERE started_at IS NOT NULL AND started_at < ?"
	// The ids are read before the delete that would empty the set, and the Get surface
	// answers one row, so they come back GROUP_CONCAT-ed into one string.
	row, err := s.db.Get("SELECT GROUP_CONCAT(id, ',') AS ids FROM ledger_requests "+where, cutoff)
	if err != nil {
		return 0, fmt.Errorf("gather the spent ledger requests: %w", err)
	}
	var ids []string
	if row != nil {
		if joined, ok := row["ids"].(string); ok && joined != "" {
			ids = strings.Split(joined, ",")
		}
	}
	if err := s.db.Run("DELETE FROM ledger_requests "+where, cutoff); err != nil {
		return 0, fmt.Errorf("prune the ledger requests: %w", err)
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := s.db.Run("DELETE FROM ledger_facts WHERE id = ?", id); err != nil {
			return len(ids), fmt.Errorf("prune the ledger facts %s: %w", id, err)
		}
	}
	return len(ids), nil
}

// read answers the first row the where clause matches, nil when it matches none.
func (s *Store) read(ctx context.Context, where, id string) (*Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read the ledger row: %w", err)
	}
	found, err := s.db.Get("SELECT * FROM ledger_requests WHERE "+where, id)
	if err != nil {
		return nil, fmt.Errorf("read the ledger row: %w", err)
	}
	if found == nil {
		return nil, nil
	}
	row := rowOf(found)
	return &row, nil
}

// rowOf converts one ledger_requests row from the driver's map shape into a Row; this is
// the only place the untyped map is touched.
func rowOf(found map[string]any) Row {
	return Row{
		ID:             textColumn(found, "id"),
		AnthropicID:    textColumn(found, "anthropic_id"),
		StartedAt:      intColumn(found, "started_at"),
		TtfbMs:         intPtrColumn(found, "ttfb_ms"),
		TtftMs:         intPtrColumn(found, "ttft_ms"),
		DurationMs:     intColumn(found, "duration_ms"),
		Lane:           textColumn(found, "lane"),
		Harness:        textColumn(found, "harness"),
		Model:          textColumn(found, "model"),
		FallbackFrom:   textColumn(found, "fallback_from"),
		Upstream:       textColumn(found, "upstream"),
		SessionID:      textColumn(found, "session_id"),
		AgentID:        textColumn(found, "agent_id"),
		ParentAgentID:  textColumn(found, "parent_agent_id"),
		PromptID:       textColumn(found, "prompt_id"),
		TurnIndex:      intPtrColumn(found, "turn_index"),
		PrevRequest:    textColumn(found, "prev_request"),
		QuerySource:    textColumn(found, "query_source"),
		Outcome:        textColumn(found, "outcome"),
		Status:         intColumn(found, "status"),
		ErrorCode:      textColumn(found, "error_code"),
		StopReason:     textColumn(found, "stop_reason"),
		Input:          intColumn(found, "input"),
		Output:         intColumn(found, "output"),
		CacheRead:      intColumn(found, "cache_read"),
		CacheWrite:     intColumn(found, "cache_write"),
		CacheWrite1h:   intPtrColumn(found, "cache_write_1h"),
		Speed:          textPtrColumn(found, "speed"),
		Geo:            textPtrColumn(found, "geo"),
		ServiceTier:    textPtrColumn(found, "service_tier"),
		Provider:       textColumn(found, "provider"),
		Via:            textPtrColumn(found, "via"),
		What:           textPtrColumn(found, "what"),
		Thinking:       intPtrColumn(found, "thinking"),
		Headers:        headersColumn(found, "headers"),
		Totals:         intColumn(found, "totals") != 0,
		NodeID:         textColumn(found, "node_id"),
		UsageSource:    textColumn(found, "usage_source"),
		PriceUSD:       floatColumn(found, "price_usd"),
		HarnessCostUSD: floatPtrColumn(found, "harness_cost_usd"),
		Ratelimit5h:    floatPtrColumn(found, "ratelimit_5h"),
		Ratelimit7d:    floatPtrColumn(found, "ratelimit_7d"),
		PromptHash:     textColumn(found, "prompt_hash"),
		InTranscript:   intColumn(found, "in_transcript") != 0,
	}
}

func textColumn(row map[string]any, name string) string {
	text, _ := row[name].(string)
	return text
}

func textPtrColumn(row map[string]any, name string) *string {
	if row[name] == nil {
		return nil
	}
	value := textColumn(row, name)
	return &value
}

// headersColumn answers the header pairs the stored JSON holds; NULL and a value that does
// not decode read as none.
func headersColumn(row map[string]any, name string) map[string]string {
	if row[name] == nil {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(textColumn(row, name)), &headers); err != nil {
		return nil
	}
	return headers
}

func intColumn(row map[string]any, name string) int64 {
	switch value := row[name].(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	case int:
		return int64(value)
	}
	return 0
}

func intPtrColumn(row map[string]any, name string) *int64 {
	if row[name] == nil {
		return nil
	}
	value := intColumn(row, name)
	return &value
}

func floatColumn(row map[string]any, name string) float64 {
	switch value := row[name].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	}
	return 0
}

func floatPtrColumn(row map[string]any, name string) *float64 {
	if row[name] == nil {
		return nil
	}
	value := floatColumn(row, name)
	return &value
}

// boolColumn answers the INTEGER an INTEGER NOT NULL column stores for a bool.
func boolColumn(on bool) int64 {
	if on {
		return 1
	}
	return 0
}
