//! Keeping `huddle.db` current: pruning old rows by `[history] keep_days` and filling rows whose
//! usage the router could not read from the session logs.

use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;

use huddle_history::{Credit, History, HistoryError};
use huddle_sessions::usage_for;

/// Rows are kept this many days when `config.toml` does not say.
pub const DEFAULT_KEEP_DAYS: u64 = 90;

/// How far back the first fill after a start looks up rows with unknown usage in the session logs.
pub const FIRST_FILL_WINDOW_MS: u64 = 30 * 86_400_000;

/// How far back every later fill looks. A log changes right after its request, so a day is
/// enough, and rows no log will ever credit (other providers') stay cheap to skip.
pub const FILL_WINDOW_MS: u64 = 86_400_000;

/// `[history] keep_days` of the config file; the default when the file, the table or the key
/// is missing or not a positive integer.
pub fn keep_days(config_path: &Path) -> u64 {
    std::fs::read_to_string(config_path)
        .ok()
        .and_then(|text| text.parse::<toml::Table>().ok())
        .and_then(|table| table.get("history")?.get("keep_days")?.as_integer())
        .and_then(|days| u64::try_from(days).ok())
        .filter(|days| *days > 0)
        .unwrap_or(DEFAULT_KEEP_DAYS)
}

/// Deletes the rows older than the configured keep time. Returns how many went.
pub fn prune(db_path: &Path, config_path: &Path, now_ms: u64) -> Result<usize, HistoryError> {
    History::open(db_path)?.prune(keep_days(config_path), now_ms)
}

/// Fills the rows of the last `window_ms` whose usage is unknown from the session logs under
/// `claude_dir`. Returns how many rows changed.
pub fn fill_missing(
    history: &mut History,
    claude_dir: &Path,
    now_ms: u64,
    window_ms: u64,
) -> Result<usize, HistoryError> {
    let since = now_ms.saturating_sub(window_ms);
    let ids = history.missing_usage(since)?;
    let credits: Vec<Credit> = usage_for(claude_dir, &ids, since)
        .into_iter()
        .map(|found| Credit {
            agent: Some(format!("{}:{}", found.session, found.agent_id)),
            project: found.project,
            usage: (
                found.usage.input + found.usage.cache_read + found.usage.cache_write,
                found.usage.cache_read,
                found.usage.cache_write,
                found.usage.output,
            ),
            stop: found.stop,
            upstream_id: found.request_id,
        })
        .collect();
    if credits.is_empty() {
        return Ok(0);
    }
    history.fill(&credits)
}

/// One fill of an existing database: none when there is no database (a fill never creates one).
/// Runs off the request path, on its own write connection.
pub fn fill(
    db_path: &Path,
    claude_dir: &Path,
    now_ms: u64,
    window_ms: u64,
) -> Result<usize, HistoryError> {
    if !db_path.exists() {
        return Ok(0);
    }
    let mut history = History::open(db_path)?;
    fill_missing(&mut history, claude_dir, now_ms, window_ms)
}

/// Opens an existing database for a history request, read-only. None when there is no database
/// (a read never creates one) or it cannot be opened. A request reads what the rows hold and
/// never scans the session logs: the background fill credits usage.
pub fn open_for_read(db_path: &Path) -> Option<History> {
    if !db_path.exists() {
        return None;
    }
    History::open_read(db_path).ok()
}

/// Runs fills on a thread of their own, one at a time. A caller never waits for a fill, so the
/// serve loop keeps answering stop, handover and reload while one reads 30 days of logs; the
/// thread is not joined, so ending the process does not wait for it either.
#[derive(Clone, Default)]
pub struct FillGate {
    running: Arc<AtomicBool>,
}

impl FillGate {
    /// Starts `work` on its own thread. False, with nothing started, while a fill still runs.
    pub fn start(&self, work: impl FnOnce() + Send + 'static) -> bool {
        if self.running.swap(true, Ordering::AcqRel) {
            return false;
        }
        let running = Arc::clone(&self.running);
        let started = thread::Builder::new()
            .name("huddle-history-fill".into())
            .spawn(move || {
                let _release = Release(running);
                work();
            });
        if started.is_err() {
            self.running.store(false, Ordering::Release);
        }
        started.is_ok()
    }
}

/// Clears the running flag when the fill ends, also by a panic.
struct Release(Arc<AtomicBool>);

impl Drop for Release {
    fn drop(&mut self) {
        self.0.store(false, Ordering::Release);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_history::{Filter, Page, Range, Row};
    use huddle_testkit::temp_home::TempHome;
    use std::time::Instant;

    const NOW: u64 = 1_800_000_000_000;
    const DAY: u64 = 86_400_000;

    fn row(at_ms: u64, upstream: Option<&str>) -> Row {
        Row {
            at_ms,
            model: "claude-opus-5-5".into(),
            lane: "anthropic".into(),
            status: 200,
            dur_ms: 10,
            upstream_id: upstream.map(str::to_string),
            ..Row::default()
        }
    }

    fn everything() -> Filter {
        Filter::new(Range {
            from_ms: 0,
            to_ms: NOW + DAY,
        })
    }

    fn rows_of(history: &History) -> Vec<Row> {
        history
            .search(
                everything(),
                Page {
                    limit: 100,
                    offset: 0,
                },
            )
            .expect("search")
            .rows
    }

    #[test]
    fn t5_keep_days_reads_config_toml_and_falls_back_to_90() {
        let home = TempHome::new();
        let path = home.root().join("config.toml");
        assert_eq!(keep_days(&path), 90);
        std::fs::write(&path, "[history]\nkeep_days = 7\n").expect("write");
        assert_eq!(keep_days(&path), 7);
        std::fs::write(&path, "[history]\nkeep_days = \"x\"\n").expect("write");
        assert_eq!(keep_days(&path), 90);
        std::fs::write(&path, "not toml [").expect("write");
        assert_eq!(keep_days(&path), 90);
    }

    #[test]
    fn t5_prune_runs_with_keep_days_from_config_toml() {
        let home = TempHome::new();
        let db = home.root().join("huddle.db");
        let config = home.root().join("config.toml");
        std::fs::write(&config, "[history]\nkeep_days = 7\n").expect("write");
        let mut history = History::open(&db).expect("open");
        history
            .insert(&[row(NOW - 8 * DAY, None), row(NOW - 6 * DAY, None)])
            .expect("insert");

        assert_eq!(prune(&db, &config, NOW).expect("prune"), 1);

        assert_eq!(rows_of(&history).len(), 1);
    }

    #[test]
    fn t5_fill_missing_fills_a_row_from_a_fixture_log() {
        let home = TempHome::new();
        let log = home.root().join("projects/-w-huddle/s1.jsonl");
        std::fs::create_dir_all(log.parent().expect("parent")).expect("mkdir");
        std::fs::write(
            &log,
            "{\"type\":\"assistant\",\"requestId\":\"req_1\",\"cwd\":\"/w/huddle\",\"message\":{\"model\":\"m\",\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":3,\"output_tokens\":11}}}\n",
        )
        .expect("write log");
        std::fs::File::options()
            .write(true)
            .open(&log)
            .and_then(|file| {
                file.set_modified(
                    std::time::UNIX_EPOCH + std::time::Duration::from_millis(NOW - 3_600_000),
                )
            })
            .expect("set mtime");
        let mut history = History::open(&home.root().join("huddle.db")).expect("open");
        history
            .insert(&[row(NOW - DAY, Some("req_1")), row(NOW - DAY, Some("req_2"))])
            .expect("insert");

        let changed = fill_missing(&mut history, home.root(), NOW, FILL_WINDOW_MS).expect("fill");

        assert_eq!(changed, 1);
        let rows = rows_of(&history);
        let filled = rows
            .iter()
            .find(|r| r.upstream_id.as_deref() == Some("req_1"))
            .expect("filled row");
        assert_eq!(filled.tokens_out, Some(11));
        assert_eq!(filled.tokens_in, Some(15));
        assert_eq!(filled.project.as_deref(), Some("huddle"));
        assert_eq!(filled.agent.as_deref(), Some("s1:main"));
        assert_eq!(filled.stop.as_deref(), Some("end_turn"));
        let other = rows
            .iter()
            .find(|r| r.upstream_id.as_deref() == Some("req_2"))
            .expect("other row");
        assert_eq!(other.tokens_out, None);
    }

    #[test]
    fn t6_open_for_read_answers_none_without_a_database_and_fill_credits_an_existing_one() {
        let home = TempHome::new();
        let db = home.root().join("huddle.db");
        assert!(open_for_read(&db).is_none());
        assert_eq!(
            fill(&db, home.root(), NOW, FILL_WINDOW_MS).expect("fill"),
            0
        );
        assert!(
            !db.exists(),
            "neither a read nor a fill creates the database"
        );

        History::open(&db)
            .expect("open")
            .insert(&[row(NOW - DAY / 2, Some("req_9"))])
            .expect("insert");
        write_log(home.root());

        assert_eq!(
            fill(&db, home.root(), NOW, FILL_WINDOW_MS).expect("fill"),
            1
        );

        let read = open_for_read(&db).expect("open");
        assert_eq!(rows_of(&read)[0].tokens_out, Some(2));
    }

    #[test]
    fn d1_a_history_read_never_scans_the_session_logs() {
        let home = TempHome::new();
        let db = home.root().join("huddle.db");
        History::open(&db)
            .expect("open")
            .insert(&[row(NOW - DAY / 2, Some("req_9"))])
            .expect("insert");
        write_log(home.root());

        let read = open_for_read(&db).expect("open");

        assert_eq!(
            rows_of(&read)[0].tokens_out,
            None,
            "a read leaves the usage to the background fill"
        );
    }

    /// A session log, changed an hour ago, that credits `req_9`.
    fn write_log(root: &Path) {
        let log = root.join("projects/-w-huddle/s9.jsonl");
        std::fs::create_dir_all(log.parent().expect("parent")).expect("mkdir");
        std::fs::write(
            &log,
            "{\"type\":\"assistant\",\"requestId\":\"req_9\",\"cwd\":\"/w/huddle\",\"message\":{\"model\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n",
        )
        .expect("write log");
        std::fs::File::options()
            .write(true)
            .open(&log)
            .and_then(|file| {
                file.set_modified(
                    std::time::UNIX_EPOCH + std::time::Duration::from_millis(NOW - 3_600_000),
                )
            })
            .expect("set mtime");
    }

    #[test]
    fn d1_a_slow_fill_never_delays_a_stop() {
        let gate = FillGate::default();
        let (release, hold) = std::sync::mpsc::channel::<()>();
        let begun = Instant::now();

        assert!(gate.start(move || {
            let _ = hold.recv_timeout(std::time::Duration::from_secs(10));
        }));
        assert!(
            begun.elapsed() < std::time::Duration::from_millis(500),
            "starting a fill returns at once"
        );
        assert!(!gate.start(|| {}), "a second fill waits for the first");

        release.send(()).expect("release");
        let deadline = Instant::now() + std::time::Duration::from_secs(5);
        while !gate.start(|| {}) {
            assert!(Instant::now() < deadline, "the gate reopens after the fill");
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
    }
}
