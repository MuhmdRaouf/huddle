mod common;

use std::time::Instant;

use common::*;
use huddle_history::{COLUMNS, Credit, History, Row, Writer};
use huddle_testkit::temp_home::TempHome;

#[test]
fn t4_open_creates_the_folder_the_file_and_the_schema() {
    let home = TempHome::new();
    let path = db_path(&home);
    assert!(!path.parent().expect("parent").exists());
    let mut history = History::open(&path).expect("open");
    assert!(path.is_file());
    history.insert(&[row(1_000, "glm-5.3")]).expect("insert");
    drop(history);

    let reopened = open(&home);
    assert_eq!(everything(&reopened), vec![row(1_000, "glm-5.3")]);
}

#[test]
fn t4_open_sets_wal_and_schema_version_one() {
    let home = TempHome::new();
    drop(open(&home));
    let raw = rusqlite::Connection::open(db_path(&home)).expect("raw");
    let mode: String = raw
        .query_row("PRAGMA journal_mode", [], |r| r.get(0))
        .expect("mode");
    let version: i64 = raw
        .query_row("PRAGMA user_version", [], |r| r.get(0))
        .expect("version");
    assert_eq!((mode.as_str(), version), ("wal", 1));
}

#[test]
fn t4_open_refuses_a_newer_schema() {
    let home = TempHome::new();
    drop(open(&home));
    let raw = rusqlite::Connection::open(db_path(&home)).expect("raw");
    raw.pragma_update(None, "user_version", 2).expect("set");
    drop(raw);
    assert!(History::open(&db_path(&home)).is_err());
}

#[test]
fn t4_no_column_holds_prompt_key_or_header_data() {
    let home = TempHome::new();
    drop(open(&home));
    let raw = rusqlite::Connection::open(db_path(&home)).expect("raw");
    let mut stmt = raw.prepare("PRAGMA table_info(requests)").expect("info");
    let columns: Vec<String> = stmt
        .query_map([], |r| r.get(1))
        .expect("query")
        .collect::<Result<_, _>>()
        .expect("rows");
    let documented = [
        "id",
        "at_ms",
        "session",
        "project",
        "agent",
        "model",
        "lane",
        "status",
        "reason",
        "ttft_ms",
        "dur_ms",
        "tokens_in",
        "cache_read",
        "cache_write",
        "tokens_out",
        "stop",
        "upstream_id",
    ];
    assert_eq!(columns, documented);
    assert_eq!(COLUMNS.to_vec(), documented);
}

#[test]
fn t4_writer_batches_rows_off_the_callers_thread() {
    let home = TempHome::new();
    let path = db_path(&home);
    let writer = Writer::spawn(&path);
    let started = Instant::now();
    for i in 0..500 {
        writer.send(row(i, "glm-5.3"));
    }
    assert!(started.elapsed().as_millis() < 500, "send blocked");
    assert_eq!(writer.dropped(), 0);
    drop(writer);

    assert_eq!(everything(&open(&home)).len(), 500);
}

#[test]
fn t4_fill_sets_unknown_usage_once_and_keeps_known_usage() {
    let home = TempHome::new();
    let mut history = open(&home);
    let unknown = Row {
        upstream_id: Some("req_a".into()),
        ..row(1, "glm-5.3")
    };
    let known = Row {
        upstream_id: Some("req_b".into()),
        tokens_in: Some(7),
        tokens_out: Some(8),
        ..row(2, "glm-5.3")
    };
    history.insert(&[unknown, known]).expect("insert");
    assert_eq!(history.missing_usage(0).expect("missing"), vec!["req_a"]);

    let credit = |id: &str| Credit {
        upstream_id: id.into(),
        agent: Some("explorer".into()),
        project: Some("huddle".into()),
        usage: (10, 20, 30, 40),
        stop: Some("end_turn".into()),
    };
    assert_eq!(
        history
            .fill(&[credit("req_a"), credit("req_b")])
            .expect("fill"),
        2
    );
    assert_eq!(
        history
            .fill(&[credit("req_a"), credit("req_b")])
            .expect("again"),
        0
    );

    let rows = everything(&history);
    let a = rows.iter().find(|r| r.at_ms == 1).expect("a");
    assert_eq!(
        (a.tokens_in, a.cache_read, a.cache_write, a.tokens_out),
        (Some(10), Some(20), Some(30), Some(40))
    );
    assert_eq!(a.stop.as_deref(), Some("end_turn"));
    let b = rows.iter().find(|r| r.at_ms == 2).expect("b");
    assert_eq!(
        (b.tokens_in, b.tokens_out, b.cache_read),
        (Some(7), Some(8), None)
    );
    assert_eq!(
        (b.agent.as_deref(), b.project.as_deref()),
        (Some("explorer"), Some("huddle"))
    );
    assert!(history.missing_usage(0).expect("missing").is_empty());
}

#[test]
fn t4_missing_usage_honours_since() {
    let home = TempHome::new();
    let mut history = open(&home);
    let at = |ms, id: &str| Row {
        upstream_id: Some(id.into()),
        ..row(ms, "m")
    };
    history
        .insert(&[at(10, "old"), at(100, "new")])
        .expect("insert");
    assert_eq!(history.missing_usage(50).expect("missing"), vec!["new"]);
}

#[test]
fn t4_prune_drops_rows_older_than_keep_days() {
    let home = TempHome::new();
    let mut history = open(&home);
    let now = 100 * DAY;
    history
        .insert(&[
            row(now - 31 * DAY, "m"),
            row(now - 29 * DAY, "m"),
            row(now, "m"),
        ])
        .expect("insert");
    assert_eq!(history.prune(30, now).expect("prune"), 1);
    assert_eq!(everything(&history).len(), 2);
}
