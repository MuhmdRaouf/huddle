use std::collections::{BTreeMap, HashMap};
use std::fmt::Write;

use crate::scan::{Interner, Sums, push_text, text_at};

use rusqlite::types::Value;
use rusqlite::{Row as SqlRow, params_from_iter};

use crate::error::HistoryError;
use crate::history::{History, from_db, to_db};
use crate::row::{Range, Row};

const MAX_PAGE: u64 = 500;

/// A request counts as an error on status 0 or 4xx/5xx, except 499 (the client left).
const IS_ERROR: &str = "(status = 0 OR (status >= 400 AND status != 499))";

/// The first and last request time. One MIN or MAX alone is a single seek to an end of the time
/// index; both in one SELECT walk the whole index.
const BOUNDS: &str = "SELECT (SELECT MIN(at_ms) FROM requests), (SELECT MAX(at_ms) FROM requests)";

/// `IS_ERROR` for the status in column `i`, decided in Rust so a scan steps plain columns only.
fn is_error(r: &SqlRow<'_>, i: usize) -> rusqlite::Result<bool> {
    let status: i64 = r.get(i)?;
    Ok(status == 0 || (status >= 400 && status != 499))
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Bucket {
    pub start_ms: u64,
    pub model: String,
    pub n: u64,
    pub err: u64,
    pub tok_in: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub tok_out: u64,
    pub ttft_p50: Option<u64>,
    pub ttft_p95: Option<u64>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GroupBy {
    Session,
    Project,
    Model,
    Lane,
    /// Provider, status and failure reason, as `lane|status|reason` (an empty reason when none).
    Reason,
    /// A calendar day of the zone the query names (UTC by default), `YYYY-MM-DD`.
    Day,
}

impl GroupBy {
    /// The key columns a totals scan selects ahead of `model`, each followed by a comma. The key
    /// is built in Rust: an expression per row in SQL (a `COALESCE`, a string join) costs more
    /// than the scan of the columns themselves. A day and a reason reuse the scan's own `at_ms`
    /// and `status`.
    fn key_columns(self) -> &'static str {
        match self {
            GroupBy::Session => "session, ",
            GroupBy::Project => "project, ",
            GroupBy::Model => "model, ",
            GroupBy::Lane => "lane, ",
            GroupBy::Reason => "lane, reason, ",
            GroupBy::Day => "",
        }
    }

    /// How many columns `key_columns` names.
    fn key_width(self) -> usize {
        match self {
            GroupBy::Reason => 2,
            GroupBy::Day => 0,
            _ => 1,
        }
    }

    /// Writes the key of `r` into `key`; `model_at` is the column of `model`, followed by
    /// `at_ms` and `status`. An unknown session, agent or project is the empty key. A day is its
    /// number since the epoch in the zone `offset_ms` east of UTC, which `totals` names
    /// afterwards.
    fn write_key(
        self,
        r: &SqlRow<'_>,
        model_at: usize,
        offset_ms: i64,
        key: &mut String,
    ) -> rusqlite::Result<()> {
        key.clear();
        match self {
            GroupBy::Reason => {
                push_text(r, 0, key)?;
                key.push('|');
                push_text(r, model_at + 2, key)?;
                key.push('|');
                push_text(r, 1, key)?;
            }
            GroupBy::Day => {
                let at: i64 = r.get(model_at + 1)?;
                let _ = write!(key, "{}", at.saturating_add(offset_ms) / 86_400_000);
            }
            _ => push_text(r, 0, key)?,
        }
        Ok(())
    }
}

/// Rows a totals query keeps, decided in SQL. An empty list keeps every row; the empty name
/// stands for a row with no session or no project.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Narrow {
    pub sessions: Vec<String>,
    pub projects: Vec<String>,
}

impl Narrow {
    /// The `AND` clauses for this filter, numbering its parameters from `?first`.
    fn clause(&self, first: usize) -> (String, Vec<Value>) {
        let mut sql = String::new();
        let mut args = Vec::new();
        for (column, names) in [("session", &self.sessions), ("project", &self.projects)] {
            if names.is_empty() {
                continue;
            }
            let marks: Vec<String> = names
                .iter()
                .filter(|name| !name.is_empty())
                .map(|name| {
                    args.push(Value::Text(name.clone()));
                    format!("?{}", first + args.len() - 1)
                })
                .collect();
            let mut tests = Vec::new();
            if !marks.is_empty() {
                tests.push(format!("{column} IN ({})", marks.join(",")));
            }
            if names.iter().any(String::is_empty) {
                tests.push(format!("COALESCE({column}, '') = ''"));
            }
            sql.push_str(&format!(" AND ({})", tests.join(" OR ")));
        }
        (sql, args)
    }
}

/// Token sums for one model, so a caller can price them.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ModelUsage {
    pub model: String,
    pub n: u64,
    pub tok_in: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub tok_out: u64,
}

/// One slice's sums per key and model, and its first-token times per key.
type TotalsPart = (HashMap<(String, String), Sums>, HashMap<String, Vec<u64>>);

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Total {
    pub key: String,
    pub n: u64,
    pub err: u64,
    pub tok_in: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub tok_out: u64,
    pub first_ms: u64,
    pub last_ms: u64,
    /// Time to first token over this key's rows that have one (nearest rank).
    pub ttft_p50: Option<u64>,
    pub ttft_p95: Option<u64>,
    pub models: Vec<ModelUsage>,
}

impl Total {
    fn absorb(&mut self, other: Total) {
        self.n += other.n;
        self.err += other.err;
        self.tok_in += other.tok_in;
        self.cache_read += other.cache_read;
        self.cache_write += other.cache_write;
        self.tok_out += other.tok_out;
        self.first_ms = self.first_ms.min(other.first_ms);
        self.last_ms = self.last_ms.max(other.last_ms);
        // Percentiles of two groups cannot be combined from their own percentiles.
        self.ttft_p50 = None;
        self.ttft_p95 = None;
        for usage in other.models {
            match self.models.iter_mut().find(|m| m.model == usage.model) {
                Some(m) => {
                    m.n += usage.n;
                    m.tok_in += usage.tok_in;
                    m.cache_read += usage.cache_read;
                    m.cache_write += usage.cache_write;
                    m.tok_out += usage.tok_out;
                }
                None => self.models.push(usage),
            }
        }
        self.models.sort_by(|a, b| a.model.cmp(&b.model));
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum StatusClass {
    #[default]
    Any,
    Ok,
    Error,
    ClientLeft,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Filter {
    pub range: Range,
    pub model: Option<String>,
    pub lane: Option<String>,
    pub session: Option<String>,
    pub agent: Option<String>,
    pub project: Option<String>,
    pub status: StatusClass,
}

impl Filter {
    pub fn new(range: Range) -> Filter {
        Filter {
            range,
            model: None,
            lane: None,
            session: None,
            agent: None,
            project: None,
            status: StatusClass::Any,
        }
    }
}

/// `limit` is capped at 500.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Page {
    pub limit: u64,
    pub offset: u64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SearchResult {
    pub rows: Vec<Row>,
    pub total: u64,
}

pub(crate) fn count(r: &SqlRow<'_>, i: usize) -> rusqlite::Result<u64> {
    Ok(from_db(r.get(i)?))
}

pub(crate) fn opt_count(r: &SqlRow<'_>, i: usize) -> rusqlite::Result<Option<u64>> {
    Ok(r.get::<_, Option<i64>>(i)?.map(from_db))
}

fn offset_ms(tz_offset_min: i32) -> i64 {
    i64::from(tz_offset_min) * 60_000
}

/// The start of the `width_ms` bucket holding `at_ms`, on the grid shifted by `offset_ms`.
fn bucket_start(at_ms: u64, width_ms: u64, offset_ms: i64) -> u64 {
    let width = width_ms as i64;
    let shifted = at_ms as i64 + offset_ms;
    (shifted.div_euclid(width) * width - offset_ms).max(0) as u64
}

/// Nearest-rank percentile of an ascending slice; `pct` in 1..=100.
fn nearest_rank(sorted: &[u64], pct: usize) -> Option<u64> {
    if sorted.is_empty() {
        return None;
    }
    let rank = (pct * sorted.len()).div_ceil(100).max(1);
    sorted.get(rank - 1).copied()
}

impl History {
    /// The table clause for a scan of `range`. A range that holds every row reads the table in
    /// row order (`NOT INDEXED`): stepping the time index and looking each row up costs more than
    /// reading every row once.
    fn scanned(&self, range: Range) -> Result<&'static str, HistoryError> {
        let (first, last): (Option<i64>, Option<i64>) = self
            .conn
            .query_row(BOUNDS, [], |r| Ok((r.get(0)?, r.get(1)?)))?;
        let whole = match (first, last) {
            (Some(first), Some(last)) => to_db(range.from_ms) <= first && last < to_db(range.to_ms),
            _ => false,
        };
        Ok(if whole {
            "requests NOT INDEXED"
        } else {
            "requests"
        })
    }

    /// Per bucket start (a multiple of `width_ms`) and model: counts, token sums, TTFT percentiles.
    /// `session` and `project` narrow the rows to one session or one project.
    pub fn buckets(
        &self,
        range: Range,
        width_ms: u64,
        session: Option<&str>,
        project: Option<&str>,
    ) -> Result<Vec<Bucket>, HistoryError> {
        self.buckets_in(range, width_ms, session, project, 0)
    }

    /// `buckets` on the grid of the zone `tz_offset_min` minutes east of UTC: a bucket starts
    /// where `start_ms + offset` is a multiple of `width_ms`.
    pub fn buckets_in(
        &self,
        range: Range,
        width_ms: u64,
        session: Option<&str>,
        project: Option<&str>,
        tz_offset_min: i32,
    ) -> Result<Vec<Bucket>, HistoryError> {
        if width_ms == 0 {
            return Err(HistoryError::ZeroWidth);
        }
        let offset_ms = offset_ms(tz_offset_min);
        let parts = self.sliced_in(range, width_ms, offset_ms, |history, slice| {
            history.buckets_part(slice, width_ms, offset_ms, session, project)
        })?;
        // The slices are disjoint and aligned to the width, so their buckets are already in order.
        Ok(parts.into_iter().flatten().collect())
    }

    fn buckets_part(
        &self,
        range: Range,
        width_ms: u64,
        offset_ms: i64,
        session: Option<&str>,
        project: Option<&str>,
    ) -> Result<Vec<Bucket>, HistoryError> {
        let mut args = vec![
            Value::Integer(to_db(range.from_ms)),
            Value::Integer(to_db(range.to_ms)),
        ];
        let mut narrow = String::new();
        for (column, wanted) in [("session", session), ("project", project)] {
            if let Some(value) = wanted {
                args.push(Value::Text(value.to_owned()));
                narrow.push_str(&format!(" AND {column} = ?{}", args.len()));
            }
        }
        let mut stmt = self.conn.prepare(&format!(
            "SELECT model, at_ms, status, tokens_in, cache_read, cache_write, tokens_out, ttft_ms
             FROM {table} WHERE at_ms >= ?1 AND at_ms < ?2{narrow}",
            table = self.scanned(range)?
        ))?;
        let mut models = Interner::default();
        let mut cells: HashMap<(u64, usize), (Sums, Vec<u64>)> = HashMap::new();
        let mut rows = stmt.query(params_from_iter(&args))?;
        while let Some(r) = rows.next()? {
            let model = models.id(&text_at(r, 0)?);
            let at = count(r, 1)?;
            let (sums, ttft) = cells
                .entry((bucket_start(at, width_ms, offset_ms), model))
                .or_default();
            sums.add(at, is_error(r, 2)?, r, 3)?;
            if let Some(ms) = opt_count(r, 7)? {
                ttft.push(ms);
            }
        }
        let mut buckets: Vec<Bucket> = cells
            .into_iter()
            .map(|((start_ms, model), (sums, mut ttft))| {
                ttft.sort_unstable();
                Bucket {
                    start_ms,
                    model: models.names.get(model).cloned().unwrap_or_default(),
                    n: sums.n,
                    err: sums.err,
                    tok_in: sums.tok_in,
                    cache_read: sums.cache_read,
                    cache_write: sums.cache_write,
                    tok_out: sums.tok_out,
                    ttft_p50: nearest_rank(&ttft, 50),
                    ttft_p95: nearest_rank(&ttft, 95),
                }
            })
            .collect();
        buckets.sort_by(|a, b| (a.start_ms, &a.model).cmp(&(b.start_ms, &b.model)));
        Ok(buckets)
    }

    /// Per key, ordered by key: counts, token sums, first and last request, and the per-model sums.
    ///
    /// Each slice of the range adds its rows into a map in memory (a `GROUP BY` over a million
    /// rows spends most of its time in SQLite's temporary sorter); the slices then merge.
    pub fn totals(&self, range: Range, by: GroupBy) -> Result<Vec<Total>, HistoryError> {
        self.totals_in(range, by, 0)
    }

    /// `totals`, with `GroupBy::Day` keyed by the days of the zone `tz_offset_min` minutes east
    /// of UTC.
    pub fn totals_in(
        &self,
        range: Range,
        by: GroupBy,
        tz_offset_min: i32,
    ) -> Result<Vec<Total>, HistoryError> {
        self.totals_narrow(range, by, tz_offset_min, &Narrow::default())
    }

    /// `totals_in` over the rows `narrow` keeps; the filter is part of the SQL, so rows it
    /// drops are never read.
    pub fn totals_narrow(
        &self,
        range: Range,
        by: GroupBy,
        tz_offset_min: i32,
        narrow: &Narrow,
    ) -> Result<Vec<Total>, HistoryError> {
        let offset_ms = offset_ms(tz_offset_min);
        let parts = self.sliced_in(range, 1, 0, |history, slice| {
            history.totals_part(slice, by, offset_ms, narrow)
        })?;
        let mut merged: BTreeMap<String, BTreeMap<String, Sums>> = BTreeMap::new();
        let mut ttft: HashMap<String, Vec<u64>> = HashMap::new();
        for (sums, times) in parts {
            for ((key, model), sums) in sums {
                merged
                    .entry(key)
                    .or_default()
                    .entry(model)
                    .or_default()
                    .merge(&sums);
            }
            for (key, mut times) in times {
                ttft.entry(key).or_default().append(&mut times);
            }
        }
        Ok(merged
            .into_iter()
            .map(|(key, per_model)| {
                let mut times = ttft.remove(&key).unwrap_or_default();
                times.sort_unstable();
                let mut total = Total {
                    ttft_p50: nearest_rank(&times, 50),
                    ttft_p95: nearest_rank(&times, 95),
                    key,
                    n: 0,
                    err: 0,
                    tok_in: 0,
                    cache_read: 0,
                    cache_write: 0,
                    tok_out: 0,
                    first_ms: u64::MAX,
                    last_ms: 0,
                    models: Vec::with_capacity(per_model.len()),
                };
                for (model, sums) in per_model {
                    total.n += sums.n;
                    total.err += sums.err;
                    total.tok_in += sums.tok_in;
                    total.cache_read += sums.cache_read;
                    total.cache_write += sums.cache_write;
                    total.tok_out += sums.tok_out;
                    total.first_ms = total.first_ms.min(sums.first_ms);
                    total.last_ms = total.last_ms.max(sums.last_ms);
                    total.models.push(ModelUsage {
                        model,
                        n: sums.n,
                        tok_in: sums.tok_in,
                        cache_read: sums.cache_read,
                        cache_write: sums.cache_write,
                        tok_out: sums.tok_out,
                    });
                }
                total
            })
            .collect())
    }

    fn totals_part(
        &self,
        range: Range,
        by: GroupBy,
        offset_ms: i64,
        narrow: &Narrow,
    ) -> Result<TotalsPart, HistoryError> {
        let (kept, extra) = narrow.clause(3);
        let mut args = vec![
            Value::Integer(to_db(range.from_ms)),
            Value::Integer(to_db(range.to_ms)),
        ];
        args.extend(extra);
        let mut stmt = self.conn.prepare(&format!(
            "SELECT {key}model, at_ms, status, tokens_in, cache_read, cache_write, tokens_out,
                    ttft_ms
             FROM {table} WHERE at_ms >= ?1 AND at_ms < ?2{kept}",
            key = by.key_columns(),
            table = self.scanned(range)?
        ))?;
        let model_at = by.key_width();
        let mut keys = Interner::default();
        let mut models = Interner::default();
        let mut sums: HashMap<(usize, usize), Sums> = HashMap::new();
        let mut times: HashMap<usize, Vec<u64>> = HashMap::new();
        let mut key = String::new();
        let mut rows = stmt.query(params_from_iter(&args))?;
        while let Some(r) = rows.next()? {
            by.write_key(r, model_at, offset_ms, &mut key)?;
            let key = keys.id(&key);
            let model = models.id(&text_at(r, model_at)?);
            let at = count(r, model_at + 1)?;
            sums.entry((key, model)).or_default().add(
                at,
                is_error(r, model_at + 2)?,
                r,
                model_at + 3,
            )?;
            if let Some(ms) = opt_count(r, model_at + 7)? {
                times.entry(key).or_default().push(ms);
            }
        }
        let names = if by == GroupBy::Day {
            self.day_names(&keys.names)?
        } else {
            keys.names
        };
        let sums = sums
            .into_iter()
            .map(|((key, model), sums)| {
                let key = names.get(key).cloned().unwrap_or_default();
                let model = models.names.get(model).cloned().unwrap_or_default();
                ((key, model), sums)
            })
            .collect();
        let times = times
            .into_iter()
            .map(|(key, times)| (names.get(key).cloned().unwrap_or_default(), times))
            .collect();
        Ok((sums, times))
    }

    /// Time to first token p50 and p95 over every row of `range` that has one (nearest rank).
    pub fn ttft_percentiles(
        &self,
        range: Range,
    ) -> Result<(Option<u64>, Option<u64>), HistoryError> {
        let parts = self.sliced(range, 1, |history, slice| {
            let mut stmt = history.conn.prepare(
                "SELECT ttft_ms FROM requests
                 WHERE at_ms >= ?1 AND at_ms < ?2 AND ttft_ms IS NOT NULL",
            )?;
            let mut rows = stmt.query([to_db(slice.from_ms), to_db(slice.to_ms)])?;
            let mut times = Vec::new();
            while let Some(r) = rows.next()? {
                times.push(count(r, 0)?);
            }
            Ok(times)
        })?;
        let mut times: Vec<u64> = parts.into_iter().flatten().collect();
        times.sort_unstable();
        Ok((nearest_rank(&times, 50), nearest_rank(&times, 95)))
    }

    /// The `YYYY-MM-DD` name of each day number in `numbers` (decimal text), in the same order.
    fn day_names(&self, numbers: &[String]) -> Result<Vec<String>, HistoryError> {
        let mut stmt = self
            .conn
            .prepare("SELECT strftime('%Y-%m-%d', ?1 * 86400, 'unixepoch')")?;
        numbers
            .iter()
            .map(|n| {
                let day: i64 = n.parse().unwrap_or(0);
                Ok(stmt.query_row([day], |r| r.get::<_, String>(0))?)
            })
            .collect()
    }

    /// `totals`, with each project key found in `aliases` replaced by its value and the groups
    /// that now share a key merged. Other groupings are returned as `totals` gives them.
    pub fn totals_with_aliases(
        &self,
        range: Range,
        by: GroupBy,
        aliases: &BTreeMap<String, String>,
        narrow: &Narrow,
    ) -> Result<Vec<Total>, HistoryError> {
        if by != GroupBy::Project || aliases.is_empty() {
            return self.totals_narrow(range, by, 0, narrow);
        }
        // A project asked for by its repository name also holds the rows of every alias of it.
        let mut projects = narrow.projects.clone();
        for (folder, repo) in aliases {
            if narrow.projects.contains(repo) {
                projects.push(folder.clone());
            }
        }
        let narrow = Narrow {
            sessions: narrow.sessions.clone(),
            projects,
        };
        let totals = self.totals_narrow(range, by, 0, &narrow)?;
        let mut merged: BTreeMap<String, Total> = BTreeMap::new();
        for mut total in totals {
            if let Some(repo) = aliases.get(&total.key) {
                total.key.clone_from(repo);
            }
            match merged.get_mut(&total.key) {
                Some(into) => into.absorb(total),
                None => {
                    merged.insert(total.key.clone(), total);
                }
            }
        }
        Ok(merged.into_values().collect())
    }

    /// Matching rows newest first, one page of them, and the count across all pages.
    ///
    /// Each slice of the range counts its matches and reads its newest `offset + limit` rows; the
    /// slices are consecutive in time, so the newest slice's rows come first.
    pub fn search(&self, filter: Filter, page: Page) -> Result<SearchResult, HistoryError> {
        let limit = page.limit.min(MAX_PAGE);
        let parts = self.sliced(filter.range, 1, |history, slice| {
            history.search_part(
                &Filter {
                    range: slice,
                    ..filter.clone()
                },
                limit.saturating_add(page.offset),
            )
        })?;
        let total = parts.iter().map(|(n, _)| n).sum();
        let rows = parts
            .into_iter()
            .rev()
            .flat_map(|(_, rows)| rows)
            .skip(usize::try_from(page.offset).unwrap_or(usize::MAX))
            .take(usize::try_from(limit).unwrap_or(usize::MAX))
            .collect();
        Ok(SearchResult { rows, total })
    }

    fn search_part(&self, filter: &Filter, take: u64) -> Result<(u64, Vec<Row>), HistoryError> {
        let (clause, mut args) = where_clause(filter);
        let total: i64 = self.conn.query_row(
            &format!("SELECT COUNT(*) FROM requests WHERE {clause}"),
            params_from_iter(args.iter()),
            |r| r.get(0),
        )?;
        args.push(Value::Integer(to_db(take)));
        let mut stmt = self.conn.prepare(&format!(
            "SELECT at_ms, session, project, agent, model, lane, status, reason, ttft_ms, dur_ms,
                    tokens_in, cache_read, cache_write, tokens_out, stop, upstream_id
             FROM requests WHERE {clause} ORDER BY at_ms DESC, id DESC LIMIT ?{limit}",
            limit = args.len()
        ))?;
        let rows = stmt
            .query_map(params_from_iter(args.iter()), |r| {
                Ok(Row {
                    at_ms: count(r, 0)?,
                    session: r.get(1)?,
                    project: r.get(2)?,
                    agent: r.get(3)?,
                    model: r.get(4)?,
                    lane: r.get(5)?,
                    status: r.get(6)?,
                    reason: r.get(7)?,
                    ttft_ms: opt_count(r, 8)?,
                    dur_ms: count(r, 9)?,
                    tokens_in: opt_count(r, 10)?,
                    cache_read: opt_count(r, 11)?,
                    cache_write: opt_count(r, 12)?,
                    tokens_out: opt_count(r, 13)?,
                    stop: r.get(14)?,
                    upstream_id: r.get(15)?,
                })
            })?
            .collect::<Result<Vec<_>, _>>()?;
        Ok((from_db(total), rows))
    }
}

/// The WHERE clause for a filter and its numbered arguments.
fn where_clause(filter: &Filter) -> (String, Vec<Value>) {
    let mut args = vec![
        Value::Integer(to_db(filter.range.from_ms)),
        Value::Integer(to_db(filter.range.to_ms)),
    ];
    let mut clause = String::from("at_ms >= ?1 AND at_ms < ?2");
    let equals = [
        ("model", &filter.model),
        ("lane", &filter.lane),
        ("session", &filter.session),
        ("agent", &filter.agent),
        ("project", &filter.project),
    ];
    for (column, wanted) in equals {
        if let Some(value) = wanted {
            args.push(Value::Text(value.clone()));
            clause.push_str(&format!(" AND {column} = ?{}", args.len()));
        }
    }
    match filter.status {
        StatusClass::Any => {}
        StatusClass::Ok => clause.push_str(" AND status > 0 AND status < 400"),
        StatusClass::Error => clause.push_str(&format!(" AND {IS_ERROR}")),
        StatusClass::ClientLeft => clause.push_str(" AND status = 499"),
    }
    (clause, args)
}

#[cfg(test)]
mod tests {
    use huddle_testkit::temp_home::TempHome;

    use crate::history::History;

    #[test]
    fn l57_the_bounds_query_seeks_both_ends_of_the_time_index() {
        let home = TempHome::new();
        let history = History::open(&home.root().join("scratch/history/huddle.db")).expect("open");
        let mut stmt = history
            .conn
            .prepare(&format!("EXPLAIN QUERY PLAN {}", super::BOUNDS))
            .expect("plan");
        let plan: Vec<String> = stmt
            .query_map([], |r| r.get(3))
            .expect("plan")
            .collect::<Result<_, _>>()
            .expect("plan");
        assert!(
            plan.iter().all(|step| !step.starts_with("SCAN requests")),
            "the bounds walk the table: {plan:?}"
        );
        assert_eq!(
            plan.iter()
                .filter(|step| step.starts_with("SEARCH requests"))
                .count(),
            2,
            "{plan:?}"
        );
    }
}
