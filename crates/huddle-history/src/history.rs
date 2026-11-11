use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use rusqlite::{Connection, params};

use crate::error::HistoryError;
use crate::row::{Credit, Row};
use crate::schema;
use huddle_config::files::DIR_MODE;

const DAY_MS: u64 = 86_400_000;

/// SQL for "no token count is known"; a row in this state waits for its credit.
pub(crate) const UNKNOWN_USAGE: &str =
    "tokens_in IS NULL AND cache_read IS NULL AND cache_write IS NULL AND tokens_out IS NULL";

/// The request history in one SQLite file.
pub struct History {
    pub(crate) conn: Connection,
}

pub(crate) fn to_db(value: u64) -> i64 {
    i64::try_from(value).unwrap_or(i64::MAX)
}

pub(crate) fn from_db(value: i64) -> u64 {
    u64::try_from(value).unwrap_or(0)
}

impl History {
    /// Opens the file, creating its folder, the file (mode 0644) and the schema when missing.
    pub fn open(path: &Path) -> Result<History, HistoryError> {
        if let Some(parent) = path.parent() {
            huddle_config::files::create_dir(parent, DIR_MODE)?;
        }
        let conn = Connection::open(path)?;
        fs::set_permissions(
            path,
            fs::Permissions::from_mode(huddle_config::files::FILE_MODE),
        )?;
        conn.busy_timeout(std::time::Duration::from_secs(5))?;
        conn.pragma_update_and_check(None, "journal_mode", "WAL", |_| Ok(()))?;
        schema::migrate(&conn)?;
        Ok(History { conn })
    }

    /// Inserts the rows in one transaction.
    pub fn insert(&mut self, rows: &[Row]) -> Result<(), HistoryError> {
        let tx = self.conn.transaction()?;
        {
            let mut stmt = tx.prepare(
                "INSERT INTO requests (at_ms, session, project, agent, model, lane, status, reason,
                    ttft_ms, dur_ms, tokens_in, cache_read, cache_write, tokens_out, stop, upstream_id)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16)",
            )?;
            for row in rows {
                stmt.execute(params![
                    to_db(row.at_ms),
                    row.session,
                    row.project,
                    row.agent,
                    row.model,
                    row.lane,
                    row.status,
                    row.reason,
                    row.ttft_ms.map(to_db),
                    to_db(row.dur_ms),
                    row.tokens_in.map(to_db),
                    row.cache_read.map(to_db),
                    row.cache_write.map(to_db),
                    row.tokens_out.map(to_db),
                    row.stop,
                    row.upstream_id,
                ])?;
            }
        }
        tx.commit()?;
        Ok(())
    }

    /// Sets tokens and stop on rows whose tokens are unknown, and agent and project where they
    /// are null. Rows with known tokens keep them. Returns how many rows changed.
    pub fn fill(&mut self, credits: &[Credit]) -> Result<usize, HistoryError> {
        let tx = self.conn.transaction()?;
        let mut changed = 0;
        {
            let mut stmt = tx.prepare(&format!(
                "UPDATE requests SET
                    tokens_in = CASE WHEN {UNKNOWN_USAGE} THEN ?1 ELSE tokens_in END,
                    cache_read = CASE WHEN {UNKNOWN_USAGE} THEN ?2 ELSE cache_read END,
                    cache_write = CASE WHEN {UNKNOWN_USAGE} THEN ?3 ELSE cache_write END,
                    tokens_out = CASE WHEN {UNKNOWN_USAGE} THEN ?4 ELSE tokens_out END,
                    stop = CASE WHEN {UNKNOWN_USAGE} THEN ?5 ELSE stop END,
                    agent = COALESCE(agent, ?6),
                    project = COALESCE(project, ?7)
                 WHERE upstream_id = ?8
                   AND ({UNKNOWN_USAGE}
                        OR (agent IS NULL AND ?6 IS NOT NULL)
                        OR (project IS NULL AND ?7 IS NOT NULL))"
            ))?;
            for credit in credits {
                let (input, cache_read, cache_write, output) = credit.usage;
                changed += stmt.execute(params![
                    to_db(input),
                    to_db(cache_read),
                    to_db(cache_write),
                    to_db(output),
                    credit.stop,
                    credit.agent,
                    credit.project,
                    credit.upstream_id,
                ])?;
            }
        }
        tx.commit()?;
        Ok(changed)
    }

    /// Upstream ids of rows since `since_ms` whose tokens are still unknown.
    pub fn missing_usage(&self, since_ms: u64) -> Result<Vec<String>, HistoryError> {
        let mut stmt = self.conn.prepare(&format!(
            "SELECT DISTINCT upstream_id FROM requests
             WHERE upstream_id IS NOT NULL AND at_ms >= ?1 AND {UNKNOWN_USAGE}
             ORDER BY upstream_id"
        ))?;
        let ids = stmt
            .query_map([to_db(since_ms)], |r| r.get(0))?
            .collect::<Result<Vec<String>, _>>()?;
        Ok(ids)
    }

    /// Deletes rows older than `keep_days` before `now_ms`. Returns how many went.
    pub fn prune(&self, keep_days: u64, now_ms: u64) -> Result<usize, HistoryError> {
        let cutoff = now_ms.saturating_sub(keep_days.saturating_mul(DAY_MS));
        Ok(self
            .conn
            .execute("DELETE FROM requests WHERE at_ms < ?1", [to_db(cutoff)])?)
    }
}
