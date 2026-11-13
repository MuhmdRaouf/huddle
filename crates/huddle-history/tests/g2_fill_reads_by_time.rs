mod common;

use huddle_history::{Credit, Row, missing_usage_sql};
use huddle_testkit::temp_home::TempHome;

use common::*;

#[test]
fn g2_missing_usage_plans_on_the_time_index() {
    let home = TempHome::new();
    drop(open(&home));
    let raw = rusqlite::Connection::open(db_path(&home)).expect("raw");
    let mut stmt = raw
        .prepare(&format!("EXPLAIN QUERY PLAN {}", missing_usage_sql()))
        .expect("explain");
    let details: Vec<String> = stmt
        .query_map([0i64], |r| r.get::<_, String>(3))
        .expect("query")
        .collect::<Result<_, _>>()
        .expect("rows");
    assert!(
        details.iter().any(|d| d.contains("requests_at_ms")),
        "the fill must read its window through the time index, plan: {details:?}"
    );
}

#[test]
fn g2_missing_usage_returns_the_same_ids() {
    let home = TempHome::new();
    let mut history = open(&home);
    let mut rows = Vec::new();
    for (ms, id, known) in [
        (100u64, Some("req_outside"), false),
        (1_100, Some("req_known"), true),
        (1_200, Some("req_a"), false),
        (1_250, Some("req_a"), false),
        (1_300, None, false),
        (1_400, Some("req_b"), false),
    ] {
        rows.push(Row {
            upstream_id: id.map(str::to_string),
            tokens_in: known.then_some(5),
            ..row(ms, "glm-5.3-flash")
        });
    }
    history.insert(&rows).expect("insert");

    assert_eq!(
        history.missing_usage(1_000).expect("missing"),
        vec!["req_a", "req_b"]
    );
    assert_eq!(
        history.missing_usage(0).expect("all"),
        vec!["req_a", "req_b", "req_outside"]
    );

    let credit = |id: &str| Credit {
        upstream_id: id.into(),
        agent: None,
        project: None,
        usage: (1, 2, 3, 4),
        stop: Some("end_turn".into()),
    };
    history.fill(&[credit("req_a")]).expect("fill");
    assert_eq!(
        history.missing_usage(1_000).expect("after fill"),
        vec!["req_b"]
    );
}
