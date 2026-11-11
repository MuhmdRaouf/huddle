//! `huddle provider <p> usage`: tokens and estimated cost per calendar window, read from the
//! router's route events and priced at the catalog's list rates.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::io::Write;

use huddle_catalog::prices::{PRICES_RETRIEVED, TokenCounts, cost_usd, price_of};
use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_router::events::{now_ms, utc_date};

/// One calendar day, in milliseconds.
const DAY_MS: i64 = 86_400_000;

/// How many days of event files the report reads back.
const READ_DAYS: i64 = 30;

/// One report window: its label, its inclusive start at UTC midnight, and its rows per model.
struct Window {
    label: &'static str,
    start: i64,
    rows: BTreeMap<String, Row>,
}

/// One model's requests and token counts inside a window.
#[derive(Default)]
struct Row {
    requests: u64,
    tokens: TokenCounts,
}

impl Window {
    /// The window's estimated cost: the sum of its rows' costs. An empty window folds from 0,
    /// where `sum` would answer negative zero and print `$-0.0000`.
    fn cost(&self) -> f64 {
        self.rows
            .iter()
            .map(|(model, row)| cost_usd(model, &row.tokens))
            .fold(0.0, |sum, cost| sum + cost)
    }
}

/// Prints the usage report. Exit 0 always; a missing events directory reads as empty.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let now = now_ms();
    let day = now.div_euclid(DAY_MS);
    let mut windows =
        [("Today", 1i64), ("Last 7 days", 7), ("Last 30 days", 30)].map(|(label, days)| Window {
            label,
            start: (day - days + 1) * DAY_MS,
            rows: BTreeMap::new(),
        });
    let events = match Paths::from_env(env) {
        Ok(paths) => paths.state_dir.join("events"),
        Err(err) => {
            let _ = writeln!(
                std::io::stderr(),
                "usage: no home to read events from: {err}"
            );
            return 0;
        }
    };
    for back in 0..READ_DAYS {
        let path = events.join(format!("{}.jsonl", utc_date((day - back) * DAY_MS)));
        let Ok(text) = fs::read_to_string(path) else {
            continue;
        };
        for line in text.lines() {
            count_line(&mut windows, provider.id, line);
        }
    }
    let _ = writeln!(std::io::stdout(), "{}", report(provider, &windows));
    0
}

/// Adds one event line to every window it falls in. A line that does not parse is skipped; only
/// this provider's `provider`-route requests count; an unreadable `ts` counts in every window.
fn count_line(windows: &mut [Window; 3], plugin: &str, line: &str) {
    let Ok(event) = serde_json::from_str::<serde_json::Value>(line) else {
        return;
    };
    if str_field(&event, "plugin") != Some(plugin) || str_field(&event, "route") != Some("provider")
    {
        return;
    }
    let ts = str_field(&event, "ts").and_then(ts_ms);
    let usage = event.get("usage");
    let tokens = TokenCounts {
        input: token_count(usage, "input_tokens"),
        output: token_count(usage, "output_tokens"),
        cache_read: token_count(usage, "cache_read_input_tokens"),
        cache_write: token_count(usage, "cache_creation_input_tokens"),
    };
    let model = str_field(&event, "model").unwrap_or_default();
    for window in windows {
        if in_window(ts, window.start) {
            let row = window.rows.entry(model.to_string()).or_default();
            row.requests += 1;
            row.tokens.input += tokens.input;
            row.tokens.output += tokens.output;
            row.tokens.cache_read += tokens.cache_read;
            row.tokens.cache_write += tokens.cache_write;
        }
    }
}

/// A string field of the event; missing or not a string reads as none.
fn str_field<'a>(event: &'a serde_json::Value, key: &str) -> Option<&'a str> {
    event.get(key).and_then(|value| value.as_str())
}

/// One usage counter as a non-negative integer; anything else counts as nothing.
fn token_count(usage: Option<&serde_json::Value>, key: &str) -> u64 {
    usage
        .and_then(|usage| usage.get(key))
        .and_then(|value| value.as_u64())
        .unwrap_or(0)
}

/// A window covers its start and everything after it; an unreadable `ts` falls in every window.
fn in_window(ts: Option<i64>, start: i64) -> bool {
    ts.is_none_or(|ts| ts >= start)
}

/// Epoch milliseconds of a route event's `ts`, in the fixed shape the router writes:
/// `2026-11-09T10:15:30.123Z`. None when it does not parse.
fn ts_ms(ts: &str) -> Option<i64> {
    let bytes = ts.as_bytes();
    if bytes.len() != 24
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || bytes[10] != b'T'
        || bytes[13] != b':'
        || bytes[16] != b':'
        || bytes[19] != b'.'
        || bytes[23] != b'Z'
    {
        return None;
    }
    let year = digits(bytes, 0, 4)?;
    let month = digits(bytes, 5, 7)?;
    let day = digits(bytes, 8, 10)?;
    let hour = digits(bytes, 11, 13)?;
    let minute = digits(bytes, 14, 16)?;
    let second = digits(bytes, 17, 19)?;
    let millis = digits(bytes, 20, 23)?;
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 60
        || millis > 999
    {
        return None;
    }
    Some(
        (days_from_civil(year, month, day) * 86_400 + hour * 3_600 + minute * 60 + second) * 1_000
            + millis,
    )
}

/// The digits between two offsets, as one number; None when a byte is not a digit.
fn digits(bytes: &[u8], from: usize, to: usize) -> Option<i64> {
    let mut value = 0i64;
    for &byte in &bytes[from..to] {
        if !byte.is_ascii_digit() {
            return None;
        }
        value = value * 10 + i64::from(byte - b'0');
    }
    Some(value)
}

/// Days from 1970-01-01 for a civil date.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = if year >= 0 { year } else { year - 399 } / 400;
    let year_of_era = year - era * 400;
    let month_from_march = (month + 9) % 12;
    let day_of_year = (153 * month_from_march + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

/// The whole report, one line per element.
fn report(provider: &Provider, windows: &[Window; 3]) -> String {
    let mut lines = vec![
        format!("{} usage", provider.display),
        format!(
            "Est. cost: today {} \u{b7} 7 days {} \u{b7} 30 days {}",
            usd(windows[0].cost()),
            usd(windows[1].cost()),
            usd(windows[2].cost()),
        ),
        format!(
            "Estimated from {} list prices read {PRICES_RETRIEVED}; your bill is the provider's. Token counts are exact.",
            provider.display
        ),
    ];
    let models: BTreeSet<&str> = windows
        .iter()
        .flat_map(|window| window.rows.keys().map(String::as_str))
        .collect();
    let unpriced: Vec<&str> = models
        .iter()
        .copied()
        .filter(|model| price_of(model).is_none())
        .collect();
    if !unpriced.is_empty() {
        lines.push(format!(
            "No list price for {}: counted as $0.",
            unpriced.join(", ")
        ));
    }
    let unverified: Vec<&str> = models
        .iter()
        .copied()
        .filter(|model| price_of(model).is_some_and(|price| !price.verified))
        .collect();
    if !unverified.is_empty() {
        lines.push(format!(
            "Cache rates unverified for {}.",
            unverified.join(", ")
        ));
    }
    lines.push(String::new());
    for window in windows {
        if window.rows.is_empty() {
            lines.push(format!("{}: no agent or /model requests.", window.label));
            continue;
        }
        lines.push(format!("{}, agents and /model", window.label));
        lines.extend(table(window));
    }
    lines.join("\n")
}

/// One window's table: a row per model, then the window's total.
fn table(window: &Window) -> Vec<String> {
    let mut total = Row::default();
    let mut total_cost = 0.0;
    let mut rows = vec![header()];
    for (model, row) in &window.rows {
        let cost = cost_usd(model, &row.tokens);
        rows.push(cells(model, row.requests, &row.tokens, cost));
        total.requests += row.requests;
        total.tokens.input += row.tokens.input;
        total.tokens.output += row.tokens.output;
        total.tokens.cache_read += row.tokens.cache_read;
        total.tokens.cache_write += row.tokens.cache_write;
        total_cost += cost;
    }
    rows.push(cells("TOTAL", total.requests, &total.tokens, total_cost));
    pad(rows)
}

/// The table's column titles.
fn header() -> Vec<String> {
    [
        "MODEL", "REQ", "IN", "OUT", "CACHE R", "CACHE W", "EST COST",
    ]
    .map(str::to_string)
    .to_vec()
}

/// One table row's cells.
fn cells(model: &str, requests: u64, tokens: &TokenCounts, cost: f64) -> Vec<String> {
    vec![
        model.to_string(),
        requests.to_string(),
        count(tokens.input),
        count(tokens.output),
        count(tokens.cache_read),
        count(tokens.cache_write),
        usd(cost),
    ]
}

/// Columns padded to their widest cell, two spaces apart; the last column is not padded.
fn pad(rows: Vec<Vec<String>>) -> Vec<String> {
    let mut widths = [0usize; 7];
    for row in &rows {
        for (column, cell) in row.iter().enumerate() {
            widths[column] = widths[column].max(cell.chars().count());
        }
    }
    rows.iter()
        .map(|row| {
            row.iter()
                .enumerate()
                .map(|(column, cell)| {
                    if column + 1 == row.len() {
                        cell.clone()
                    } else {
                        format!("{:width$}", cell, width = widths[column])
                    }
                })
                .collect::<Vec<String>>()
                .join("  ")
        })
        .collect()
}

/// A count as the tables print it: the integer below 1,000, one decimal and `k` below a million,
/// one decimal and `M` above.
fn count(value: u64) -> String {
    if value < 1_000 {
        value.to_string()
    } else if value < 1_000_000 {
        format!("{:.1}k", value as f64 / 1_000.0)
    } else {
        format!("{:.1}M", value as f64 / 1_000_000.0)
    }
}

/// A dollar amount: two decimals from 1 up, four below.
fn usd(amount: f64) -> String {
    if amount >= 1.0 {
        format!("${amount:.2}")
    } else {
        format!("${amount:.4}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_router::events::iso_utc_ms;

    #[test]
    fn ts_ms_reads_the_stamp_the_router_writes() {
        for ms in [0, 1, -1, 1_794_219_330_123] {
            assert_eq!(ts_ms(&iso_utc_ms(ms)), Some(ms));
        }
        assert_eq!(ts_ms("not a stamp"), None);
        assert_eq!(ts_ms("2026-11-09T10:15:30Z"), None);
    }

    #[test]
    fn a_window_covers_its_start_and_an_unreadable_stamp() {
        let start = 1_794_219_330_000;
        assert!(in_window(Some(start), start));
        assert!(!in_window(Some(start - 1), start));
        assert!(in_window(None, start));
    }
}
