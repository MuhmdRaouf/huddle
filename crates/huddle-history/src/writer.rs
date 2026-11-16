use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, RecvTimeoutError, SyncSender, TrySendError, sync_channel};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use crate::history::History;
use crate::row::Row;

const QUEUE: usize = 4096;
const BATCH_ROWS: usize = 200;
const BATCH_WAIT: Duration = Duration::from_secs(1);

/// Writes rows from its own thread so the caller never waits on the disk.
pub struct Writer {
    tx: Option<SyncSender<Row>>,
    dropped: Arc<AtomicU64>,
    thread: Option<JoinHandle<()>>,
}

impl Writer {
    /// Starts the thread; it opens its own connection to `path`.
    pub fn spawn(path: &Path) -> Writer {
        let (tx, rx) = sync_channel(QUEUE);
        let path = path.to_path_buf();
        let dropped = Arc::new(AtomicU64::new(0));
        let lost = Arc::clone(&dropped);
        let thread = thread::Builder::new()
            .name("huddle-history-writer".into())
            .spawn(move || {
                if let Ok(mut history) = History::open(&path) {
                    run(&mut history, &rx, &lost, &|| {});
                }
            })
            .ok();
        Writer {
            tx: Some(tx),
            dropped,
            thread,
        }
    }

    /// Queues a row without blocking. A full queue drops the row and counts it.
    pub fn send(&self, row: Row) {
        let sent = match &self.tx {
            Some(tx) => !matches!(
                tx.try_send(row),
                Err(TrySendError::Full(_) | TrySendError::Disconnected(_))
            ),
            None => false,
        };
        if !sent {
            self.dropped.fetch_add(1, Ordering::Relaxed);
        }
    }

    /// How many rows were dropped, by a full queue or a failed write.
    pub fn dropped(&self) -> u64 {
        self.dropped.load(Ordering::Relaxed)
    }
}

impl Drop for Writer {
    /// Flushes what is queued and waits for the thread.
    fn drop(&mut self) {
        self.tx = None;
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

/// Writes rows until the queue closes. `turns` runs once per receive wake so tests can prove an
/// idle writer stays blocked.
fn run(history: &mut History, rx: &Receiver<Row>, lost: &AtomicU64, turns: &dyn Fn()) {
    let mut batch: Vec<Row> = Vec::with_capacity(BATCH_ROWS);
    loop {
        // An empty batch blocks until the first row arrives, so an idle writer never wakes; a
        // started batch waits at most BATCH_WAIT before it flushes.
        let open = match rx.recv() {
            Ok(first) => {
                batch.push(first);
                true
            }
            Err(_) => false,
        };
        turns();
        if !open {
            return;
        }
        let deadline = Instant::now() + BATCH_WAIT;
        while batch.len() < BATCH_ROWS {
            match rx.recv_timeout(deadline.saturating_duration_since(Instant::now())) {
                Ok(row) => batch.push(row),
                Err(RecvTimeoutError::Timeout | RecvTimeoutError::Disconnected) => break,
            }
        }
        if history.insert(&batch).is_err() {
            lost.fetch_add(batch.len() as u64, Ordering::Relaxed);
        }
        batch.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    use huddle_testkit::temp_home::TempHome;

    #[test]
    fn l21_an_idle_writer_does_not_wake() {
        let home = TempHome::new();
        let mut history = History::open(&home.root().join("history/huddle.db")).expect("open");
        let (tx, rx) = sync_channel(16);
        let turns = Arc::new(AtomicU64::new(0));
        let count = Arc::clone(&turns);
        let handle = thread::spawn(move || {
            let idle = move || {
                count.fetch_add(1, Ordering::Relaxed);
            };
            run(&mut history, &rx, &AtomicU64::new(0), &idle)
        });
        // A writer asleep in recv turns only on work; any turn inside this window is a wake
        // with nothing queued.
        thread::sleep(Duration::from_millis(2_500));
        assert_eq!(
            turns.load(Ordering::Relaxed),
            0,
            "the writer woke while it had nothing queued"
        );
        drop(tx);
        handle.join().expect("join");
        assert_eq!(
            turns.load(Ordering::Relaxed),
            1,
            "the disconnect ends the loop in exactly one turn"
        );
    }

    #[test]
    fn l21_a_single_row_is_flushed_within_one_batch_wait() {
        let home = TempHome::new();
        let path = home.root().join("history/huddle.db");
        let writer = Writer::spawn(&path);
        writer.send(Row {
            at_ms: 1_000,
            model: "glm-5.3".to_string(),
            lane: "zai".to_string(),
            status: 200,
            dur_ms: 100,
            ..Row::default()
        });
        let deadline = Instant::now() + BATCH_WAIT + Duration::from_millis(300);
        let mut stored = 0i64;
        while Instant::now() < deadline {
            let read = History::open_read(&path).ok().and_then(|history| {
                history
                    .conn
                    .query_row("SELECT COUNT(*) FROM requests", [], |r| r.get::<_, i64>(0))
                    .ok()
            });
            match read {
                Some(count) if count > 0 => {
                    stored = count;
                    break;
                }
                _ => thread::sleep(Duration::from_millis(20)),
            }
        }
        drop(writer);
        assert_eq!(stored, 1, "the row flushed within one batch wait");
    }
}
