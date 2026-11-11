use rusqlite::Connection;

use crate::error::HistoryError;

pub const VERSION: i64 = 1;

/// The columns of the `requests` table, in order. Each is a field of `Row` plus the row id.
pub const COLUMNS: [&str; 17] = [
    "id",
    "at_ms",
    "session",
    "project",
    "agent",
    "model",
    "lane",
    "status",
    "reason",
    "ttft_ms",
    "dur_ms",
    "tokens_in",
    "cache_read",
    "cache_write",
    "tokens_out",
    "stop",
    "upstream_id",
];

const CREATE_V1: &str = "
CREATE TABLE requests (
    id INTEGER PRIMARY KEY,
    at_ms INTEGER NOT NULL,
    session TEXT,
    project TEXT,
    agent TEXT,
    model TEXT NOT NULL,
    lane TEXT NOT NULL,
    status INTEGER NOT NULL,
    reason TEXT,
    ttft_ms INTEGER,
    dur_ms INTEGER NOT NULL,
    tokens_in INTEGER,
    cache_read INTEGER,
    cache_write INTEGER,
    tokens_out INTEGER,
    stop TEXT,
    upstream_id TEXT
);
CREATE INDEX requests_at_ms ON requests (at_ms);
CREATE INDEX requests_upstream_id ON requests (upstream_id);
";

/// Creates the schema on a fresh file and refuses a file from a newer version.
pub fn migrate(conn: &Connection) -> Result<(), HistoryError> {
    let found: i64 = conn.query_row("PRAGMA user_version", [], |r| r.get(0))?;
    if found > VERSION {
        return Err(HistoryError::SchemaTooNew {
            found,
            supported: VERSION,
        });
    }
    if found < 1 {
        conn.execute_batch(CREATE_V1)?;
        conn.pragma_update(None, "user_version", VERSION)?;
    }
    Ok(())
}
