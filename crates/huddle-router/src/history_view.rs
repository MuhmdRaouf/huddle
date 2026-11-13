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

/// The preset ranges, as the page names them, each with its length.
const PRESETS: [(&str, u64); 6] = [
    ("15m", 15 * MINUTE),
    ("1h", HOUR),
    ("24h", DAY),
    ("7d", 7 * DAY),
    ("30d", 30 * DAY),
    ("90d", 90 * DAY),
];

/// Bucket widths from fine to coarse; a window takes the finest one that gives at most
/// `MAX_BUCKETS` buckets.
const WIDTHS: [u64; 12] = [
    30 * SECOND,
    MINUTE,
    5 * MINUTE,
    10 * MINUTE,
    15 * MINUTE,
    30 * MINUTE,
    HOUR,
    2 * HOUR,
    3 * HOUR,
    6 * HOUR,
    12 * HOUR,
    DAY,
];
const MAX_BUCKETS: u64 = 120;

/// One time window the page asks for: a preset or a custom from-to, cut to the kept history.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Window {
    name: &'static str,
    from_ms: u64,
    to_ms: u64,
    /// The start the window was cut to when it began before the kept history.
    clamped_from_ms: Option<u64>,
}

impl Window {
    /// The window from `from_ms` to `to_ms`, cut to start no earlier than `earliest_ms`.
    fn new(name: &'static str, from_ms: u64, to_ms: u64, earliest_ms: u64) -> Window {
        let clamped = from_ms < earliest_ms;
        Window {
            name,
            from_ms: from_ms.max(earliest_ms),
            to_ms,
            clamped_from_ms: clamped.then_some(earliest_ms),
        }
    }

    /// The bucket width: the finest step that keeps the window within `MAX_BUCKETS` buckets.
    pub fn width_ms(self) -> u64 {
        let length = self.to_ms.saturating_sub(self.from_ms);
        WIDTHS
            .iter()
            .copied()
            .find(|width| length.div_ceil(*width) <= MAX_BUCKETS)
            .unwrap_or(DAY)
    }

    fn range(self) -> Range {
        Range {
            from_ms: self.from_ms,
            to_ms: self.to_ms,
        }
    }
}

/// One parsed history question.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Query {
    Buckets {
        window: Window,
        session: Option<String>,
        project: Option<String>,
    },
    Totals(Window, GroupBy),
    Search {
        window: Window,
        filter: Filter,
        page: Page,
    },
    /// How much history the router keeps.
    Meta {
        keep_days: u64,
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

/// The window a query names: `from` and `to` together (custom), or a preset `range`
/// (24 h when neither is given). A start before the kept history is cut to it.
fn window_of(
    range: Option<&str>,
    from: Option<&str>,
    to: Option<&str>,
    now_ms: u64,
    keep_days: u64,
) -> Result<Window, Refusal> {
    let earliest = now_ms.saturating_sub(keep_days.saturating_mul(DAY));
    let end = now_ms + 1;
    let window = match (from, to) {
        (None, None) => {
            let (name, length) = match range {
                None => PRESETS[2],
                Some(text) => PRESETS
                    .iter()
                    .copied()
                    .find(|(name, _)| *name == text)
                    .ok_or_else(|| bad("range must be 15m, 1h, 24h, 7d, 30d or 90d"))?,
            };
            Window::new(name, now_ms.saturating_sub(length), end, earliest)
        }
        (Some(from), Some(to)) => {
            let whole = |text: &str| {
                text.parse::<u64>()
                    .map_err(|_| bad("from and to must be whole milliseconds"))
            };
            Window::new("custom", whole(from)?, whole(to)?.min(end), earliest)
        }
        _ => return Err(bad("from and to go together")),
    };
    if window.from_ms >= window.to_ms {
        return Err(bad("from must be before to, inside the kept history"));
    }
    Ok(window)
}

/// Parses `query` (the part after `?`) at time `now_ms`.
pub fn parse(query: &str, now_ms: u64, keep_days: u64) -> Result<Query, Refusal> {
    let pairs = pairs(query);
    let get = |name: &str| {
        pairs
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
            .filter(|value| !value.is_empty())
    };
    let window = window_of(get("range"), get("from"), get("to"), now_ms, keep_days)?;
    match get("view") {
        None => Err(bad("view is required: buckets, totals, search or meta")),
        Some("meta") => Ok(Query::Meta { keep_days }),
        Some("buckets") => Ok(Query::Buckets {
            window,
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
            Ok(Query::Totals(window, by))
        }
        Some("search") => {
            let mut filter = Filter::new(window.range());
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
            Ok(Query::Search {
                window,
                filter,
                page,
            })
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

/// What the cache reads saved: the same tokens at the full input rate minus what they cost.
fn cache_saved(model: &str, cache_read: u64) -> f64 {
    price_of(model).map_or(0.0, |p| {
        (cache_read as f64 / 1_000_000.0) * (p.input - p.cache_read)
    })
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

fn envelope(view: &str, window: Window) -> serde_json::Map<String, Value> {
    let Value::Object(mut map) = json!({
        "view": view,
        "range": window.name,
        "from_ms": window.from_ms,
        "to_ms": window.to_ms,
        "prices_retrieved": PRICES_RETRIEVED,
        "claude_prices_retrieved": CLAUDE_PRICES_RETRIEVED,
    }) else {
        return serde_json::Map::new();
    };
    if let Some(clamped) = window.clamped_from_ms {
        map.insert("clamped_from_ms".into(), json!(clamped));
    }
    map
}

/// The answer to `query` as JSON.
/// `aliases` maps an old folder name to the repository it belongs to, for project totals.
pub fn answer(
    history: &History,
    query: &Query,
    aliases: &BTreeMap<String, String>,
) -> Result<Value, HistoryError> {
    match query {
        Query::Buckets {
            window,
            session,
            project,
        } => {
            let buckets = history.buckets(
                window.range(),
                window.width_ms(),
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
            let mut out = envelope("buckets", *window);
            out.insert("width_ms".into(), json!(window.width_ms()));
            out.insert("buckets".into(), Value::Array(rows));
            Ok(Value::Object(out))
        }
        Query::Totals(window, by) => {
            let totals = history.totals_with_aliases(window.range(), *by, aliases)?;
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
                                "priced": price_of(&m.model).is_some(),
                                "cache_saved_usd": cache_saved(&m.model, m.cache_read),
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
                        "priced": t.models.iter().all(|m| price_of(&m.model).is_some()),
                        "cache_saved_usd": t
                            .models
                            .iter()
                            .map(|m| cache_saved(&m.model, m.cache_read))
                            .sum::<f64>(),
                        "models": models,
                    })
                })
                .collect();
            let mut out = envelope("totals", *window);
            out.insert("by".into(), json!(group_name(*by)));
            out.insert("totals".into(), Value::Array(rows));
            Ok(Value::Object(out))
        }
        Query::Search {
            window,
            filter,
            page,
        } => {
            let found = history.search(filter.clone(), *page)?;
            let rows: Vec<Value> = found.rows.iter().map(row_json).collect();
            let mut out = envelope("search", *window);
            out.insert("total".into(), json!(found.total));
            out.insert("limit".into(), json!(page.limit));
            out.insert("offset".into(), json!(page.offset));
            out.insert("rows".into(), Value::Array(rows));
            Ok(Value::Object(out))
        }
        Query::Meta { keep_days } => Ok(meta_answer(*keep_days)),
    }
}

fn meta_answer(keep_days: u64) -> Value {
    json!({ "view": "meta", "keep_days": keep_days })
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
pub fn empty_answer(query: &Query) -> Value {
    let (view, window) = match query {
        Query::Buckets { window, .. } => ("buckets", *window),
        Query::Totals(window, _) => ("totals", *window),
        Query::Search { window, .. } => ("search", *window),
        Query::Meta { keep_days } => return meta_answer(*keep_days),
    };
    let mut out = envelope(view, window);
    match query {
        Query::Buckets { window, .. } => {
            out.insert("width_ms".into(), json!(window.width_ms()));
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
        Query::Meta { .. } => {}
    }
    Value::Object(out)
}
