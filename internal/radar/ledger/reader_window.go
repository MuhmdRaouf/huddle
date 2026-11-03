package ledger

import (
	"context"
	"fmt"
)

// RequestFact is one request as the insights report reads it: ids, counts, the lane and the
// price, and no prompt, answer or captured line.
type RequestFact struct {
	Session, Model, Provider             string
	StartedAt, DurationMs, Status        int64
	Input, Output, CacheRead, CacheWrite int64
	CostUSD                              float64 // the cost the harness reported, else radar's price
}

// requestFactsQuery reads the requests that started inside a window, oldest first; an empty
// session matches every session. The provider falls back to the lane when none is recorded.
const requestFactsQuery = "SELECT COALESCE(session_id, ''), COALESCE(model, ''), " +
	"COALESCE(NULLIF(provider, ''), NULLIF(lane, ''), ''), started_at, COALESCE(duration_ms, 0), " +
	"COALESCE(status, 0), COALESCE(input, 0), COALESCE(output, 0), COALESCE(cache_read, 0), " +
	"COALESCE(cache_write, 0), COALESCE(harness_cost_usd, price_usd, 0) " +
	"FROM ledger_requests WHERE started_at >= ? AND started_at <= ? AND (? = '' OR session_id = ?) " +
	"ORDER BY started_at, id"

// RequestFacts answers every request that started from fromMs to toMs (ms since the epoch,
// both included), oldest first, with no page cap; a session narrows it to that session.
func (r *Reader) RequestFacts(ctx context.Context, fromMs, toMs int64, session string) ([]RequestFact, error) {
	rows, err := r.pool.QueryContext(ctx, requestFactsQuery, fromMs, toMs, session, session)
	if err != nil {
		return nil, fmt.Errorf("read the ledger's requests: %w", err)
	}
	defer rows.Close()
	var out []RequestFact
	for rows.Next() {
		var f RequestFact
		if err := rows.Scan(&f.Session, &f.Model, &f.Provider, &f.StartedAt, &f.DurationMs, &f.Status,
			&f.Input, &f.Output, &f.CacheRead, &f.CacheWrite, &f.CostUSD); err != nil {
			return nil, fmt.Errorf("read the ledger's requests: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the ledger's requests: %w", err)
	}
	return out, nil
}
