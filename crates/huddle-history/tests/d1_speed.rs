//! D1: every history view answers fast on a 90-day database of about a million rows.

mod common;

use std::time::{Duration, Instant};

use common::{DAY, db_path, row};
use huddle_history::{Filter, GroupBy, History, Page, Range, Row, StatusClass};
use huddle_testkit::temp_home::TempHome;

const NOW: u64 = 1_800_000_000_000;
const ROWS: u64 = 1_000_000;
/// Views up to 7 days read a slice of the table, so each answers inside this.
const SHORT_LIMIT: Duration = Duration::from_millis(300);
/// A range of 30 days or longer is one cold scan of most of the table on one thread; repeat
/// questions are answered from the history cache.
const LONG_LIMIT: Duration = Duration::from_millis(600);
const LANES: [&str; 6] = ["anthropic", "zai", "kimi", "deepseek", "minimax", "qwen"];

fn generated(i: u64) -> Row {
    let model = format!("model-{}", i % 12);
    let mut r = row(NOW - 90 * DAY + i * (90 * DAY / ROWS), &model);
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

fn seeded(home: &TempHome) -> History {
    let mut history = History::open(&db_path(home)).expect("open");
    let mut next = 0;
    while next < ROWS {
        let end = (next + 50_000).min(ROWS);
        let chunk: Vec<Row> = (next..end).map(generated).collect();
        history.insert(&chunk).expect("insert");
        next = end;
    }
    history
}

fn range(span_ms: u64) -> Range {
    Range {
        from_ms: NOW - span_ms,
        to_ms: NOW + 1,
    }
}

fn timed<T>(what: &str, limit: Duration, work: impl FnOnce() -> T) {
    let start = Instant::now();
    let _ = work();
    let took = start.elapsed();
    println!("{what}: {took:?}");
    assert!(took < limit, "{what} took {took:?}, limit {limit:?}");
}

#[test]
#[cfg_attr(debug_assertions, ignore = "the budget is for the release build")]
fn d1_every_view_answers_fast_on_ninety_days() {
    let home = TempHome::new();
    drop(seeded(&home));
    let reader = History::open_read(&db_path(&home)).expect("open read");

    let spans = [
        ("15m", 15 * 60_000, 30_000, SHORT_LIMIT),
        ("24h", DAY, 900_000, SHORT_LIMIT),
        ("7d", 7 * DAY, 3_600_000, SHORT_LIMIT),
        ("30d", 30 * DAY, DAY, LONG_LIMIT),
        ("90d", 90 * DAY, DAY, LONG_LIMIT),
    ];
    for (name, span, width, limit) in spans {
        timed(&format!("buckets {name}"), limit, || {
            reader
                .buckets(range(span), width, None, None)
                .expect("buckets")
        });
        timed(&format!("buckets {name} by session"), limit, || {
            reader
                .buckets(range(span), width, Some("session-3"), None)
                .expect("buckets")
        });
        for by in [
            GroupBy::Session,
            GroupBy::Project,
            GroupBy::Model,
            GroupBy::Lane,
            GroupBy::Reason,
            GroupBy::Day,
        ] {
            timed(&format!("totals {name} {by:?}"), limit, || {
                reader.totals(range(span), by).expect("totals")
            });
        }
        let page = Page {
            limit: 100,
            offset: 0,
        };
        timed(&format!("search {name}"), limit, || {
            reader
                .search(Filter::new(range(span)), page)
                .expect("search")
        });
        let mut filtered = Filter::new(range(span));
        filtered.lane = Some("zai".into());
        filtered.status = StatusClass::Error;
        timed(&format!("search {name} filtered"), limit, || {
            reader.search(filtered, page).expect("search")
        });
    }
}

#[test]
#[cfg_attr(debug_assertions, ignore = "the budget is for the release build")]
fn d1_reads_never_wait_on_the_writer() {
    let home = TempHome::new();
    let mut writer = seeded(&home);
    let path = db_path(&home);
    let reader = History::open_read(&path).expect("open read");

    let burst = std::thread::spawn(move || {
        let stop = Instant::now() + Duration::from_secs(4);
        let mut i = ROWS;
        while Instant::now() < stop {
            let chunk: Vec<Row> = (i..i + 20_000).map(generated).collect();
            writer.insert(&chunk).expect("insert");
            i += 20_000;
        }
    });
    std::thread::sleep(Duration::from_millis(300));
    for _ in 0..5 {
        timed("search during a write burst", SHORT_LIMIT, || {
            reader
                .search(
                    Filter::new(range(DAY)),
                    Page {
                        limit: 100,
                        offset: 0,
                    },
                )
                .expect("search")
        });
        timed("totals during a write burst", SHORT_LIMIT, || {
            reader.totals(range(DAY), GroupBy::Lane).expect("totals")
        });
    }
    burst.join().expect("burst");
}
