package ledger

import (
	"fmt"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
)

// The ledger's schema: created in place with IF NOT EXISTS and versioned through the
// user_version pragma.

// One row per model request; every column answers one Row field, the pointers nullable.
const ledgerRequestsDDL = `
CREATE TABLE IF NOT EXISTS ledger_requests (
  id TEXT PRIMARY KEY,
  anthropic_id TEXT,
  started_at INTEGER,
  ttfb_ms INTEGER,
  ttft_ms INTEGER,
  duration_ms INTEGER,
  lane TEXT,
  harness TEXT,
  model TEXT,
  fallback_from TEXT,
  upstream TEXT,
  session_id TEXT,
  agent_id TEXT,
  parent_agent_id TEXT,
  prompt_id TEXT,
  turn_index INTEGER,
  prev_request TEXT,
  query_source TEXT,
  outcome TEXT,
  status INTEGER,
  error_code TEXT,
  stop_reason TEXT,
  input INTEGER,
  output INTEGER,
  cache_read INTEGER,
  cache_write INTEGER,
  usage_source TEXT,
  cost_usd REAL,
  cost_reported_usd REAL,
  ratelimit_5h REAL,
  ratelimit_7d REAL,
  prompt_hash TEXT,
  in_transcript INTEGER NOT NULL DEFAULT 0
)`

// Two rows may share an empty anthropic_id; a real one names one request.
const ledgerRequestsIndex = `
CREATE UNIQUE INDEX IF NOT EXISTS ledger_requests_anthropic_id
ON ledger_requests(anthropic_id) WHERE anthropic_id <> ''`

// applySchema creates the table and its index on a newly opened file.
func applySchema(db history.DB) error {
	if err := db.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";PRAGMA user_version = 1"); err != nil {
		return fmt.Errorf("create the ledger schema: %w", err)
	}
	return nil
}
