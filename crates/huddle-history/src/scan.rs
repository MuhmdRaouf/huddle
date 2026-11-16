//! Scans of the request table that add rows into maps in memory on the calling thread.

use std::borrow::Cow;
use std::collections::HashMap;
use std::fmt::Write;

use rusqlite::Row as SqlRow;
use rusqlite::types::ValueRef;

use crate::error::HistoryError;
use crate::history::History;
use crate::query::opt_count;
use crate::row::Range;

/// A text column borrowed from the row, an integer (a status) as its decimal text.
pub(crate) fn text_at<'r>(r: &'r SqlRow<'_>, i: usize) -> rusqlite::Result<Cow<'r, str>> {
    match r.get_ref(i)? {
        ValueRef::Text(bytes) => Ok(String::from_utf8_lossy(bytes)),
        ValueRef::Integer(n) => Ok(Cow::Owned(n.to_string())),
        _ => Ok(Cow::Borrowed("")),
    }
}

/// Appends column `i` to `out` as `text_at` reads it, writing an integer's digits in place
/// instead of into a new string.
pub(crate) fn push_text(r: &SqlRow<'_>, i: usize, out: &mut String) -> rusqlite::Result<()> {
    match r.get_ref(i)? {
        ValueRef::Text(bytes) => out.push_str(&String::from_utf8_lossy(bytes)),
        ValueRef::Integer(n) => {
            let _ = write!(out, "{n}");
        }
        _ => {}
    }
    Ok(())
}

/// Numbers each distinct name once, in first-seen order.
#[derive(Default)]
pub(crate) struct Interner {
    ids: HashMap<String, usize>,
    pub(crate) names: Vec<String>,
}

impl Interner {
    pub(crate) fn id(&mut self, name: &str) -> usize {
        if let Some(&id) = self.ids.get(name) {
            return id;
        }
        let id = self.names.len();
        self.ids.insert(name.to_owned(), id);
        self.names.push(name.to_owned());
        id
    }
}

/// Running sums of one key and model.
pub(crate) struct Sums {
    pub(crate) n: u64,
    pub(crate) err: u64,
    pub(crate) tok_in: u64,
    pub(crate) cache_read: u64,
    pub(crate) cache_write: u64,
    pub(crate) tok_out: u64,
    pub(crate) first_ms: u64,
    pub(crate) last_ms: u64,
}

impl Default for Sums {
    fn default() -> Sums {
        Sums {
            n: 0,
            err: 0,
            tok_in: 0,
            cache_read: 0,
            cache_write: 0,
            tok_out: 0,
            first_ms: u64::MAX,
            last_ms: 0,
        }
    }
}

impl Sums {
    /// Adds a row at `at` whose four token columns, from `first` on, are tokens_in, cache_read,
    /// cache_write and tokens_out.
    pub(crate) fn add(
        &mut self,
        at: u64,
        err: bool,
        r: &SqlRow<'_>,
        first: usize,
    ) -> rusqlite::Result<()> {
        self.n += 1;
        self.err += u64::from(err);
        self.tok_in += opt_count(r, first)?.unwrap_or(0);
        self.cache_read += opt_count(r, first + 1)?.unwrap_or(0);
        self.cache_write += opt_count(r, first + 2)?.unwrap_or(0);
        self.tok_out += opt_count(r, first + 3)?.unwrap_or(0);
        self.first_ms = self.first_ms.min(at);
        self.last_ms = self.last_ms.max(at);
        Ok(())
    }
}

impl Sums {
    /// Adds the sums of another slice.
    pub(crate) fn merge(&mut self, other: &Sums) {
        self.n += other.n;
        self.err += other.err;
        self.tok_in += other.tok_in;
        self.cache_read += other.cache_read;
        self.cache_write += other.cache_write;
        self.tok_out += other.tok_out;
        self.first_ms = self.first_ms.min(other.first_ms);
        self.last_ms = self.last_ms.max(other.last_ms);
    }
}

impl History {
    /// Runs `work` over `range` on this handle's one read connection and returns its result as
    /// the single slice. No thread and no connection is created: a call's memory is its own
    /// maps and one connection's page cache. The grid arguments are the callers' bucket
    /// alignment, which one whole scan has no use for.
    pub(crate) fn sliced<T>(
        &self,
        range: Range,
        align_ms: u64,
        work: impl Fn(&History, Range) -> Result<T, HistoryError>,
    ) -> Result<Vec<T>, HistoryError> {
        self.sliced_in(range, align_ms, 0, work)
    }

    /// `sliced`, with the bucket grid shifted by `offset_ms`.
    pub(crate) fn sliced_in<T>(
        &self,
        range: Range,
        _align_ms: u64,
        _offset_ms: i64,
        work: impl Fn(&History, Range) -> Result<T, HistoryError>,
    ) -> Result<Vec<T>, HistoryError> {
        work(self, range).map(|one| vec![one])
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;
    use std::thread;

    use huddle_testkit::temp_home::TempHome;

    use crate::history::History;
    use crate::row::Range;

    const DAY: u64 = 86_400_000;

    #[test]
    fn l26_a_history_call_spawns_no_threads() {
        let home = TempHome::new();
        let history = History::open(&home.root().join("scratch/history/huddle.db")).expect("open");
        let seen = Mutex::new(Vec::new());
        let range = Range {
            from_ms: 0,
            to_ms: 90 * DAY,
        };
        let parts = history
            .sliced_in(range, DAY, 0, |_, slice| {
                seen.lock().expect("lock").push(thread::current().id());
                Ok(slice)
            })
            .expect("sliced");
        let seen = seen.into_inner().expect("lock");
        assert!(!parts.is_empty());
        assert!(
            seen.iter().all(|id| *id == thread::current().id()),
            "a slice ran on another thread: {seen:?}"
        );
    }
}
