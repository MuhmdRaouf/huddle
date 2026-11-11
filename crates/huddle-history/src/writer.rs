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
        let thread = thread::spawn(move || {
            if let Ok(mut history) = History::open(&path) {
                run(&mut history, &rx, &lost);
            }
        });
        Writer {
            tx: Some(tx),
            dropped,
            thread: Some(thread),
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

fn run(history: &mut History, rx: &Receiver<Row>, lost: &AtomicU64) {
    let mut batch: Vec<Row> = Vec::with_capacity(BATCH_ROWS);
    let mut deadline = Instant::now() + BATCH_WAIT;
    loop {
        let wait = deadline.saturating_duration_since(Instant::now());
        let open = match rx.recv_timeout(wait) {
            Ok(row) => {
                batch.push(row);
                true
            }
            Err(RecvTimeoutError::Timeout) => true,
            Err(RecvTimeoutError::Disconnected) => false,
        };
        if !open || batch.len() >= BATCH_ROWS || Instant::now() >= deadline {
            if !batch.is_empty() {
                if history.insert(&batch).is_err() {
                    lost.fetch_add(batch.len() as u64, Ordering::Relaxed);
                }
                batch.clear();
            }
            deadline = Instant::now() + BATCH_WAIT;
        }
        if !open {
            return;
        }
    }
}
