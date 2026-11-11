//! A cached reading refreshed on demand. A caller that finds the reading older than the window
//! starts one background read on a blocking thread and still gets the old reading at once, so
//! a slow disk or `git` never holds a request. With no caller nothing runs.

use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::{Duration, Instant};

use tokio::runtime::Handle;

/// Takes one reading. Runs on a blocking thread.
pub type Read<T> = Arc<dyn Fn() -> T + Send + Sync>;

struct State<T> {
    reading: Arc<T>,
    read_at: Option<Instant>,
    busy: bool,
}

pub struct Cache<T> {
    read: Read<T>,
    window: Duration,
    runtime: Handle,
    state: Mutex<State<T>>,
}

/// Clears `busy` when a read ends, also when it panics.
struct Done<'a, T>(&'a Cache<T>);

impl<T> Drop for Done<'_, T> {
    fn drop(&mut self) {
        self.0.lock().busy = false;
    }
}

impl<T> Cache<T> {
    fn lock(&self) -> MutexGuard<'_, State<T>> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

impl<T: Default + Send + Sync + 'static> Cache<T> {
    /// The first reading is `T::default()` until the first read lands.
    pub fn new(read: Read<T>, window: Duration, runtime: Handle) -> Arc<Cache<T>> {
        Arc::new(Cache {
            read,
            window,
            runtime,
            state: Mutex::new(State {
                reading: Arc::new(T::default()),
                read_at: None,
                busy: false,
            }),
        })
    }

    pub fn latest(self: &Arc<Cache<T>>) -> Arc<T> {
        let mut state = self.lock();
        let stale = state.read_at.is_none_or(|at| at.elapsed() >= self.window);
        if stale && !state.busy {
            state.busy = true;
            let cache = Arc::clone(self);
            self.runtime.spawn_blocking(move || {
                let _done = Done(&cache);
                let reading = Arc::new((cache.read)());
                let mut state = cache.lock();
                state.reading = reading;
                state.read_at = Some(Instant::now());
            });
        }
        Arc::clone(&state.reading)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn counting_cache(window: Duration) -> (Arc<Cache<u32>>, Arc<AtomicUsize>) {
        let reads = Arc::new(AtomicUsize::new(0));
        let read: Read<u32> = Arc::new({
            let reads = Arc::clone(&reads);
            move || {
                reads.fetch_add(1, Ordering::SeqCst);
                std::thread::sleep(Duration::from_millis(150));
                7
            }
        });
        (Cache::new(read, window, Handle::current()), reads)
    }

    #[tokio::test]
    async fn no_read_runs_while_no_snapshot_asks() {
        let (_cache, reads) = counting_cache(Duration::ZERO);
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(reads.load(Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn concurrent_snapshots_after_the_window_start_one_read() {
        let (cache, reads) = counting_cache(Duration::ZERO);
        let callers: Vec<_> = (0..8)
            .map(|_| {
                let cache = Arc::clone(&cache);
                tokio::spawn(async move { cache.latest() })
            })
            .collect();
        for caller in callers {
            caller.await.expect("caller");
        }
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(reads.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_fresh_reading_is_not_read_again() {
        let (cache, reads) = counting_cache(Duration::from_secs(60));
        assert_eq!(*cache.latest(), 0);
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(*cache.latest(), 7);
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert_eq!(reads.load(Ordering::SeqCst), 1);
    }
}
