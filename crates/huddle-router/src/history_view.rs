//! huddle-router: the history endpoint.
//!
//! `parse` turns a query string into a typed request, `answer` runs it against an open
//! database and prices the token sums. Neither touches the async runtime: the caller runs
//! them on a blocking thread.

use std::collections::BTreeMap;

use huddle_catalog::prices::{
    CLAUDE_PRICES_RETRIEVED, PRICES_RETRIEVED, TokenCounts, cost_usd, is_api_equivalent, price_of,
};
use huddle_history::{Filter, GroupBy, History, HistoryError, Page, Range, Row, StatusClass};
use serde_json::{Value, json};

const SECOND: u64 = 1_000;
const MINUTE: u64 = 60 * SECOND;
const HOUR: u64 = 60 * MINUTE;
const DAY: u64 = 24 * HOUR;

/// The most rows one search page holds.
pub const MAX_LIMIT: u64 = 500;
const DEFAULT_LIMIT: u64 = 100;

/// The time windows the page asks for, each with its bucket width.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Span {
    QuarterHour,
    Day,
    Week,
    Month,
    Quarter,
}

impl Span {
    fn parse(text: &str) -> Option<Span> {
        match text {
            "15m" => Some(Span::QuarterHour),
            "24h" => Some(Span::Day),
            "7d" => Some(Span::Week),
            "30d" => Some(Span::Month),
            "90d" => Some(Span::Quarter),
            _ => None,
        }
    }

    fn name(self) -> &'static str {
        match self {
            Span::QuarterHour => "15m",
            Span::Day => "24h",
            Span::Week => "7d",
            Span::Month => "30d",
            Span::Quarter => "90d",
        }
    }

    fn length_ms(self) -> u64 {
        match self {
            Span::QuarterHour => 15 * MINUTE,
            Span::Day => DAY,
            Span::Week => 7 * DAY,
            Span::Month => 30 * DAY,
            Span::Quarter => 90 * DAY,
        }
    }

    pub fn width_ms(self) -> u64 {
        match self {
            Span::QuarterHour => 30 * SECOND,
            Span::Day => 15 * MINUTE,
            Span::Week => HOUR,
            Span::Month | Span::Quarter => DAY,
        }
    }

    fn range(self, now_ms: u64) -> Range {
        Range {
            from_ms: now_ms.saturating_sub(self.length_ms()),
            to_ms: now_ms + 1,
        }
    }
}

/// One parsed history question.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Query {
    Buckets {
        span: Span,
        session: Option<String>,
        project: Option<String>,
    },
    Totals(Span, GroupBy),
    Search {
        span: Span,
        filter: Filter,
        page: Page,
    },
}

/// Why a request did not parse.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refusal {
    /// 400, with the one-line reason.
    Bad(String),
    /// 404: no such view.
    UnknownView,
}

/// The pairs of a query string, percent-decoded.
fn pairs(query: &str) -> Vec<(String, String)> {
    query
        .split('&')
        .filter(|part| !part.is_empty())
        .map(|part| {
            let (key, value) = part.split_once('=').unwrap_or((part, ""));
            (decode(key), decode(value))
        })
        .collect()
}

fn decode(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while let Some(&byte) = bytes.get(i) {
        let hex = |at: usize| bytes.get(at).and_then(|b| (*b as char).to_digit(16));
        match byte {
            b'+' => out.push(b' '),
            b'%' => match (hex(i + 1), hex(i + 2)) {
                (Some(high), Some(low)) => {
                    out.push((high * 16 + low) as u8);
                    i += 2;
                }
                _ => out.push(byte),
            },
            _ => out.push(byte),
        }
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn bad(reason: impl Into<String>) -> Refusal {
    Refusal::Bad(reason.into())
}

/// Parses `query` (the part after `?`) at time `now_ms`.
pub fn parse(query: &str, now_ms: u64) -> Result<Query, Refusal> {
    let pairs = pairs(query);
    let get = |name: &str| {
        pairs
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
            .filter(|value| !value.is_empty())
    };
    let span = match get("range") {
        None => Span::Day,
        Some(text) => {
            Span::parse(text).ok_or_else(|| bad("range must be 15m, 24h, 7d, 30d or 90d"))?
        }
    };
    match get("view") {
        None => Err(bad("view is required: buckets, totals or search")),
        Some("buckets") => Ok(Query::Buckets {
            span,
            session: get("session").map(str::to_owned),
            project: get("project").map(str::to_owned),
        }),
        Some("totals") => {
            let by = match get("by") {
                None => GroupBy::Model,
                Some("session") => GroupBy::Session,
                Some("project") => GroupBy::Project,
                Some("model") => GroupBy::Model,
                Some("lane") => GroupBy::Lane,
                Some("day") => GroupBy::Day,
                Some(_) => return Err(bad("by must be session, project, model, lane or day")),
            };
            Ok(Query::Totals(span, by))
        }
        Some("search") => {
            let mut filter = Filter::new(span.range(now_ms));
            let text = |name| get(name).map(str::to_owned);
            filter.model = text("model");
            filter.lane = text("lane");
            filter.session = text("session");
            filter.agent = text("agent");
            filter.project = text("project");
            filter.status = match get("status") {
                None | Some("any") => StatusClass::Any,
                Some("ok") => StatusClass::Ok,
                Some("error") => StatusClass::Error,
                Some("left") => StatusClass::ClientLeft,
                Some(_) => return Err(bad("status must be any, ok, error or left")),
            };
            let number = |name: &str, default: u64| match get(name) {
                None => Ok(default),
                Some(text) => text
                    .parse::<u64>()
                    .map_err(|_| bad(format!("{name} must be a whole number"))),
            };
            let page = Page {
                limit: number("limit", DEFAULT_LIMIT)?.min(MAX_LIMIT),
                offset: number("offset", 0)?,
            };
            Ok(Query::Search { span, filter, page })
        }
        Some(_) => Err(Refusal::UnknownView),
    }
}

fn price(model: &str, input: u64, output: u64, cache_read: u64, cache_write: u64) -> f64 {
    cost_usd(
        model,
        &TokenCounts {
            input,
            output,
            cache_read,
            cache_write,
        },
    )
}

/// Token counts as stored: the input total holds the cache reads and writes.
fn cost_of_sums(model: &str, tok_in: u64, cache_read: u64, cache_write: u64, out: u64) -> f64 {
    let fresh = tok_in.saturating_sub(cache_read + cache_write);
    price(model, fresh, out, cache_read, cache_write)
}

/// A row's cost; unknown when any count is unknown or the model has no price.
fn row_cost(row: &Row) -> Option<f64> {
    price_of(&row.model)?;
    let total = row.tokens_in?;
    let out = row.tokens_out?;
    Some(cost_of_sums(
        &row.model,
        total,
        row.cache_read.unwrap_or(0),
        row.cache_write.unwrap_or(0),
        out,
    ))
}

fn envelope(view: &str, span: Span, now_ms: u64) -> serde_json::Map<String, Value> {
    let range = span.range(now_ms);
    let Value::Object(map) = json!({
        "view": view,
        "range": span.name(),
        "from_ms": range.from_ms,
        "to_ms": range.to_ms,
        "prices_retrieved": PRICES_RETRIEVED,
        "claude_prices_retrieved": CLAUDE_PRICES_RETRIEVED,
    }) else {
        return serde_json::Map::new();
    };
    map
}

/// The answer to `query` as JSON.
/// `aliases` maps an old folder name to the repository it belongs to, for project totals.
pub fn answer(
    history: &History,
    query: &Query,
    now_ms: u64,
    aliases: &BTreeMap<String, String>,
) -> Result<Value, HistoryError> {
    match query {
        Query::Buckets {
            span,
            session,
            project,
        } => {
            let buckets = history.buckets(
                span.range(now_ms),
                span.width_ms(),
                session.as_deref(),
                project.as_deref(),
            )?;
            let rows: Vec<Value> = buckets
                .iter()
                .map(|b| {
                    json!({
                        "start_ms": b.start_ms, "model": b.model, "n": b.n, "err": b.err,
                        "tok_in": b.tok_in, "cache_read": b.cache_read,
                        "cache_write": b.cache_write, "tok_out": b.tok_out,
                        "ttft_p50": b.ttft_p50, "ttft_p95": b.ttft_p95,
                        "cost_usd": cost_of_sums(&b.model, b.tok_in, b.cache_read, b.cache_write, b.tok_out),
                    })
                })
                .collect();
            let mut out = envelope("buckets", *span, now_ms);
            out.insert("width_ms".into(), json!(span.width_ms()));
            out.insert("buckets".into(), Value::Array(rows));
            Ok(Value::Object(out))
        }
        Query::Totals(span, by) => {
            let totals = history.totals_with_aliases(span.range(now_ms), *by, aliases)?;
            let rows: Vec<Value> = totals
                .iter()
                .map(|t| {
                    let models: Vec<Value> = t
                        .models
                        .iter()
                        .map(|m| {
                            json!({
                                "model": m.model, "n": m.n, "tok_in": m.tok_in,
                                "cache_read": m.cache_read, "cache_write": m.cache_write,
                                "tok_out": m.tok_out,
                                "cost_usd": cost_of_sums(&m.model, m.tok_in, m.cache_read, m.cache_write, m.tok_out),
                            })
                        })
                        .collect();
                    let cost: f64 = t
                        .models
                        .iter()
                        .map(|m| cost_of_sums(&m.model, m.tok_in, m.cache_read, m.cache_write, m.tok_out))
                        .sum();
                    json!({
                        "key": t.key, "n": t.n, "err": t.err, "tok_in": t.tok_in,
                        "cache_read": t.cache_read, "cache_write": t.cache_write,
                        "tok_out": t.tok_out, "first_ms": t.first_ms, "last_ms": t.last_ms,
                        "cost_usd": cost,
                        "api_equivalent": t.models.iter().any(|m| is_api_equivalent(&m.model)),
                        "models": models,
                    })
                })
                .collect();
            let mut out = envelope("totals", *span, now_ms);
            out.insert("by".into(), json!(group_name(*by)));
            out.insert("totals".into(), Value::Array(rows));
            Ok(Value::Object(out))
        }
        Query::Search { span, filter, page } => {
            let found = history.search(filter.clone(), *page)?;
            let rows: Vec<Value> = found.rows.iter().map(row_json).collect();
            let mut out = envelope("search", *span, now_ms);
            out.insert("total".into(), json!(found.total));
            out.insert("limit".into(), json!(page.limit));
            out.insert("offset".into(), json!(page.offset));
            out.insert("rows".into(), Value::Array(rows));
            Ok(Value::Object(out))
        }
    }
}

fn group_name(by: GroupBy) -> &'static str {
    match by {
        GroupBy::Session => "session",
        GroupBy::Project => "project",
        GroupBy::Model => "model",
        GroupBy::Lane => "lane",
        GroupBy::Day => "day",
    }
}

fn row_json(row: &Row) -> Value {
    json!({
        "at_ms": row.at_ms, "session": row.session, "project": row.project,
        "agent": row.agent, "model": row.model, "lane": row.lane, "status": row.status,
        "reason": row.reason, "ttft_ms": row.ttft_ms, "dur_ms": row.dur_ms,
        "tokens_in": row.tokens_in, "cache_read": row.cache_read,
        "cache_write": row.cache_write, "tokens_out": row.tokens_out,
        "stop": row.stop, "upstream_id": row.upstream_id,
        "cost_usd": row_cost(row),
    })
}

/// The answer with no database: the same shape, no rows.
pub fn empty_answer(query: &Query, now_ms: u64) -> Value {
    let (view, span) = match query {
        Query::Buckets { span, .. } => ("buckets", *span),
        Query::Totals(span, _) => ("totals", *span),
        Query::Search { span, .. } => ("search", *span),
    };
    let mut out = envelope(view, span, now_ms);
    match query {
        Query::Buckets { span, .. } => {
            out.insert("width_ms".into(), json!(span.width_ms()));
            out.insert("buckets".into(), json!([]));
        }
        Query::Totals(_, by) => {
            out.insert("by".into(), json!(group_name(*by)));
            out.insert("totals".into(), json!([]));
        }
        Query::Search { page, .. } => {
            out.insert("total".into(), json!(0));
            out.insert("limit".into(), json!(page.limit));
            out.insert("offset".into(), json!(page.offset));
            out.insert("rows".into(), json!([]));
        }
    }
    Value::Object(out)
}
