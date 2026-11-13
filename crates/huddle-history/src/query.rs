use std::collections::{BTreeMap, HashMap};

use crate::scan::{Interner, Sums, text_at};

use rusqlite::types::Value;
use rusqlite::{Row as SqlRow, params_from_iter};

use crate::error::HistoryError;
use crate::history::{History, from_db, to_db};
use crate::row::{Range, Row};

const MAX_PAGE: u64 = 500;

/// A request counts as an error on status 0 or 4xx/5xx, except 499 (the client left).
const IS_ERROR: &str = "(status = 0 OR (status >= 400 AND status != 499))";

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
    /// A UTC calendar day, `YYYY-MM-DD`.
    Day,
}

impl GroupBy {
    /// The key expression; an unknown session, agent or project groups under the empty key.
    /// A day is its number since the epoch; `totals` names it afterwards.
    fn key_column(self) -> &'static str {
        match self {
            GroupBy::Session => "COALESCE(session, '')",
            GroupBy::Project => "COALESCE(project, '')",
            GroupBy::Model => "model",
            GroupBy::Lane => "lane",
            GroupBy::Day => "at_ms / 86400000",
        }
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

/// Nearest-rank percentile of an ascending slice; `pct` in 1..=100.
fn nearest_rank(sorted: &[u64], pct: usize) -> Option<u64> {
    if sorted.is_empty() {
        return None;
    }
    let rank = (pct * sorted.len()).div_ceil(100).max(1);
    sorted.get(rank - 1).copied()
}

impl History {
    /// Per bucket start (a multiple of `width_ms`) and model: counts, token sums, TTFT percentiles.
    /// `session` and `project` narrow the rows to one session or one project.
    pub fn buckets(
        &self,
        range: Range,
        width_ms: u64,
        session: Option<&str>,
        project: Option<&str>,
    ) -> Result<Vec<Bucket>, HistoryError> {
        if width_ms == 0 {
            return Err(HistoryError::ZeroWidth);
        }
        let parts = self.sliced(range, width_ms, |history, slice| {
            history.buckets_part(slice, width_ms, session, project)
        })?;
        // The slices are disjoint and aligned to the width, so their buckets are already in order.
        Ok(parts.into_iter().flatten().collect())
    }

    fn buckets_part(
        &self,
        range: Range,
        width_ms: u64,
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
            "SELECT model, at_ms, {IS_ERROR}, tokens_in, cache_read, cache_write, tokens_out, ttft_ms
             FROM requests WHERE at_ms >= ?1 AND at_ms < ?2{narrow}"
        ))?;
        let mut models = Interner::default();
        let mut cells: HashMap<(u64, usize), (Sums, Vec<u64>)> = HashMap::new();
        let mut rows = stmt.query(params_from_iter(&args))?;
        while let Some(r) = rows.next()? {
            let model = models.id(&text_at(r, 0)?);
            let start = count(r, 1)? / width_ms * width_ms;
            let (sums, ttft) = cells.entry((start, model)).or_default();
            sums.add(r, 1)?;
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
        let parts = self.sliced(range, 1, |history, slice| history.totals_part(slice, by))?;
        let mut merged: BTreeMap<String, BTreeMap<String, Sums>> = BTreeMap::new();
        for ((key, model), sums) in parts.into_iter().flatten() {
            merged
                .entry(key)
                .or_default()
                .entry(model)
                .or_default()
                .merge(&sums);
        }
        Ok(merged
            .into_iter()
            .map(|(key, per_model)| {
                let mut total = Total {
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
    ) -> Result<HashMap<(String, String), Sums>, HistoryError> {
        let mut stmt = self.conn.prepare(&format!(
            "SELECT {key}, model, at_ms, {IS_ERROR}, tokens_in, cache_read, cache_write, tokens_out
             FROM requests WHERE at_ms >= ?1 AND at_ms < ?2",
            key = by.key_column()
        ))?;
        let mut keys = Interner::default();
        let mut models = Interner::default();
        let mut sums: HashMap<(usize, usize), Sums> = HashMap::new();
        let mut rows = stmt.query([to_db(range.from_ms), to_db(range.to_ms)])?;
        while let Some(r) = rows.next()? {
            let key = keys.id(&text_at(r, 0)?);
            let model = models.id(&text_at(r, 1)?);
            sums.entry((key, model)).or_default().add(r, 2)?;
        }
        let names = if by == GroupBy::Day {
            self.day_names(&keys.names)?
        } else {
            keys.names
        };
        Ok(sums
            .into_iter()
            .map(|((key, model), sums)| {
                let key = names.get(key).cloned().unwrap_or_default();
                let model = models.names.get(model).cloned().unwrap_or_default();
                ((key, model), sums)
            })
            .collect())
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
    ) -> Result<Vec<Total>, HistoryError> {
        let totals = self.totals(range, by)?;
        if by != GroupBy::Project || aliases.is_empty() {
            return Ok(totals);
        }
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
