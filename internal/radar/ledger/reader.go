package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// Reader reads the ledger and never writes it: its own pool on the same file, with query_only set on
// every connection, so a page read never takes the writer's lock and never creates a missing file.
type Reader struct{ pool *sql.DB }

// OpenReader opens an existing ledger file for reading; a missing file is an error.
func OpenReader(file string) (*Reader, error) {
	// mode=rw opens an existing file and never creates one; query_only refuses every write
	// on the connection, busy_timeout waits out the writer instead of failing a read.
	pool, err := sql.Open("sqlite", "file:"+file+"?mode=rw&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open the ledger reader for %s: %w", file, err)
	}
	// the reader shares the file with the writer, so several reads run at once
	pool.SetMaxOpenConns(4)
	// the ping opens the first connection, so a missing or unusable file fails here, on
	// open, not on the first query
	if err := pool.Ping(); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("open the ledger reader for %s: %w", file, err)
	}
	return &Reader{pool: pool}, nil
}

// Close closes the reader's pool.
func (r *Reader) Close() error { return r.pool.Close() }

// sessionPageQuery answers one session's requests newest first; a page binds started_at to
// the last row's started_at.
const sessionPageQuery = "SELECT * FROM ledger_requests " +
	"WHERE session_id = ? AND started_at < ? ORDER BY started_at DESC, id DESC LIMIT ?"

// RequestsOfSession answers one session's requests started before beforeMs (0 means no bound), newest
// first, at most limit of them; limit is held to 1..1000.
func (r *Reader) RequestsOfSession(ctx context.Context, sessionID string, beforeMs int64, limit int) ([]Row, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	if beforeMs <= 0 {
		beforeMs = math.MaxInt64
	}
	rows, err := r.pool.QueryContext(ctx, sessionPageQuery, sessionID, beforeMs, limit)
	if err != nil {
		return nil, fmt.Errorf("read the session %s requests: %w", sessionID, err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		found, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("read the session %s requests: %w", sessionID, err)
		}
		out = append(out, rowOf(found))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the session %s requests: %w", sessionID, err)
	}
	return out, nil
}
