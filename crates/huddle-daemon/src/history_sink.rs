//! The router's history sink: finished requests go to the history writer thread and applied
//! credits to a second thread that fills the database. Neither call blocks the caller.

use std::path::Path;
use std::sync::mpsc::{Receiver, SyncSender, sync_channel};
use std::thread::{self, JoinHandle};

use huddle_history::{Credit, History, Row, Writer};
use huddle_router::live::HistorySink;

/// How many credit batches wait for the filler before `credited` drops one. A dropped batch is
/// credited again by the next snapshot or by `fill_missing`.
const CREDIT_QUEUE: usize = 64;

pub struct HistoryBridge {
    writer: Writer,
    credits: Option<SyncSender<Vec<Credit>>>,
    filler: Option<JoinHandle<()>>,
}

impl HistoryBridge {
    /// Starts both threads on the database at `path`; each opens its own connection.
    pub fn start(path: &Path) -> HistoryBridge {
        let (credits, queue) = sync_channel(CREDIT_QUEUE);
        let db = path.to_path_buf();
        let filler = thread::Builder::new()
            .name("history-fill".into())
            .spawn(move || fill_loop(&db, &queue))
            .ok();
        HistoryBridge {
            writer: Writer::spawn(path),
            credits: Some(credits),
            filler,
        }
    }
}

impl HistorySink for HistoryBridge {
    fn finished(&self, row: Row) {
        self.writer.send(row);
    }

    fn credited(&self, credits: Vec<Credit>) {
        let Some(queue) = &self.credits else {
            return;
        };
        if queue.try_send(credits).is_err() {
            eprintln!("history: credits dropped");
        }
    }
}

impl Drop for HistoryBridge {
    fn drop(&mut self) {
        self.credits = None;
        if let Some(filler) = self.filler.take() {
            let _ = filler.join();
        }
    }
}

fn fill_loop(path: &Path, queue: &Receiver<Vec<Credit>>) {
    let Ok(mut history) = History::open(path) else {
        return;
    };
    for credits in queue {
        if let Err(error) = history.fill(&credits) {
            eprintln!("history: fill: {error}");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_history::{Filter, Page, Range};
    use huddle_testkit::temp_home::TempHome;
    use std::time::{Duration, Instant};

    fn row(upstream: &str) -> Row {
        Row {
            at_ms: 1_000,
            model: "glm-5.3".into(),
            lane: "zai".into(),
            status: 200,
            dur_ms: 5,
            upstream_id: Some(upstream.into()),
            ..Row::default()
        }
    }

    fn all(history: &History) -> Vec<Row> {
        let filter = Filter::new(Range {
            from_ms: 0,
            to_ms: u64::MAX / 2,
        });
        history
            .search(
                filter,
                Page {
                    limit: 500,
                    offset: 0,
                },
            )
            .expect("search")
            .rows
    }

    #[test]
    fn t5_finished_rows_and_credits_reach_the_database() {
        let home = TempHome::new();
        let db = home.root().join("huddle.db");
        let reader = History::open(&db).expect("open");
        {
            let bridge = HistoryBridge::start(&db);
            bridge.finished(row("req_1"));
            // Dropping flushes the writer, so the credit lands on a row that exists.
            drop(bridge);
        }
        {
            let bridge = HistoryBridge::start(&db);
            bridge.credited(vec![Credit {
                upstream_id: "req_1".into(),
                agent: Some("s1:main".into()),
                project: Some("huddle".into()),
                usage: (1, 2, 3, 4),
                stop: Some("end_turn".into()),
            }]);
            drop(bridge);
        }
        let rows = all(&reader);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].tokens_out, Some(4));
        assert_eq!(rows[0].agent.as_deref(), Some("s1:main"));
        assert_eq!(rows[0].project.as_deref(), Some("huddle"));
    }

    #[test]
    fn t5_a_stalled_database_never_delays_a_thousand_finished_requests() {
        let home = TempHome::new();
        let db = home.root().join("huddle.db");
        let blocker = rusqlite::Connection::open(&db).expect("open");
        blocker
            .execute_batch("PRAGMA journal_mode=WAL; BEGIN EXCLUSIVE;")
            .expect("lock");
        let bridge = HistoryBridge::start(&db);

        let started = Instant::now();
        for n in 0..1000 {
            bridge.finished(row(&format!("req_{n}")));
        }
        bridge.credited(Vec::new());
        let took = started.elapsed();

        assert!(took < Duration::from_millis(250), "{took:?}");
        drop(blocker);
        drop(bridge);
    }
}
