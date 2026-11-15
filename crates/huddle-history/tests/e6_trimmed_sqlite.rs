mod common;

use std::collections::BTreeMap;

use common::*;
use huddle_history::{Credit, Filter, GroupBy, History, Narrow, Range, Row, StatusClass};
use huddle_testkit::temp_home::TempHome;
use rusqlite::Connection;

/// The 0.0.7 schema, written by hand so the test does not depend on the crate's own migration.
const SCHEMA_0_0_7: &str = "
CREATE TABLE requests (
    id INTEGER PRIMARY KEY,
    at_ms INTEGER NOT NULL,
    session TEXT,
    project TEXT,
    agent TEXT,
    model TEXT NOT NULL,
    lane TEXT NOT NULL,
    status INTEGER NOT NULL,
    reason TEXT,
    ttft_ms INTEGER,
    dur_ms INTEGER NOT NULL,
    tokens_in INTEGER,
    cache_read INTEGER,
    cache_write INTEGER,
    tokens_out INTEGER,
    stop TEXT,
    upstream_id TEXT
);
CREATE INDEX requests_at_ms ON requests (at_ms);
CREATE INDEX requests_upstream_id ON requests (upstream_id);
PRAGMA user_version = 1;
";

fn compile_options() -> Vec<String> {
    let conn = Connection::open_in_memory().expect("memory db");
    let mut stmt = conn.prepare("PRAGMA compile_options").expect("pragma");
    stmt.query_map([], |r| r.get::<_, String>(0))
        .expect("query")
        .map(|o| o.expect("option"))
        .collect()
}

#[test]
fn e6_the_bundled_sqlite_leaves_out_what_huddle_does_not_use() {
    let options = compile_options();
    for omitted in [
        "OMIT_JSON",
        "OMIT_LOAD_EXTENSION",
        "OMIT_DEPRECATED",
        "OMIT_SHARED_CACHE",
        "OMIT_PROGRESS_CALLBACK",
        "OMIT_AUTHORIZATION",
    ] {
        assert!(
            options.iter().any(|o| o == omitted),
            "{omitted} missing: {options:?}"
        );
    }
    for dropped in [
        "ENABLE_FTS3",
        "ENABLE_FTS5",
        "ENABLE_RTREE",
        "ENABLE_STAT4",
        "ENABLE_DBSTAT_VTAB",
        "ENABLE_COLUMN_METADATA",
        "SOUNDEX",
    ] {
        assert!(
            !options.iter().any(|o| o == dropped),
            "{dropped} still built: {options:?}"
        );
    }
}

fn write_0_0_7_db(path: &std::path::Path) {
    std::fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    let conn = Connection::open(path).expect("create");
    conn.pragma_update_and_check(None, "journal_mode", "WAL", |_| Ok(()))
        .expect("wal");
    conn.execute_batch(SCHEMA_0_0_7).expect("schema");
    let mut insert = conn
        .prepare(
            "INSERT INTO requests (at_ms, session, project, agent, model, lane, status, reason,
                 ttft_ms, dur_ms, tokens_in, cache_read, cache_write, tokens_out, stop, upstream_id)
             VALUES (?1, ?2, ?3, ?4, ?5, 'zai', ?6, ?7, ?8, 120, ?9, ?9, ?9, ?9, NULL, ?10)",
        )
        .expect("prepare");
    for i in 0..30_u64 {
        let status = [200, 200, 500, 499, 0][(i % 5) as usize];
        let known = i % 3 != 0;
        insert
            .execute(rusqlite::params![
                i64::try_from(1_000 + i * DAY / 10).expect("ms"),
                format!("s{}", i % 3),
                format!("p{}", i % 2),
                "claude",
                format!("m{}", i % 2),
                status,
                (status >= 400).then_some("upstream"),
                Some(i64::try_from(40 + i).expect("ttft")),
                known.then_some(5_i64),
                format!("up{i}"),
            ])
            .expect("insert");
    }
}

#[test]
fn e6_history_queries_run_on_the_trimmed_sqlite() {
    let home = TempHome::new();
    let path = db_path(&home);
    write_0_0_7_db(&path);

    let mut history = History::open(&path).expect("open 0.0.7 db");
    let read = History::open_read(&path).expect("open read");

    let buckets = read.buckets(all(), 1_000, None, None).expect("buckets");
    assert!(!buckets.is_empty());
    assert!(buckets.iter().any(|b| b.ttft_p50.is_some()));

    for by in [
        GroupBy::Session,
        GroupBy::Project,
        GroupBy::Model,
        GroupBy::Lane,
        GroupBy::Reason,
        GroupBy::Day,
    ] {
        let totals = read.totals(all(), by).expect("totals");
        assert!(!totals.is_empty(), "{by:?}");
    }
    let days = read.totals(all(), GroupBy::Day).expect("days");
    assert!(
        days.iter()
            .all(|t| t.key.len() == 10 && t.key.starts_with("1970-"))
    );

    let aliases = BTreeMap::from([("p1".to_string(), "p0".to_string())]);
    let merged = read
        .totals_with_aliases(all(), GroupBy::Project, &aliases, &Narrow::default())
        .expect("aliases");
    assert_eq!(merged.len(), 1);

    let mut filter = Filter::new(all());
    filter.model = Some("m1".into());
    filter.status = StatusClass::Error;
    let found = read.search(filter, page(5, 2)).expect("search");
    assert!(found.total > 0 && found.rows.len() <= 5);

    let missing = read.missing_usage(0).expect("missing");
    assert!(!missing.is_empty());
    let credits: Vec<Credit> = missing
        .iter()
        .map(|id| Credit {
            upstream_id: id.clone(),
            agent: Some("claude".into()),
            project: Some("p0".into()),
            usage: (1, 2, 3, 4),
            stop: Some("end_turn".into()),
        })
        .collect();
    assert_eq!(history.fill(&credits).expect("fill"), missing.len());
    assert!(read.missing_usage(0).expect("missing").is_empty());

    history
        .insert(&[Row {
            upstream_id: Some("fresh".into()),
            ..row(5 * DAY, "m0")
        }])
        .expect("insert");
    let removed = history.prune(1, 5 * DAY + 2 * DAY).expect("prune");
    assert!(removed > 0);
    let left = read
        .search(
            Filter::new(Range {
                from_ms: 0,
                to_ms: u64::MAX / 2,
            }),
            page(500, 0),
        )
        .expect("search");
    assert!(left.total < 31);
}
