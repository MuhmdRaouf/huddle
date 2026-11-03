package ledger

import (
	"context"
	"fmt"
)

// The history reader's window reads for the insights report: ids, names, counts and times, and
// none of the text history.db keeps beside them.

// NodeFact is a session's main node, or one of its subagents' nodes.
type NodeFact struct {
	SessionID string
	Kind      string // "main" or "subagent"
	Repo      string // the repo root, else the working dir; "" when neither is known
	StartedAt int64
	EndedAt   int64 // 0 while the node has not ended
}

// ToolFact is one tool call.
type ToolFact struct {
	Session, Name, InputKey string // InputKey is a hash of the call, "" when unknown
	StartedAt, DurationMs   int64  // DurationMs is 0 when unknown
	OK                      bool
}

// CompactionFact is one context compaction.
type CompactionFact struct {
	Session string
	At      int64
}

// sessionNodesQuery reads the main nodes alive at any moment of the window and the subagent
// nodes that started inside it. An empty session matches every session.
const sessionNodesQuery = "SELECT session_id, kind, COALESCE(NULLIF(repo, ''), NULLIF(cwd, ''), ''), " +
	"started_at, COALESCE(ended_at, 0) FROM nodes WHERE started_at IS NOT NULL AND started_at <= ? " +
	"AND ((kind = 'main' AND (ended_at IS NULL OR ended_at >= ?)) OR (kind = 'subagent' AND started_at >= ?)) " +
	"AND (? = '' OR session_id = ?) ORDER BY kind, started_at, id"

const toolFactsQuery = "SELECT session_id, name, COALESCE(input_key, ''), started_at, COALESCE(duration_ms, 0), ok " +
	"FROM tools WHERE started_at >= ? AND started_at <= ? AND (? = '' OR session_id = ?) ORDER BY started_at, id"

const compactionsQuery = "SELECT session_id, ts FROM events WHERE kind = 'PreCompact' AND session_id IS NOT NULL " +
	"AND ts >= ? AND ts <= ? AND (? = '' OR session_id = ?) ORDER BY ts, seq"

// SessionNodes answers the session and subagent nodes of a window (ms since the epoch, both
// ends included), a session narrowing it to that session.
func (r *HistoryReader) SessionNodes(ctx context.Context, fromMs, toMs int64, session string) ([]NodeFact, error) {
	rows, err := r.pool.QueryContext(ctx, sessionNodesQuery, toMs, fromMs, fromMs, session, session)
	if err != nil {
		return nil, fmt.Errorf("read the history's nodes: %w", err)
	}
	defer rows.Close()
	var out []NodeFact
	for rows.Next() {
		var f NodeFact
		if err := rows.Scan(&f.SessionID, &f.Kind, &f.Repo, &f.StartedAt, &f.EndedAt); err != nil {
			return nil, fmt.Errorf("read the history's nodes: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the history's nodes: %w", err)
	}
	return out, nil
}

// ToolFacts answers every tool call that started inside the window, oldest first, with no page
// cap.
func (r *HistoryReader) ToolFacts(ctx context.Context, fromMs, toMs int64, session string) ([]ToolFact, error) {
	rows, err := r.pool.QueryContext(ctx, toolFactsQuery, fromMs, toMs, session, session)
	if err != nil {
		return nil, fmt.Errorf("read the history's tool calls: %w", err)
	}
	defer rows.Close()
	var out []ToolFact
	for rows.Next() {
		var f ToolFact
		var ok int64
		if err := rows.Scan(&f.Session, &f.Name, &f.InputKey, &f.StartedAt, &f.DurationMs, &ok); err != nil {
			return nil, fmt.Errorf("read the history's tool calls: %w", err)
		}
		f.OK = ok != 0
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the history's tool calls: %w", err)
	}
	return out, nil
}

// Compactions answers the PreCompact events inside the window, oldest first.
func (r *HistoryReader) Compactions(ctx context.Context, fromMs, toMs int64, session string) ([]CompactionFact, error) {
	rows, err := r.pool.QueryContext(ctx, compactionsQuery, fromMs, toMs, session, session)
	if err != nil {
		return nil, fmt.Errorf("read the history's compactions: %w", err)
	}
	defer rows.Close()
	var out []CompactionFact
	for rows.Next() {
		var f CompactionFact
		if err := rows.Scan(&f.Session, &f.At); err != nil {
			return nil, fmt.Errorf("read the history's compactions: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the history's compactions: %w", err)
	}
	return out, nil
}
