//! The history answers the dashboard tabs share.
//!
//! An answer is kept under its query string, the newest row the database holds and the minute it
//! was read in, so every tab asking the same thing inside a minute reads the database once, and a
//! newer row or the next minute asks again. Asks of one query that arrive while its answer is
//! being read wait for that one read and share its answer, and each query keeps only its newest
//! answer.

use std::collections::{HashSet, VecDeque};
use std::sync::{Condvar, Mutex, PoisonError};

use huddle_history::{Filter, History, Page, Range};

/// The most answers kept; the oldest goes first.
pub const CAPACITY: usize = 32;

/// What makes two history questions the same question.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CacheKey {
    pub query: String,
    /// A signature of the database's newest rows: it changes when a row arrives or is pruned.
    pub newest: u64,
    pub minute: u64,
}

/// The last hour the signature looks at: short enough that the database reads it on this
/// thread.
const PROBE_MS: u64 = 3_600_000;

/// A number that changes when a row arrives: the count of the last hour's rows and the newest
/// row's time, from one index lookup. A database that cannot answer gives 0; the minute in the
/// key bounds how long an answer outlives a change this misses.
pub fn newest_signature(history: &History, now_ms: u64) -> u64 {
    let hour = Filter::new(Range {
        from_ms: now_ms.saturating_sub(PROBE_MS),
        to_ms: now_ms.saturating_add(1),
    });
    match history.search(
        hour,
        Page {
            limit: 1,
            offset: 0,
        },
    ) {
        Ok(found) => {
            let newest = found.rows.first().map_or(0, |row| row.at_ms);
            found.total.rotate_left(40) ^ newest
        }
        Err(_) => 0,
    }
}

/// A bounded, shared set of finished answers. Concurrent asks of one query share one read, and
/// each query keeps only its newest answer.
#[derive(Default)]
pub struct HistoryCache {
    entries: Mutex<VecDeque<(CacheKey, String)>>,
    /// The query strings whose answer is being read right now.
    computing: Mutex<HashSet<String>>,
    /// Wakes the waiters when a read ends, in success and in error.
    finished: Condvar,
}

/// Ends the read of one query: it removes the query from `computing` and wakes the waiters when
/// dropped, which happens on success and on error alike, so a failed read never strands them.
struct Reading<'a> {
    cache: &'a HistoryCache,
    query: String,
}

impl Drop for Reading<'_> {
    fn drop(&mut self) {
        let mut computing = self
            .cache
            .computing
            .lock()
            .unwrap_or_else(PoisonError::into_inner);
        computing.remove(&self.query);
        self.cache.finished.notify_all();
    }
}

impl HistoryCache {
    /// The kept answer for `key`, else the one `compute` reads, which is kept when it succeeds.
    /// Asks of one query that arrive while its answer is being read wait for that read: they
    /// share its answer on success, and each reads on its own after a failed read. No lock is
    /// held while `compute` runs.
    pub fn get_or_compute<E>(
        &self,
        key: CacheKey,
        compute: impl FnOnce() -> Result<String, E>,
    ) -> Result<String, E> {
        if let Some(hit) = self.lookup(&key) {
            return Ok(hit);
        }
        let mut computing = self
            .computing
            .lock()
            .unwrap_or_else(PoisonError::into_inner);
        while computing.contains(&key.query) {
            computing = self
                .finished
                .wait(computing)
                .unwrap_or_else(PoisonError::into_inner);
        }
        if let Some(hit) = self.lookup(&key) {
            return Ok(hit);
        }
        computing.insert(key.query.clone());
        drop(computing);
        let _reading = Reading {
            cache: self,
            query: key.query.clone(),
        };
        let answer = compute()?;
        let mut entries = self.entries.lock().unwrap_or_else(PoisonError::into_inner);
        entries.retain(|(kept, _)| kept.query != key.query);
        entries.push_back((key, answer.clone()));
        while entries.len() > CAPACITY {
            entries.pop_front();
        }
        Ok(answer)
    }

    fn lookup(&self, key: &CacheKey) -> Option<String> {
        let entries = self.entries.lock().unwrap_or_else(PoisonError::into_inner);
        entries
            .iter()
            .find(|(kept, _)| kept == key)
            .map(|(_, answer)| answer.clone())
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.entries
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::Cell;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::{Duration, Instant};

    fn key(query: &str, newest: u64, minute: u64) -> CacheKey {
        CacheKey {
            query: query.into(),
            newest,
            minute,
        }
    }

    #[test]
    fn l25_a_repeated_query_is_answered_from_the_cache() {
        let cache = HistoryCache::default();
        let reads = Cell::new(0);
        let read = || {
            reads.set(reads.get() + 1);
            Ok::<_, ()>("answer".to_string())
        };
        let a = cache.get_or_compute(key("range=90d", 7, 100), read);
        let b = cache.get_or_compute(key("range=90d", 7, 100), read);
        assert_eq!((a, b), (Ok("answer".into()), Ok("answer".into())));
        assert_eq!(reads.get(), 1);
    }

    #[test]
    fn l25_a_new_row_or_a_new_minute_misses_the_cache() {
        let cache = HistoryCache::default();
        let reads = Cell::new(0);
        let read = || {
            reads.set(reads.get() + 1);
            Ok::<_, ()>("answer".to_string())
        };
        let _ = cache.get_or_compute(key("range=90d", 7, 100), read);
        let _ = cache.get_or_compute(key("range=90d", 8, 100), read);
        let _ = cache.get_or_compute(key("range=90d", 8, 101), read);
        let _ = cache.get_or_compute(key("range=30d", 8, 101), read);
        assert_eq!(reads.get(), 4);
    }

    #[test]
    fn l25_the_cache_holds_a_bounded_number_of_answers() {
        let cache = HistoryCache::default();
        for n in 0..(CAPACITY as u64 * 2) {
            let _ =
                cache.get_or_compute(key(&format!("q{n}"), 0, 0), || Ok::<_, ()>(n.to_string()));
        }
        assert_eq!(cache.len(), CAPACITY);
    }

    #[test]
    fn l25_a_failed_read_is_not_cached() {
        let cache = HistoryCache::default();
        assert_eq!(
            cache.get_or_compute(key("q", 0, 0), || Err::<String, _>(())),
            Err(())
        );
        assert_eq!(cache.len(), 0);
    }

    #[test]
    fn l50_concurrent_asks_of_one_query_compute_once() {
        let cache = HistoryCache::default();
        let calls = AtomicUsize::new(0);
        let answers: Vec<Result<String, ()>> = std::thread::scope(|scope| {
            let mut handles = Vec::new();
            for _ in 0..5 {
                let cache = &cache;
                let calls = &calls;
                handles.push(scope.spawn(move || {
                    cache.get_or_compute(key("q", 1, 1), || {
                        calls.fetch_add(1, Ordering::SeqCst);
                        std::thread::sleep(Duration::from_millis(50));
                        Ok::<_, ()>("answer".to_string())
                    })
                }));
            }
            handles
                .into_iter()
                .map(|handle| handle.join().unwrap())
                .collect()
        });
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        for answer in &answers {
            assert_eq!(*answer, Ok("answer".to_string()));
        }
    }

    #[test]
    fn l50_a_newer_answer_replaces_the_older_one_of_the_same_query() {
        let cache = HistoryCache::default();
        let _ = cache.get_or_compute(key("q", 1, 1), || Ok::<_, ()>("old".to_string()));
        let _ = cache.get_or_compute(key("q", 2, 1), || Ok::<_, ()>("mid".to_string()));
        let _ = cache.get_or_compute(key("q", 2, 2), || Ok::<_, ()>("new".to_string()));
        assert_eq!(cache.len(), 1);
        assert_eq!(cache.lookup(&key("q", 2, 2)), Some("new".to_string()));
    }

    #[test]
    fn l50_a_failed_compute_frees_the_waiters() {
        let cache = HistoryCache::default();
        let calls = AtomicUsize::new(0);
        let answer = std::thread::scope(|scope| {
            scope.spawn(|| {
                let _ = cache.get_or_compute(key("q", 1, 1), || {
                    calls.fetch_add(1, Ordering::SeqCst);
                    std::thread::sleep(Duration::from_millis(50));
                    Err(())
                });
            });
            std::thread::sleep(Duration::from_millis(10));
            cache.get_or_compute(key("q", 1, 1), || {
                calls.fetch_add(1, Ordering::SeqCst);
                Ok::<_, ()>("second".to_string())
            })
        });
        assert_eq!(answer, Ok("second".to_string()));
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    #[test]
    fn l50_two_queries_do_not_wait_for_each_other() {
        let cache = HistoryCache::default();
        std::thread::scope(|scope| {
            scope.spawn(|| {
                let _ = cache.get_or_compute(key("a", 1, 1), || {
                    std::thread::sleep(Duration::from_millis(300));
                    Ok::<_, ()>("a".to_string())
                });
            });
            std::thread::sleep(Duration::from_millis(50));
            let asked = Instant::now();
            let answer = cache.get_or_compute(key("b", 1, 1), || Ok::<_, ()>("b".to_string()));
            let waited = asked.elapsed();
            assert_eq!(answer, Ok("b".to_string()));
            assert!(
                waited < Duration::from_millis(200),
                "query b waited {waited:?}"
            );
        });
    }
}
