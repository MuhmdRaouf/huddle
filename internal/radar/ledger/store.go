package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	facts, err := s.factsOf(ctx, id)
	if err != nil {
		return err
	}
	if route != nil {
		facts.Route = route // a later route fact replaces the earlier one
	}
	if transcript != nil {
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

// factsOf reads the facts the file already holds for the id; an empty Sources when none.
func (s *Store) factsOf(ctx context.Context, id string) (Sources, error) {
	facts := Sources{}
	if err := ctx.Err(); err != nil {
		return facts, fmt.Errorf("read the ledger facts %s: %w", id, err)
	}
	found, err := s.db.Get("SELECT route, transcript FROM ledger_facts WHERE id = ?", id)
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
	// VALUES mirrors the column list above, one ? per column, in order.
	const query = "INSERT OR REPLACE INTO ledger_requests (" +
		"id, anthropic_id, started_at, ttfb_ms, ttft_ms, duration_ms, lane, harness, " +
		"model, fallback_from, upstream, session_id, agent_id, parent_agent_id, prompt_id, turn_index, " +
		"prev_request, query_source, outcome, status, error_code, stop_reason, input, output, " +
		"cache_read, cache_write, usage_source, cost_usd, cost_reported_usd, ratelimit_5h, " +
		"ratelimit_7d, prompt_hash, in_transcript) VALUES (" +
		"?, ?, ?, ?, ?, ?, ?, ?, " +
		"?, ?, ?, ?, ?, ?, ?, ?, " +
		"?, ?, ?, ?, ?, ?, ?, ?, " +
		"?, ?, ?, ?, ?, ?, ?, ?, " +
		"?)"
	err := s.db.Run(query,
		row.ID, row.AnthropicID, row.StartedAt, row.TtfbMs, row.TtftMs, row.DurationMs,
		row.Lane, row.Harness, row.Model, row.FallbackFrom, row.Upstream,
		row.SessionID, row.AgentID, row.ParentAgentID, row.PromptID, row.TurnIndex,
		row.PrevRequest, row.QuerySource, row.Outcome, row.Status, row.ErrorCode, row.StopReason,
		row.Input, row.Output, row.CacheRead, row.CacheWrite, row.UsageSource,
		row.CostUSD, row.CostReportedUSD, row.Ratelimit5h, row.Ratelimit7d,
		row.PromptHash, boolColumn(row.InTranscript))
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
		ID:              textColumn(found, "id"),
		AnthropicID:     textColumn(found, "anthropic_id"),
		StartedAt:       intColumn(found, "started_at"),
		TtfbMs:          intPtrColumn(found, "ttfb_ms"),
		TtftMs:          intPtrColumn(found, "ttft_ms"),
		DurationMs:      intColumn(found, "duration_ms"),
		Lane:            textColumn(found, "lane"),
		Harness:         textColumn(found, "harness"),
		Model:           textColumn(found, "model"),
		FallbackFrom:    textColumn(found, "fallback_from"),
		Upstream:        textColumn(found, "upstream"),
		SessionID:       textColumn(found, "session_id"),
		AgentID:         textColumn(found, "agent_id"),
		ParentAgentID:   textColumn(found, "parent_agent_id"),
		PromptID:        textColumn(found, "prompt_id"),
		TurnIndex:       intPtrColumn(found, "turn_index"),
		PrevRequest:     textColumn(found, "prev_request"),
		QuerySource:     textColumn(found, "query_source"),
		Outcome:         textColumn(found, "outcome"),
		Status:          intColumn(found, "status"),
		ErrorCode:       textColumn(found, "error_code"),
		StopReason:      textColumn(found, "stop_reason"),
		Input:           intColumn(found, "input"),
		Output:          intColumn(found, "output"),
		CacheRead:       intColumn(found, "cache_read"),
		CacheWrite:      intColumn(found, "cache_write"),
		UsageSource:     textColumn(found, "usage_source"),
		CostUSD:         floatColumn(found, "cost_usd"),
		CostReportedUSD: floatPtrColumn(found, "cost_reported_usd"),
		Ratelimit5h:     floatPtrColumn(found, "ratelimit_5h"),
		Ratelimit7d:     floatPtrColumn(found, "ratelimit_7d"),
		PromptHash:      textColumn(found, "prompt_hash"),
		InTranscript:    intColumn(found, "in_transcript") != 0,
	}
}

func textColumn(row map[string]any, name string) string {
	text, _ := row[name].(string)
	return text
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
