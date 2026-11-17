//! L57: the long views build their keys in Rust and read a whole-table range in row order; the
//! answers stay the ones the SQL key expressions and the time index give.

mod common;

use std::collections::BTreeMap;

use common::{DAY, db_path, open, row};
use huddle_history::{Bucket, GroupBy, History, Narrow, Range, Row, Total};
use huddle_testkit::temp_home::TempHome;
use rusqlite::Connection;
use rusqlite::types::ValueRef;

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

/// The mixed values every grouped column cycles through: an Arabic name, an emoji, one
/// accented name in its composed and its decomposed form (two different keys on any path that
/// compares bytes), mixed case, the empty string and a trailing space. `session`, `project`
/// and `reason` are also NULL on their stride.
const NAMES: [&str; 10] = [
    "مشروع",
    "🦀",
    "Caf\u{00e9}",
    "Cafe\u{0301}",
    "Abc",
    "abc",
    "",
    "spot ",
    "session-3",
    "project-2",
];

const MIXED_ROWS: u64 = 120;

fn mixed(i: u64) -> Row {
    let pick = |stride: u64| NAMES[((i * stride) % NAMES.len() as u64) as usize];
    let mut r = row(START + i * STEP, pick(1));
    r.session = (!i.is_multiple_of(8)).then(|| pick(3).to_string());
    r.project = (!i.is_multiple_of(9)).then(|| pick(5).to_string());
    r.lane = pick(7).to_string();
    r.reason = (!i.is_multiple_of(10)).then(|| pick(9).to_string());
    r.status = [200, 200, 0, 500, 429][(i % 5) as usize];
    r.ttft_ms = (!i.is_multiple_of(3)).then_some(50 + i % 900);
    r.tokens_in = Some(100 + i % 40);
    r.tokens_out = Some(20 + i % 30);
    r
}

fn mixed_rows() -> Vec<Row> {
    (0..MIXED_ROWS).map(mixed).collect()
}

/// Rows whose grouped text columns hold a BLOB of invalid UTF-8 (`x'fffe'`): one row per
/// named column, then one row with all the named columns at once. The other grouped columns
/// stay plain (NULL, or text where the column refuses NULL), so a key difference can only
/// come from a named column.
fn blob_rows(home: &TempHome, first_at: u64, targets: &[&str]) {
    let conn = Connection::open(db_path(home)).expect("open");
    let cell = |column: &str, blob: bool| {
        if blob && targets.contains(&column) {
            "x'fffe'"
        } else if column == "model" {
            "'Abc'"
        } else if column == "lane" {
            "'abc'"
        } else {
            "NULL"
        }
    };
    for (k, target) in targets.iter().enumerate() {
        let at = first_at + k as u64 * STEP;
        conn.execute(
            &format!(
                "INSERT INTO requests (at_ms, session, project, model, lane, status, reason,
                                        dur_ms, tokens_in, tokens_out)
                 VALUES ({at}, {s}, {p}, {m}, {l}, 500, {rr}, 40, 10, 5)",
                s = cell("session", "session" == *target),
                p = cell("project", "project" == *target),
                m = cell("model", "model" == *target),
                l = cell("lane", "lane" == *target),
                rr = cell("reason", "reason" == *target),
            ),
            [],
        )
        .expect("insert the blob row");
    }
    let at = first_at + targets.len() as u64 * STEP;
    conn.execute(
        &format!(
            "INSERT INTO requests (at_ms, session, project, model, lane, status, reason,
                                    dur_ms, tokens_in, tokens_out)
             VALUES ({at}, {s}, {p}, {m}, {l}, 500, {rr}, 40, 10, 5)",
            s = cell("session", true),
            p = cell("project", true),
            m = cell("model", true),
            l = cell("lane", true),
            rr = cell("reason", true),
        ),
        [],
    )
    .expect("insert the all-targets blob row");
}

/// The key value of the current SQL row as the scan reads it: TEXT with `U+FFFD` where the
/// bytes are not UTF-8, an integer as its decimal text, a BLOB or NULL as the empty key.
fn plain_key(r: &rusqlite::Row<'_>) -> rusqlite::Result<String> {
    Ok(match r.get_ref(0)? {
        ValueRef::Text(bytes) => String::from_utf8_lossy(bytes).into_owned(),
        ValueRef::Integer(n) => n.to_string(),
        _ => String::new(),
    })
}

/// `sql_groups`, over rows the plain path cannot describe in a `String`: the same key
/// expressions, with each group's key read the way `plain_key` reads it, and groups whose
/// keys read the same summed into one group, as the scan's interner merges them.
fn plain_groups(conn: &Connection, by: GroupBy, offset_ms: i64) -> BTreeMap<String, [i64; 4]> {
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
            plain_key(r)?,
            [
                r.get(1)?,
                r.get(2)?,
                r.get::<_, f64>(3)? as i64,
                r.get::<_, f64>(4)? as i64,
            ],
        ))
    })
    .expect("query")
    .fold(BTreeMap::new(), |mut groups, group| {
        let (key, sums) = group.expect("rows");
        let total = groups.entry(key).or_insert([0; 4]);
        for (total, sum) in total.iter_mut().zip(sums) {
            *total += sum;
        }
        groups
    })
}

/// The totals built in Rust against the plain SQL key expressions, per key of each `by` over
/// `tz {0, 330, -480}`: rows, errors and token sums must agree.
fn totals_as_sql(history: &History, conn: &Connection, bys: &[GroupBy]) {
    for tz in [0, 330, -480] {
        for by in bys {
            let ours: BTreeMap<String, [i64; 4]> = history
                .totals_in(whole(), *by, tz)
                .expect("totals")
                .into_iter()
                .map(|t| {
                    let sums = [t.n, t.err, t.tok_in, t.tok_out].map(|v| v as i64);
                    (t.key, sums)
                })
                .collect();
            let offset_ms = i64::from(tz) * 60_000;
            assert_eq!(ours, plain_groups(conn, *by, offset_ms), "{by:?} at {tz}");
        }
    }
}

/// L71: for rows whose grouped text columns hold non-ASCII, mixed-case, empty, NULL and
/// trailing-space values, the whole-table scan and the time index answer alike, and the
/// totals built in Rust answer like the plain SQL key expressions. A BLOB of foreign bytes
/// in session, project or model is compared too, where both paths read it as the empty key.
#[test]
fn l71_same_answers_for_non_ascii_and_broken_text() {
    let scanned_home = TempHome::new();
    let mut scanned = open(&scanned_home);
    scanned.insert(&mixed_rows()).expect("insert");
    blob_rows(
        &scanned_home,
        START + MIXED_ROWS * STEP,
        &["session", "project", "model"],
    );

    // One more row long before the range: the same rows are read through the time index.
    let indexed_home = TempHome::new();
    let mut indexed = open(&indexed_home);
    let mut more = mixed_rows();
    more.push(row(1_000, "model-0"));
    indexed.insert(&more).expect("insert");
    blob_rows(
        &indexed_home,
        START + MIXED_ROWS * STEP,
        &["session", "project", "model"],
    );

    let by_scan = answers(&scanned, whole());
    assert!(
        by_scan.contains("session-3"),
        "the views hold the named rows"
    );
    assert!(
        by_scan.contains("مشروع"),
        "the views hold the non-ascii rows"
    );
    assert_eq!(by_scan, answers(&indexed, whole()));
    let conn = Connection::open(db_path(&scanned_home)).expect("open");
    totals_as_sql(
        &scanned,
        &conn,
        &[
            GroupBy::Session,
            GroupBy::Project,
            GroupBy::Model,
            GroupBy::Lane,
            GroupBy::Reason,
            GroupBy::Day,
        ],
    );

    // The writer stores lane and reason as text only, and the two paths read foreign bytes
    // differently, so the rows with a BLOB in lane or reason stay out of the Reason
    // comparison. Session, Project, Model and Lane grouping is compared on them here, where
    // both paths read the BLOB as the empty key.
    let broken_home = TempHome::new();
    let mut broken = open(&broken_home);
    broken.insert(&mixed_rows()).expect("insert");
    blob_rows(&broken_home, START + MIXED_ROWS * STEP, &["lane", "reason"]);
    let conn = Connection::open(db_path(&broken_home)).expect("open");
    totals_as_sql(
        &broken,
        &conn,
        &[
            GroupBy::Session,
            GroupBy::Project,
            GroupBy::Model,
            GroupBy::Lane,
        ],
    );
}
