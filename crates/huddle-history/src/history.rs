use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;

use rusqlite::{Connection, OpenFlags, params};

use crate::error::HistoryError;
use crate::row::{Credit, Row};
use crate::schema;
use huddle_config::files::DIR_MODE;

const DAY_MS: u64 = 86_400_000;

/// The statement `missing_usage` runs: distinct upstream ids of rows since ?1 whose tokens are
/// still unknown. Pinned to the time index: left alone the planner takes the whole upstream_id
/// index to get the DISTINCT and the ORDER BY for free, so the 60 s fill reads every row of
/// history instead of only its window. Exposed as SQL so a test can hold the query plan to its
/// promise.
pub fn missing_usage_sql() -> String {
    format!(
        "SELECT DISTINCT upstream_id FROM requests INDEXED BY requests_at_ms
         WHERE upstream_id IS NOT NULL AND at_ms >= ?1 AND {UNKNOWN_USAGE}
         ORDER BY upstream_id"
    )
}

/// SQL for "no token count is known"; a row in this state waits for its credit.
pub(crate) const UNKNOWN_USAGE: &str =
    "tokens_in IS NULL AND cache_read IS NULL AND cache_write IS NULL AND tokens_out IS NULL";

/// The page cache of a read connection, in KiB: a scan streams its rows once, so a big cache only
/// adds resident memory per concurrent call.
const READ_CACHE_KIB: i64 = 512;

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
        // The WAL shrinks back to this cap after a checkpoint instead of keeping its
        // high-water mark on disk.
        conn.pragma_update(None, "journal_size_limit", 4_194_304)?;
        schema::migrate(&conn)?;
        Ok(History { conn })
    }

    /// Opens the file for writing only when it exists: no folder, file or schema is created, and
    /// a missing file is an error.
    pub fn open_existing(path: &Path) -> Result<History, HistoryError> {
        let conn = Connection::open_with_flags(
            path,
            OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_NO_MUTEX,
        )?;
        conn.busy_timeout(std::time::Duration::from_secs(5))?;
        Ok(History { conn })
    }

    /// Opens an existing file for reading only. Nothing is created, migrated or changed, and a
    /// read never waits for the writer: the file is in WAL mode, so readers and the writer run
    /// side by side.
    pub fn open_read(path: &Path) -> Result<History, HistoryError> {
        let conn = Connection::open_with_flags(
            path,
            OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
        )?;
        conn.busy_timeout(std::time::Duration::from_secs(5))?;
        conn.pragma_update(None, "cache_size", -READ_CACHE_KIB)?;
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

    /// Marks the rows since `since_ms` that ended in an error answer and have unknown tokens as
    /// having none: an error answer is not billed, so no log will ever credit it. A client that
    /// left (499) stays unknown, since its answer may have been billed. Returns how many changed.
    pub fn settle_failed(&self, since_ms: u64) -> Result<usize, HistoryError> {
        Ok(self.conn.execute(
            &format!(
                "UPDATE requests SET tokens_in = 0, cache_read = 0, cache_write = 0, tokens_out = 0
                 WHERE at_ms >= ?1 AND (status = 0 OR status >= 400) AND status != 499
                   AND {UNKNOWN_USAGE}"
            ),
            [to_db(since_ms)],
        )?)
    }

    /// Upstream ids of rows since `since_ms` whose tokens are still unknown.
    pub fn missing_usage(&self, since_ms: u64) -> Result<Vec<String>, HistoryError> {
        let mut stmt = self.conn.prepare(&missing_usage_sql())?;
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

#[cfg(test)]
mod tests {
    use super::*;

    use huddle_testkit::temp_home::TempHome;

    const WAL_CAP: u64 = 4_194_304;

    /// Rows with fat session and project strings, so one transaction pushes the WAL
    /// past the 4 MiB cap and past the 1000-page auto-checkpoint.
    fn fat_rows(count: u64) -> Vec<Row> {
        (0..count)
            .map(|i| Row {
                at_ms: 1_000 + i,
                session: Some(format!("sess-{i:06}-{}", "x".repeat(150))),
                project: Some(format!("proj-{i:06}-{}", "y".repeat(120))),
                model: "glm-5.3".to_string(),
                lane: "zai".to_string(),
                status: 200,
                dur_ms: 100,
                ..Row::default()
            })
            .collect()
    }

    #[test]
    fn l22_the_pragma_reads_back_4194304() {
        let home = TempHome::new();
        let history = History::open(&home.root().join("history/huddle.db")).expect("open");
        let limit: i64 = history
            .conn
            .query_row("PRAGMA journal_size_limit", [], |r| r.get(0))
            .expect("pragma");
        assert_eq!(limit, WAL_CAP as i64);
    }

    #[test]
    fn l22_the_wal_file_shrinks_to_the_cap_after_a_checkpoint() {
        let home = TempHome::new();
        let path = home.root().join("history/huddle.db");
        let mut history = History::open(&path).expect("open");
        let wal = path.with_file_name("huddle.db-wal");

        history.insert(&fat_rows(50_000)).expect("big insert");
        let grown = fs::metadata(&wal).expect("wal").len();
        assert!(
            grown > WAL_CAP,
            "precondition: the WAL grew past the cap, was {grown}"
        );

        history
            .insert(&[Row {
                at_ms: 2_000,
                model: "glm-5.3".to_string(),
                lane: "zai".to_string(),
                status: 200,
                dur_ms: 100,
                ..Row::default()
            }])
            .expect("one more write");

        let shrunk = fs::metadata(&wal).expect("wal").len();
        assert!(
            shrunk <= WAL_CAP,
            "the WAL is at most 4 MiB after the checkpoint, was {shrunk}"
        );
    }
}
