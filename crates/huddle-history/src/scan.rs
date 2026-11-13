//! Scans of the request table that add rows into maps in memory, one slice of the time range per
//! thread.

use std::borrow::Cow;
use std::collections::HashMap;
use std::thread;

use rusqlite::Row as SqlRow;
use rusqlite::types::ValueRef;

use crate::error::HistoryError;
use crate::history::History;
use crate::query::{count, opt_count};
use crate::row::Range;

/// The most slices one scan splits into.
const MAX_SLICES: usize = 4;

/// A range shorter than this is scanned on the calling thread.
const SLICE_FROM_MS: u64 = 6 * 3_600_000;

/// A text column borrowed from the row, an integer (a day number) as its decimal text.
pub(crate) fn text_at<'r>(r: &'r SqlRow<'_>, i: usize) -> rusqlite::Result<Cow<'r, str>> {
    match r.get_ref(i)? {
        ValueRef::Text(bytes) => Ok(String::from_utf8_lossy(bytes)),
        ValueRef::Integer(n) => Ok(Cow::Owned(n.to_string())),
        _ => Ok(Cow::Borrowed("")),
    }
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
    /// Adds a row whose columns from `first` on are at_ms, is_error, tokens_in, cache_read,
    /// cache_write and tokens_out.
    pub(crate) fn add(&mut self, r: &SqlRow<'_>, first: usize) -> rusqlite::Result<()> {
        let at = count(r, first)?;
        self.n += 1;
        self.err += count(r, first + 1)?;
        self.tok_in += opt_count(r, first + 2)?.unwrap_or(0);
        self.cache_read += opt_count(r, first + 3)?.unwrap_or(0);
        self.cache_write += opt_count(r, first + 4)?.unwrap_or(0);
        self.tok_out += opt_count(r, first + 5)?.unwrap_or(0);
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

/// Cuts `range` into up to `parts` consecutive slices whose inner boundaries are multiples of
/// `align_ms`, so an aligned bucket never straddles two slices.
fn cut(range: Range, align_ms: u64, parts: u64) -> Vec<Range> {
    let step = range.to_ms.saturating_sub(range.from_ms) / parts.max(1);
    let mut edges = vec![range.from_ms];
    for i in 1..parts {
        let edge = (range.from_ms + i * step) / align_ms * align_ms;
        if edge > edges.last().copied().unwrap_or(0) && edge < range.to_ms {
            edges.push(edge);
        }
    }
    edges.push(range.to_ms);
    edges
        .windows(2)
        .map(|w| Range {
            from_ms: w[0],
            to_ms: w[1],
        })
        .collect()
}

impl History {
    /// Runs `work` over slices of `range`, each on its own thread and read connection, and
    /// returns the results in slice order. A short range, or a handle with no file, runs whole
    /// on this thread.
    pub(crate) fn sliced<T: Send>(
        &self,
        range: Range,
        align_ms: u64,
        work: impl Fn(&History, Range) -> Result<T, HistoryError> + Sync,
    ) -> Result<Vec<T>, HistoryError> {
        let parts = thread::available_parallelism()
            .map(|n| n.get())
            .unwrap_or(1)
            .min(MAX_SLICES) as u64;
        let long = range.to_ms.saturating_sub(range.from_ms) >= SLICE_FROM_MS;
        if !long || parts < 2 {
            return work(self, range).map(|one| vec![one]);
        }
        let slices = cut(range, align_ms.max(1), parts);
        thread::scope(|scope| {
            let work = &work;
            let path = &self.path;
            let jobs: Vec<_> = slices
                .into_iter()
                .map(|slice| {
                    scope.spawn(move || {
                        let reader = History::open_read(path)?;
                        work(&reader, slice)
                    })
                })
                .collect();
            jobs.into_iter()
                .map(|job| job.join().unwrap_or(Err(HistoryError::Worker)))
                .collect()
        })
    }
}
