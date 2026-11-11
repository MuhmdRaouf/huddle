//! `huddle provider <p> usage`: tokens and estimated cost per calendar window, read from the
//! request history database and priced at the catalog's list rates.

use std::collections::{BTreeMap, BTreeSet};
use std::io::Write;

use huddle_catalog::prices::{PRICES_RETRIEVED, TokenCounts, cost_usd, price_of};
use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_history::{GroupBy, History, ModelUsage, Range};
use huddle_router::clock::now_ms;

/// One calendar day, in milliseconds.
const DAY_MS: i64 = 86_400_000;

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

/// Prints the usage report. Exit 0 always; a missing database reads as empty.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let now = now_ms();
    let day = now.div_euclid(DAY_MS);
    let mut windows =
        [("Today", 1i64), ("Last 7 days", 7), ("Last 30 days", 30)].map(|(label, days)| Window {
            label,
            start: (day - days + 1) * DAY_MS,
            rows: BTreeMap::new(),
        });
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(err) => {
            let _ = writeln!(
                std::io::stderr(),
                "usage: no home to read the history from: {err}"
            );
            return 0;
        }
    };
    let db = paths.state_dir.join("huddle.db");
    if db.exists() {
        match History::open(&db) {
            Ok(history) => {
                for window in &mut windows {
                    fill(window, &history, provider.id, (day + 1) * DAY_MS);
                }
            }
            Err(err) => {
                let _ = writeln!(std::io::stderr(), "usage: cannot read the history: {err}");
            }
        }
    }
    let _ = writeln!(std::io::stdout(), "{}", report(provider, &windows));
    0
}

/// Reads one window's per-model counts for the lane `lane` from the history.
fn fill(window: &mut Window, history: &History, lane: &str, end: i64) {
    let range = Range {
        from_ms: u64::try_from(window.start).unwrap_or(0),
        to_ms: u64::try_from(end).unwrap_or(0),
    };
    let Ok(totals) = history.totals(range, GroupBy::Lane) else {
        return;
    };
    for usage in totals
        .into_iter()
        .filter(|total| total.key == lane)
        .flat_map(|total| total.models)
    {
        window.rows.insert(usage.model.clone(), row_of(&usage));
    }
}

/// The history stores the prompt total in `tok_in`; the price table wants the plain input.
fn row_of(usage: &ModelUsage) -> Row {
    Row {
        requests: usage.n,
        tokens: TokenCounts {
            input: usage
                .tok_in
                .saturating_sub(usage.cache_read + usage.cache_write),
            output: usage.tok_out,
            cache_read: usage.cache_read,
            cache_write: usage.cache_write,
        },
    }
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
