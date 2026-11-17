//! L72: the 90-day views on a database written and pruned day by day for 180 days, next to the
//! same rows written in one go. Run by name in a release build; it prints a table and asserts
//! only that both databases answer every view the same.
//!
//! cargo test --release -p huddle-history --test l72_pruned_database -- --ignored --nocapture --test-threads=1

mod common;

use std::time::{Duration, Instant};

use common::{DAY, db_path, row};
use huddle_history::{Filter, GroupBy, History, Page, Range, Row, StatusClass};
use huddle_testkit::temp_home::TempHome;
use rusqlite::Connection;

const START: u64 = 1_800_000_000_000;
const DAYS: u64 = 180;
const KEEP_DAYS: u64 = 90;
const PER_DAY: u64 = 11_112;
const RUNS: usize = 5;
const LANES: [&str; 6] = ["anthropic", "zai", "kimi", "deepseek", "minimax", "qwen"];

/// Row `i` of the whole 180 days: `PER_DAY` rows a day, spread over the day.
fn generated(i: u64) -> Row {
    let at = START + (i / PER_DAY) * DAY + (i % PER_DAY) * (DAY / PER_DAY);
    let mut r = row(at, &format!("model-{}", i % 12));
    r.lane = LANES[(i % 6) as usize].to_string();
    r.session = Some(format!("session-{}", i % 30));
    r.project = Some(format!("project-{}", i % 40));
    r.agent = Some(format!("agent-{}", i % 7));
    r.status = if i.is_multiple_of(50) { 429 } else { 200 };
    r.ttft_ms = Some(100 + (i * 7919) % 4000);
    r.dur_ms = 500 + (i * 31) % 9000;
    r.tokens_in = Some(1000 + i % 500);
    r.cache_read = Some(i % 400);
    r.cache_write = Some(i % 50);
    r.tokens_out = Some(200 + i % 300);
    r
}

/// Every day inserts its rows, then the product's own prune runs at the end of that day.
fn pruned_daily(home: &TempHome) {
    let mut history = History::open(&db_path(home)).expect("open");
    for day in 0..DAYS {
        let rows: Vec<Row> = (day * PER_DAY..(day + 1) * PER_DAY)
            .map(generated)
            .collect();
        history.insert(&rows).expect("insert");
        history
            .prune(KEEP_DAYS, START + (day + 1) * DAY)
            .expect("prune");
    }
}

/// The rows the pruned database keeps, written in order in big chunks.
fn fresh(home: &TempHome) {
    let mut history = History::open(&db_path(home)).expect("open");
    let (first, end) = ((DAYS - KEEP_DAYS) * PER_DAY, DAYS * PER_DAY);
    let mut next = first;
    while next < end {
        let stop = (next + 50_000).min(end);
        let rows: Vec<Row> = (next..stop).map(generated).collect();
        history.insert(&rows).expect("insert");
        next = stop;
    }
}

struct Shape {
    bytes: u64,
    pages: i64,
    free: i64,
    rows: i64,
}

fn shape(home: &TempHome) -> Shape {
    let path = db_path(home);
    let conn = Connection::open(&path).expect("open raw");
    let pragma = |name: &str| -> i64 {
        conn.query_row(&format!("PRAGMA {name}"), [], |r| r.get(0))
            .expect("pragma")
    };
    Shape {
        bytes: std::fs::metadata(&path).expect("size").len(),
        pages: pragma("page_count"),
        free: pragma("freelist_count"),
        rows: conn
            .query_row("SELECT COUNT(*) FROM requests", [], |r| r.get(0))
            .expect("count"),
    }
}

fn range() -> Range {
    Range {
        from_ms: START + DAYS * DAY - KEEP_DAYS * DAY,
        to_ms: START + DAYS * DAY + 1,
    }
}

type View<'a> = (String, Box<dyn Fn(&History) -> String + 'a>);

/// The 90-day views `d1_speed` measures.
fn views() -> Vec<View<'static>> {
    let mut views: Vec<View> = vec![
        (
            "buckets".into(),
            Box::new(|h| {
                format!(
                    "{:?}",
                    h.buckets(range(), DAY, None, None).expect("buckets")
                )
            }),
        ),
        (
            "buckets one session".into(),
            Box::new(|h| {
                let b = h.buckets(range(), DAY, Some("session-3"), None);
                format!("{:?}", b.expect("buckets"))
            }),
        ),
    ];
    for by in [
        GroupBy::Session,
        GroupBy::Project,
        GroupBy::Model,
        GroupBy::Lane,
        GroupBy::Reason,
        GroupBy::Day,
    ] {
        views.push((
            format!("totals {by:?}"),
            Box::new(move |h| format!("{:?}", h.totals(range(), by).expect("totals"))),
        ));
    }
    let page = Page {
        limit: 100,
        offset: 0,
    };
    views.push((
        "search first page".into(),
        Box::new(move |h| {
            format!(
                "{:?}",
                h.search(Filter::new(range()), page).expect("search")
            )
        }),
    ));
    views.push((
        "search filtered".into(),
        Box::new(move |h| {
            let mut filter = Filter::new(range());
            filter.lane = Some("zai".into());
            filter.status = StatusClass::Error;
            format!("{:?}", h.search(filter, page).expect("search"))
        }),
    ));
    views
}

fn ms(d: Duration) -> f64 {
    d.as_secs_f64() * 1000.0
}

fn median_and_worst(mut times: Vec<Duration>) -> (f64, f64) {
    times.sort();
    (ms(times[times.len() / 2]), ms(times[times.len() - 1]))
}

fn describe(name: &str, s: &Shape) {
    println!(
        "{name}: {} rows, {} bytes, page_count {}, freelist_count {}",
        s.rows, s.bytes, s.pages, s.free
    );
}

#[test]
#[ignore = "a measurement: run by name in a release build"]
fn l72_ninety_day_views_on_a_pruned_database() {
    let (fresh_home, pruned_home) = (TempHome::new(), TempHome::new());
    fresh(&fresh_home);
    pruned_daily(&pruned_home);
    let (fresh_shape, pruned_shape) = (shape(&fresh_home), shape(&pruned_home));
    describe("fresh", &fresh_shape);
    describe("pruned", &pruned_shape);
    assert_eq!(fresh_shape.rows, pruned_shape.rows, "the same live rows");

    let fresh_reader = History::open_read(&db_path(&fresh_home)).expect("open read");
    let pruned_reader = History::open_read(&db_path(&pruned_home)).expect("open read");
    println!("view | fresh median / worst ms | pruned median / worst ms | ratio");
    for (name, view) in views() {
        let (mut fresh_times, mut pruned_times) = (Vec::new(), Vec::new());
        let (mut fresh_answer, mut pruned_answer) = (String::new(), String::new());
        for _ in 0..RUNS {
            let start = Instant::now();
            fresh_answer = view(&fresh_reader);
            fresh_times.push(start.elapsed());
            let start = Instant::now();
            pruned_answer = view(&pruned_reader);
            pruned_times.push(start.elapsed());
        }
        assert_eq!(fresh_answer, pruned_answer, "{name}: the same answer");
        let (fm, fw) = median_and_worst(fresh_times);
        let (pm, pw) = median_and_worst(pruned_times);
        println!(
            "{name} | {fm:.0} / {fw:.0} | {pm:.0} / {pw:.0} | {:.2}",
            pm / fm
        );
    }
}
