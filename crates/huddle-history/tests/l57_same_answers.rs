//! L57: the long views build their keys in Rust and read a whole-table range in row order; the
//! answers stay the ones the SQL key expressions and the time index give.

mod common;

use std::collections::BTreeMap;

use common::{DAY, db_path, open, row};
use huddle_history::{Bucket, GroupBy, History, Narrow, Range, Row, Total};
use huddle_testkit::temp_home::TempHome;
use rusqlite::Connection;

const START: u64 = 1_800_000_000_000;
const ROWS: u64 = 3_000;
const STEP: u64 = 97_003;

fn varied(i: u64) -> Row {
    let mut r = row(START + i * STEP, &format!("model-{}", i % 5));
    r.lane = ["anthropic", "zai", "kimi"][(i % 3) as usize].into();
    r.session = (!i.is_multiple_of(17)).then(|| format!("session-{}", i % 7));
    r.project = (!i.is_multiple_of(23)).then(|| format!("project-{}", i % 4));
    r.ttft_ms = (!i.is_multiple_of(29)).then_some(100 + (i * 7919) % 4000);
    r.tokens_in = (!i.is_multiple_of(31)).then_some(1000 + i % 500);
    r.cache_read = Some(i % 400);
    r.cache_write = Some(i % 50);
    r.tokens_out = (!i.is_multiple_of(31)).then_some(200 + i % 300);
    r.status = [200, 0, 499, 500, 401, 399, 429][(i % 7) as usize];
    r.reason = match i % 13 {
        0 => Some("timeout".into()),
        1 => Some("rate limit".into()),
        _ => None,
    };
    r
}

fn rows() -> Vec<Row> {
    (0..ROWS).map(varied).collect()
}

/// The range that holds exactly the seeded rows.
fn whole() -> Range {
    Range {
        from_ms: START,
        to_ms: START + (ROWS - 1) * STEP + 1,
    }
}

/// Every long view of `range`, as text.
fn answers(history: &History, range: Range) -> String {
    let mut out = String::new();
    for tz in [0, 330, -480, 345] {
        for width in [900_000, 3_600_000, DAY] {
            for (s, p) in [
                (None, None),
                (Some("session-3"), None),
                (None, Some("project-2")),
            ] {
                let b: Vec<Bucket> = history.buckets_in(range, width, s, p, tz).expect("buckets");
                out.push_str(&format!("{tz} {width} {s:?} {p:?} {b:?}\n"));
            }
        }
        for by in [
            GroupBy::Session,
            GroupBy::Project,
            GroupBy::Model,
            GroupBy::Lane,
            GroupBy::Reason,
            GroupBy::Day,
        ] {
            let t: Vec<Total> = history.totals_in(range, by, tz).expect("totals");
            out.push_str(&format!("{tz} {by:?} {t:?}\n"));
            let narrow = Narrow {
                sessions: vec!["session-3".into(), String::new()],
                projects: Vec::new(),
            };
            let t = history
                .totals_narrow(range, by, tz, &narrow)
                .expect("totals");
            out.push_str(&format!("{tz} {by:?} narrow {t:?}\n"));
        }
    }
    out
}

#[test]
fn l57_a_whole_table_scan_answers_as_the_time_index_does() {
    let scanned_home = TempHome::new();
    let mut scanned = open(&scanned_home);
    scanned.insert(&rows()).expect("insert");

    // One more row long before the range: the range no longer holds the whole table, so the
    // same rows are read through the time index.
    let indexed_home = TempHome::new();
    let mut indexed = open(&indexed_home);
    let mut more = rows();
    more.push(row(1_000, "model-0"));
    indexed.insert(&more).expect("insert");

    let by_scan = answers(&scanned, whole());
    assert!(by_scan.contains("session-3"), "the views hold rows");
    assert_eq!(by_scan, answers(&indexed, whole()));
}

/// Per key of `by`: rows, errors and token sums, grouped in SQL by the key expressions the
/// totals used before the keys moved into Rust.
fn sql_groups(conn: &Connection, by: GroupBy, offset_ms: i64) -> BTreeMap<String, [i64; 4]> {
    let key = match by {
        GroupBy::Session => "COALESCE(session, '')".to_string(),
        GroupBy::Project => "COALESCE(project, '')".to_string(),
        GroupBy::Model => "model".to_string(),
        GroupBy::Lane => "lane".to_string(),
        GroupBy::Reason => "lane || '|' || status || '|' || COALESCE(reason, '')".to_string(),
        GroupBy::Day => format!(
            "strftime('%Y-%m-%d', ((at_ms + ({offset_ms})) / 86400000) * 86400, 'unixepoch')"
        ),
    };
    let mut stmt = conn
        .prepare(&format!(
            "SELECT {key}, COUNT(*),
                    SUM(status = 0 OR (status >= 400 AND status != 499)),
                    TOTAL(tokens_in), TOTAL(tokens_out)
             FROM requests GROUP BY 1"
        ))
        .expect("prepare");
    stmt.query_map([], |r| {
        Ok((
            r.get::<_, String>(0)?,
            [
                r.get(1)?,
                r.get(2)?,
                r.get::<_, f64>(3)? as i64,
                r.get::<_, f64>(4)? as i64,
            ],
        ))
    })
    .expect("query")
    .collect::<Result<_, _>>()
    .expect("rows")
}

#[test]
fn l57_keys_built_in_rust_match_the_sql_key_expressions() {
    let home = TempHome::new();
    let mut history = open(&home);
    history.insert(&rows()).expect("insert");
    let conn = Connection::open(db_path(&home)).expect("open");
    for tz in [0, 330, -480] {
        for by in [
            GroupBy::Session,
            GroupBy::Project,
            GroupBy::Model,
            GroupBy::Lane,
            GroupBy::Reason,
            GroupBy::Day,
        ] {
            let ours: BTreeMap<String, [i64; 4]> = history
                .totals_in(whole(), by, tz)
                .expect("totals")
                .into_iter()
                .map(|t| {
                    let sums = [t.n, t.err, t.tok_in, t.tok_out].map(|v| v as i64);
                    (t.key, sums)
                })
                .collect();
            let offset_ms = i64::from(tz) * 60_000;
            assert_eq!(ours, sql_groups(&conn, by, offset_ms), "{by:?} at {tz}");
        }
    }
}
