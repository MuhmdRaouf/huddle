package ledger

import (
	"fmt"
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

// A session's requests in time order: the reader pages them newest first.
const ledgerRequestsSessionIndex = `
CREATE INDEX IF NOT EXISTS ledger_requests_session
ON ledger_requests(session_id, started_at)`

// What each source filed for one request, as JSON blobs, empty when that source has not spoken
// yet. A later source of the same kind replaces the earlier one; a different kind joins it.
const ledgerFactsDDL = `
CREATE TABLE IF NOT EXISTS ledger_facts (
  id TEXT PRIMARY KEY,
  route TEXT NOT NULL DEFAULT '',
  transcript TEXT NOT NULL DEFAULT ''
)`

// schemaSteps are the migrations after version 3, one per version: index 0 takes a file to
// version 4. A step does schema work only (ALTER, CREATE, an index); rows are rebuilt by
// the backfill.
var schemaSteps = [...]string{
	// 4: the version of the rules each row was built with, and the ledger's own key/value table
	"ALTER TABLE ledger_requests ADD COLUMN reconciled INTEGER NOT NULL DEFAULT 0;" +
		"CREATE TABLE IF NOT EXISTS ledger_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
	// 5: the billing columns history carries (ADR-5)
	"ALTER TABLE ledger_requests ADD COLUMN cache_write_1h INTEGER;" +
		"ALTER TABLE ledger_requests ADD COLUMN speed TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN geo TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN service_tier TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN provider TEXT NOT NULL DEFAULT '';" +
		"ALTER TABLE ledger_requests ADD COLUMN via TEXT",
	// 6: the capture columns history carries (ADR-5)
	"ALTER TABLE ledger_requests ADD COLUMN what TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN thinking INTEGER;" +
		"ALTER TABLE ledger_requests ADD COLUMN headers TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN totals INTEGER NOT NULL DEFAULT 0",
	// 7: the node a request lands on (ADR-5)
	"ALTER TABLE ledger_requests ADD COLUMN node_id TEXT NOT NULL DEFAULT ''",
	// 8: two money columns, one meaning each (ADR-5)
	"ALTER TABLE ledger_requests RENAME COLUMN cost_usd TO price_usd;" +
		"ALTER TABLE ledger_requests RENAME COLUMN cost_reported_usd TO harness_cost_usd",
}

// SchemaVersion is the version a file sits at once Open has applied every step.
const SchemaVersion = 3 + len(schemaSteps)

// applySchema creates the tables and their indexes on a newly opened file, then raises the
// file through every step it is behind. The base statements are IF NOT EXISTS and each step
// runs once per version, so a file at an older version gains whatever it is missing on its
// next Open.
func applySchema(db DB) error {
	if err := db.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL + ";" +
		ledgerRequestsSessionIndex); err != nil {
		return fmt.Errorf("create the ledger schema: %w", err)
	}
	found, err := db.Get("PRAGMA user_version")
	if err != nil {
		return fmt.Errorf("read the ledger schema's version: %w", err)
	}
	version := int(intColumn(found, "user_version"))
	if version < 3 {
		if err := db.Exec("PRAGMA user_version = 3"); err != nil {
			return fmt.Errorf("raise the ledger schema's version to 3: %w", err)
		}
		version = 3
	}
	for at, step := range schemaSteps {
		to := 3 + at + 1
		if version >= to {
			continue
		}
		// the step and its version go in together: a half-applied step never stands
		if err := db.Tx(func(tx DB) error {
			if err := tx.Exec(step); err != nil {
				return err
			}
			return tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", to))
		}); err != nil {
			return fmt.Errorf("raise the ledger schema's version to %d: %w", to, err)
		}
	}
	return nil
}
