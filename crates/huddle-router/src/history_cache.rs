//! The history answers the dashboard tabs share.
//!
//! An answer is kept under its query string, the newest row the database holds and the minute it
//! was read in, so every tab asking the same thing inside a minute reads the database once, and a
//! newer row or the next minute asks again.

use std::collections::VecDeque;
use std::sync::{Mutex, PoisonError};

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

/// A bounded, shared set of finished answers.
#[derive(Default)]
pub struct HistoryCache {
    entries: Mutex<VecDeque<(CacheKey, String)>>,
}

impl HistoryCache {
    /// The kept answer for `key`, else the one `compute` reads, which is kept when it succeeds.
    pub fn get_or_compute<E>(
        &self,
        key: CacheKey,
        compute: impl FnOnce() -> Result<String, E>,
    ) -> Result<String, E> {
        if let Some(hit) = self.lookup(&key) {
            return Ok(hit);
        }
        let answer = compute()?;
        let mut entries = self.entries.lock().unwrap_or_else(PoisonError::into_inner);
        entries.retain(|(kept, _)| *kept != key);
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
            let _ = cache.get_or_compute(key("q", 0, n), || Ok::<_, ()>(n.to_string()));
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
}
