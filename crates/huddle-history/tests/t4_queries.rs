mod common;

use common::*;
use huddle_history::{Filter, GroupBy, Range, Row, StatusClass};
use huddle_testkit::temp_home::TempHome;

fn with_status(at_ms: u64, status: u16) -> Row {
    Row {
        status,
        ..row(at_ms, "m")
    }
}

#[test]
fn t4_buckets_group_by_start_and_model_and_sum_tokens() {
    let home = TempHome::new();
    let mut history = open(&home);
    let used = |at_ms, model: &str, status| Row {
        status,
        tokens_in: Some(1),
        cache_read: Some(2),
        cache_write: Some(3),
        tokens_out: Some(4),
        ..row(at_ms, model)
    };
    history
        .insert(&[
            used(10, "a", 200),
            used(20, "a", 500),
            used(30, "a", 499),
            used(40, "a", 0),
            used(1_010, "a", 200),
            used(15, "b", 200),
            row(5_000, "a"),
        ])
        .expect("insert");
    let range = Range {
        from_ms: 0,
        to_ms: 5_000,
    };
    let buckets = history.buckets(range, 1_000, None, None).expect("buckets");
    let keys: Vec<(u64, &str)> = buckets
        .iter()
        .map(|b| (b.start_ms, b.model.as_str()))
        .collect();
    assert_eq!(keys, vec![(0, "a"), (0, "b"), (1_000, "a")]);
    let first = &buckets[0];
    assert_eq!((first.n, first.err), (4, 2));
    assert_eq!(
        (
            first.tok_in,
            first.cache_read,
            first.cache_write,
            first.tok_out
        ),
        (4, 8, 12, 16)
    );
}

#[test]
fn t4_buckets_ttft_percentiles_use_nearest_rank() {
    let home = TempHome::new();
    let mut history = open(&home);
    let rows: Vec<Row> = (1..=20)
        .map(|i| Row {
            ttft_ms: Some(i * 10),
            ..row(i, "m")
        })
        .chain([row(30, "m")])
        .collect();
    history.insert(&rows).expect("insert");
    let buckets = history
        .buckets(
            Range {
                from_ms: 0,
                to_ms: 1_000,
            },
            1_000,
            None,
            None,
        )
        .expect("buckets");
    assert_eq!(
        (buckets[0].ttft_p50, buckets[0].ttft_p95),
        (Some(100), Some(190))
    );
}

#[test]
fn t4_buckets_without_ttft_have_no_percentiles_and_zero_width_fails() {
    let home = TempHome::new();
    let mut history = open(&home);
    history.insert(&[row(1, "m")]).expect("insert");
    let range = Range {
        from_ms: 0,
        to_ms: 10,
    };
    let buckets = history.buckets(range, 10, None, None).expect("buckets");
    assert_eq!((buckets[0].ttft_p50, buckets[0].ttft_p95), (None, None));
    assert!(history.buckets(range, 0, None, None).is_err());
}

fn seeded(home: &TempHome) -> huddle_history::History {
    let mut history = open(home);
    let r =
        |at_ms, model: &str, lane: &str, session: Option<&str>, project: Option<&str>, status| {
            Row {
                lane: lane.into(),
                session: session.map(Into::into),
                project: project.map(Into::into),
                status,
                tokens_in: Some(10),
                tokens_out: Some(5),
                ..row(at_ms, model)
            }
        };
    history
        .insert(&[
            r(1_000, "glm", "zai", Some("s1"), Some("p1"), 200),
            r(2_000, "kimi", "kimi", Some("s1"), Some("p1"), 500),
            r(DAY + 3_000, "glm", "zai", Some("s2"), None, 200),
        ])
        .expect("insert");
    history
}

#[test]
fn t4_totals_by_session_with_per_model_sums() {
    let home = TempHome::new();
    let totals = seeded(&home)
        .totals(all(), GroupBy::Session)
        .expect("totals");
    let keys: Vec<&str> = totals.iter().map(|t| t.key.as_str()).collect();
    assert_eq!(keys, vec!["s1", "s2"]);
    let s1 = &totals[0];
    assert_eq!((s1.n, s1.err, s1.tok_in, s1.tok_out), (2, 1, 20, 10));
    assert_eq!((s1.first_ms, s1.last_ms), (1_000, 2_000));
    let models: Vec<(&str, u64, u64)> = s1
        .models
        .iter()
        .map(|m| (m.model.as_str(), m.n, m.tok_in))
        .collect();
    assert_eq!(models, vec![("glm", 1, 10), ("kimi", 1, 10)]);
}

#[test]
fn t4_totals_by_project_groups_unknown_under_the_empty_key() {
    let home = TempHome::new();
    let totals = seeded(&home)
        .totals(all(), GroupBy::Project)
        .expect("totals");
    let keys: Vec<&str> = totals.iter().map(|t| t.key.as_str()).collect();
    assert_eq!(keys, vec!["", "p1"]);
}

#[test]
fn t4_totals_by_model_and_lane() {
    let home = TempHome::new();
    let history = seeded(&home);
    let by_model = history.totals(all(), GroupBy::Model).expect("model");
    assert_eq!(
        by_model
            .iter()
            .map(|t| (t.key.as_str(), t.n))
            .collect::<Vec<_>>(),
        vec![("glm", 2), ("kimi", 1)]
    );
    let by_lane = history.totals(all(), GroupBy::Lane).expect("lane");
    assert_eq!(
        by_lane
            .iter()
            .map(|t| (t.key.as_str(), t.n))
            .collect::<Vec<_>>(),
        vec![("kimi", 1), ("zai", 2)]
    );
}

#[test]
fn t4_totals_by_day_uses_utc_days_and_honours_the_range() {
    let home = TempHome::new();
    let history = seeded(&home);
    let days = history.totals(all(), GroupBy::Day).expect("day");
    assert_eq!(
        days.iter()
            .map(|t| (t.key.as_str(), t.n))
            .collect::<Vec<_>>(),
        vec![("1970-01-01", 2), ("1970-01-02", 1)]
    );
    let narrow = Range {
        from_ms: DAY,
        to_ms: 2 * DAY,
    };
    assert_eq!(
        history.totals(narrow, GroupBy::Day).expect("narrow").len(),
        1
    );
}

#[test]
fn t4_search_orders_newest_first_and_pages_with_a_total() {
    let home = TempHome::new();
    let mut history = open(&home);
    let rows: Vec<Row> = (1..=10).map(|i| row(i, "m")).collect();
    history.insert(&rows).expect("insert");
    let result = history
        .search(Filter::new(all()), page(3, 2))
        .expect("search");
    assert_eq!(result.total, 10);
    assert_eq!(
        result.rows.iter().map(|r| r.at_ms).collect::<Vec<_>>(),
        vec![8, 7, 6]
    );
}

#[test]
fn t4_search_caps_the_page_at_500() {
    let home = TempHome::new();
    let mut history = open(&home);
    let rows: Vec<Row> = (0..600).map(|i| row(i, "m")).collect();
    history.insert(&rows).expect("insert");
    let result = history
        .search(Filter::new(all()), page(10_000, 0))
        .expect("search");
    assert_eq!((result.rows.len(), result.total), (500, 600));
}

#[test]
fn t4_search_filters_by_model_lane_session_agent_and_project() {
    let home = TempHome::new();
    let history = seeded(&home);
    let count = |filter: Filter| history.search(filter, page(50, 0)).expect("search").total;
    let base = || Filter::new(all());
    assert_eq!(
        count(Filter {
            model: Some("glm".into()),
            ..base()
        }),
        2
    );
    assert_eq!(
        count(Filter {
            lane: Some("kimi".into()),
            ..base()
        }),
        1
    );
    assert_eq!(
        count(Filter {
            session: Some("s2".into()),
            ..base()
        }),
        1
    );
    assert_eq!(
        count(Filter {
            project: Some("p1".into()),
            ..base()
        }),
        2
    );
    assert_eq!(
        count(Filter {
            agent: Some("none".into()),
            ..base()
        }),
        0
    );
    assert_eq!(
        count(Filter {
            range: Range {
                from_ms: 0,
                to_ms: 1_500
            },
            ..base()
        }),
        1
    );
}

#[test]
fn t4_search_filters_by_status_class() {
    let home = TempHome::new();
    let mut history = open(&home);
    history
        .insert(&[
            with_status(1, 200),
            with_status(2, 304),
            with_status(3, 404),
            with_status(4, 0),
            with_status(5, 499),
        ])
        .expect("insert");
    let count = |status| {
        history
            .search(
                Filter {
                    status,
                    ..Filter::new(all())
                },
                page(50, 0),
            )
            .expect("search")
            .total
    };
    assert_eq!(count(StatusClass::Any), 5);
    assert_eq!(count(StatusClass::Ok), 2);
    assert_eq!(count(StatusClass::Error), 2);
    assert_eq!(count(StatusClass::ClientLeft), 1);
}

#[test]
fn t9_buckets_narrow_to_one_session_and_one_project() {
    let home = TempHome::new();
    let mut history = open(&home);
    let owned = |at_ms, session: &str, project: &str| Row {
        session: Some(session.into()),
        project: Some(project.into()),
        ..row(at_ms, "m")
    };
    history
        .insert(&[
            owned(1, "s1", "p1"),
            owned(2, "s1", "p2"),
            owned(3, "s2", "p1"),
        ])
        .expect("insert");
    let range = Range {
        from_ms: 0,
        to_ms: 10,
    };
    let n = |session, project| {
        history
            .buckets(range, 10, session, project)
            .expect("buckets")
            .iter()
            .map(|b| b.n)
            .sum::<u64>()
    };
    assert_eq!(n(None, None), 3);
    assert_eq!(n(Some("s1"), None), 2);
    assert_eq!(n(None, Some("p1")), 2);
    assert_eq!(n(Some("s1"), Some("p1")), 1);
    assert_eq!(n(Some("nope"), None), 0);
}
