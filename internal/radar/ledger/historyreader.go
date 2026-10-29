package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// HistoryReader reads history.db's request rows and never writes the file: its own pool,
// query_only on every connection, so the sweep cannot write history by construction.
type HistoryReader struct{ pool *sql.DB }

// OpenHistoryReader opens an existing history.db for reading; a missing file is an error.
func OpenHistoryReader(file string) (*HistoryReader, error) {
	// mode=rw opens an existing file and never creates one; query_only refuses every write
	// on the connection, busy_timeout waits out the writer instead of failing a read.
	pool, err := sql.Open("sqlite", "file:"+file+"?mode=rw&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open the history reader for %s: %w", file, err)
	}
	// one connection serves every read: the sweep pages the rows in id order, one page
	// at a time
	pool.SetMaxOpenConns(1)
	// the ping opens the first connection, so a missing or unusable file fails here, on
	// open, not on the first query
	if err := pool.Ping(); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("open the history reader for %s: %w", file, err)
	}
	return &HistoryReader{pool: pool}, nil
}

// Close closes the reader's pool.
func (r *HistoryReader) Close() error { return r.pool.Close() }

// HistoryRow is one history.db requests row as the sweep reads it; a NULL reads as "" or
// 0, except the pointer fields, which keep NULL as nil.
type HistoryRow struct {
	ID, NodeID, SessionID, AgentID, Model, Upstream string
	Ts, LatencyMs                                   int64
	TtfbMs, TtftMs                                  *int64
	Input, Output, CacheRead, CacheWrite            int64
	CacheWrite1h                                    *int64
	Speed, Geo, ServiceTier                         *string
	StopReason, Error, ParentAgentID, Fallback      string
	Provider, Route, Status, Harness                string
	Via                                             *string
	CostUSD                                         *float64
	What                                            *string
	Thinking                                        *int64
	PromptHash                                      string
	Headers                                         map[string]string // the headers column's JSON object; nil when NULL or ""
	Totals                                          bool
}

// historyPageQuery answers the requests rows after one id, in id order. The columns are
// named, never *, so a widened history table cannot shift a scan.
const historyPageQuery = "SELECT id, node_id, session_id, agent_id, model, upstream, ts, " +
	"latency_ms, ttfb_ms, ttft_ms, input, output, cache_read, cache_write, cache_write_1h, " +
	"speed, geo, service_tier, stop_reason, error, parent_agent_id, fallback, provider, " +
	"route, via, status, harness, cost_usd, what, thinking, prompt_hash, headers, totals " +
	"FROM requests WHERE id > ? ORDER BY id LIMIT ?"

// HistoryPage answers at most limit request rows whose id sorts after afterID, in id
// order; limit is held to 1..1000.
func (r *HistoryReader) HistoryPage(ctx context.Context, afterID string, limit int) ([]HistoryRow, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := r.pool.QueryContext(ctx, historyPageQuery, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("read the history requests after %s: %w", afterID, err)
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		found, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("read the history requests after %s: %w", afterID, err)
		}
		row, err := historyRowOf(found)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the history requests after %s: %w", afterID, err)
	}
	return out, nil
}

// historyRowOf converts one requests row from the driver's map shape into a HistoryRow;
// this is the only place the untyped map is touched.
func historyRowOf(found map[string]any) (HistoryRow, error) {
	row := HistoryRow{
		ID:            textColumn(found, "id"),
		NodeID:        textColumn(found, "node_id"),
		SessionID:     textColumn(found, "session_id"),
		AgentID:       textColumn(found, "agent_id"),
		Model:         textColumn(found, "model"),
		Upstream:      textColumn(found, "upstream"),
		Ts:            intColumn(found, "ts"),
		LatencyMs:     intColumn(found, "latency_ms"),
		TtfbMs:        intPtrColumn(found, "ttfb_ms"),
		TtftMs:        intPtrColumn(found, "ttft_ms"),
		Input:         intColumn(found, "input"),
		Output:        intColumn(found, "output"),
		CacheRead:     intColumn(found, "cache_read"),
		CacheWrite:    intColumn(found, "cache_write"),
		CacheWrite1h:  intPtrColumn(found, "cache_write_1h"),
		Speed:         textPtrColumn(found, "speed"),
		Geo:           textPtrColumn(found, "geo"),
		ServiceTier:   textPtrColumn(found, "service_tier"),
		StopReason:    textColumn(found, "stop_reason"),
		Error:         textColumn(found, "error"),
		ParentAgentID: textColumn(found, "parent_agent_id"),
		Fallback:      textColumn(found, "fallback"),
		Provider:      textColumn(found, "provider"),
		Route:         textColumn(found, "route"),
		Status:        textColumn(found, "status"),
		Harness:       textColumn(found, "harness"),
		Via:           textPtrColumn(found, "via"),
		CostUSD:       floatPtrColumn(found, "cost_usd"),
		What:          textPtrColumn(found, "what"),
		Thinking:      intPtrColumn(found, "thinking"),
		PromptHash:    textColumn(found, "prompt_hash"),
		Totals:        intColumn(found, "totals") != 0,
	}
	headers, err := headerObject(found["headers"])
	if err != nil {
		return HistoryRow{}, fmt.Errorf("read the history row %s: %w", row.ID, err)
	}
	row.Headers = headers
	return row, nil
}

// headerObject answers the header pairs the column's JSON object holds: nil for NULL and
// "", an error for a value that is not an object of strings.
func headerObject(value any) (map[string]string, error) {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(text), &headers); err != nil {
		return nil, err
	}
	return headers, nil
}
