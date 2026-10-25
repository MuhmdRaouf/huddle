package ledger

import (
	"context"
	"fmt"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
)

// Store is the ledger's SQLite file, opened through the same driver history.db uses.
type Store struct{ db history.DB }

// Open opens or creates the file and applies the schema.
func Open(file string) (*Store, error) {
	db, err := history.OpenDB(file)
	if err != nil {
		return nil, err
	}
	if err := applySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error {
	return s.db.Close()
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
	_, err := s.db.Run(query,
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
func rowOf(found map[string]history.SqlValue) Row {
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

func textColumn(row map[string]history.SqlValue, name string) string {
	text, _ := row[name].(string)
	return text
}

func intColumn(row map[string]history.SqlValue, name string) int64 {
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

func intPtrColumn(row map[string]history.SqlValue, name string) *int64 {
	if row[name] == nil {
		return nil
	}
	value := intColumn(row, name)
	return &value
}

func floatColumn(row map[string]history.SqlValue, name string) float64 {
	switch value := row[name].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	}
	return 0
}

func floatPtrColumn(row map[string]history.SqlValue, name string) *float64 {
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
