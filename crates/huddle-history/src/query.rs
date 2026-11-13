use std::collections::BTreeMap;

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
    fn expr(self) -> &'static str {
        match self {
            GroupBy::Session => "COALESCE(session, '')",
            GroupBy::Project => "COALESCE(project, '')",
            GroupBy::Model => "model",
            GroupBy::Lane => "lane",
            GroupBy::Day => "strftime('%Y-%m-%d', at_ms / 1000, 'unixepoch')",
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

fn count(r: &SqlRow<'_>, i: usize) -> rusqlite::Result<u64> {
    Ok(from_db(r.get(i)?))
}

fn opt_count(r: &SqlRow<'_>, i: usize) -> rusqlite::Result<Option<u64>> {
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
        let width = to_db(width_ms);
        let mut args = vec![
            Value::Integer(to_db(range.from_ms)),
            Value::Integer(to_db(range.to_ms)),
            Value::Integer(width),
        ];
        let mut narrow = String::new();
        for (column, wanted) in [("session", session), ("project", project)] {
            if let Some(value) = wanted {
                args.push(Value::Text(value.to_owned()));
                narrow.push_str(&format!(" AND {column} = ?{}", args.len()));
            }
        }
        let mut stmt = self.conn.prepare(&format!(
            "SELECT (at_ms / ?3) * ?3 AS start, model, COUNT(*),
                    COALESCE(SUM({IS_ERROR}), 0),
                    COALESCE(SUM(tokens_in), 0), COALESCE(SUM(cache_read), 0),
                    COALESCE(SUM(cache_write), 0), COALESCE(SUM(tokens_out), 0)
             FROM requests WHERE at_ms >= ?1 AND at_ms < ?2{narrow}
             GROUP BY start, model ORDER BY start, model"
        ))?;
        let mut buckets = stmt
            .query_map(params_from_iter(&args), |r| {
                Ok(Bucket {
                    start_ms: count(r, 0)?,
                    model: r.get(1)?,
                    n: count(r, 2)?,
                    err: count(r, 3)?,
                    tok_in: count(r, 4)?,
                    cache_read: count(r, 5)?,
                    cache_write: count(r, 6)?,
                    tok_out: count(r, 7)?,
                    ttft_p50: None,
                    ttft_p95: None,
                })
            })?
            .collect::<Result<Vec<_>, _>>()?;

        let mut ttft = self.conn.prepare(&format!(
            "SELECT (at_ms / ?3) * ?3, model, ttft_ms FROM requests
             WHERE at_ms >= ?1 AND at_ms < ?2{narrow} AND ttft_ms IS NOT NULL ORDER BY ttft_ms"
        ))?;
        let mut samples: BTreeMap<(u64, String), Vec<u64>> = BTreeMap::new();
        let found = ttft.query_map(params_from_iter(&args), |r| {
            Ok((count(r, 0)?, r.get::<_, String>(1)?, count(r, 2)?))
        })?;
        for sample in found {
            let (start, model, ms) = sample?;
            samples.entry((start, model)).or_default().push(ms);
        }
        for bucket in &mut buckets {
            if let Some(sorted) = samples.get(&(bucket.start_ms, bucket.model.clone())) {
                bucket.ttft_p50 = nearest_rank(sorted, 50);
                bucket.ttft_p95 = nearest_rank(sorted, 95);
            }
        }
        Ok(buckets)
    }

    /// Per key, ordered by key: counts, token sums, first and last request, and the per-model sums.
    pub fn totals(&self, range: Range, by: GroupBy) -> Result<Vec<Total>, HistoryError> {
        let mut stmt = self.conn.prepare(&format!(
            "SELECT {key} AS k, model, COUNT(*), COALESCE(SUM({IS_ERROR}), 0),
                    COALESCE(SUM(tokens_in), 0), COALESCE(SUM(cache_read), 0),
                    COALESCE(SUM(cache_write), 0), COALESCE(SUM(tokens_out), 0),
                    MIN(at_ms), MAX(at_ms)
             FROM requests WHERE at_ms >= ?1 AND at_ms < ?2
             GROUP BY k, model ORDER BY k, model",
            key = by.expr()
        ))?;
        let groups = stmt.query_map([to_db(range.from_ms), to_db(range.to_ms)], |r| {
            let usage = ModelUsage {
                model: r.get(1)?,
                n: count(r, 2)?,
                tok_in: count(r, 4)?,
                cache_read: count(r, 5)?,
                cache_write: count(r, 6)?,
                tok_out: count(r, 7)?,
            };
            Ok((
                r.get::<_, String>(0)?,
                usage,
                count(r, 3)?,
                count(r, 8)?,
                count(r, 9)?,
            ))
        })?;
        let mut totals: Vec<Total> = Vec::new();
        for group in groups {
            let (key, usage, err, first, last) = group?;
            match totals.last_mut().filter(|t| t.key == key) {
                Some(total) => {
                    total.n += usage.n;
                    total.err += err;
                    total.tok_in += usage.tok_in;
                    total.cache_read += usage.cache_read;
                    total.cache_write += usage.cache_write;
                    total.tok_out += usage.tok_out;
                    total.first_ms = total.first_ms.min(first);
                    total.last_ms = total.last_ms.max(last);
                    total.models.push(usage);
                }
                None => totals.push(Total {
                    key,
                    n: usage.n,
                    err,
                    tok_in: usage.tok_in,
                    cache_read: usage.cache_read,
                    cache_write: usage.cache_write,
                    tok_out: usage.tok_out,
                    first_ms: first,
                    last_ms: last,
                    models: vec![usage],
                }),
            }
        }
        Ok(totals)
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
    pub fn search(&self, filter: Filter, page: Page) -> Result<SearchResult, HistoryError> {
        let (clause, mut args) = where_clause(&filter);
        let total: i64 = self.conn.query_row(
            &format!("SELECT COUNT(*) FROM requests WHERE {clause}"),
            params_from_iter(args.iter()),
            |r| r.get(0),
        )?;
        args.push(Value::Integer(to_db(page.limit.min(MAX_PAGE))));
        args.push(Value::Integer(to_db(page.offset)));
        let mut stmt = self.conn.prepare(&format!(
            "SELECT at_ms, session, project, agent, model, lane, status, reason, ttft_ms, dur_ms,
                    tokens_in, cache_read, cache_write, tokens_out, stop, upstream_id
             FROM requests WHERE {clause} ORDER BY at_ms DESC, id DESC
             LIMIT ?{limit} OFFSET ?{offset}",
            limit = args.len() - 1,
            offset = args.len()
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
        Ok(SearchResult {
            rows,
            total: from_db(total),
        })
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
