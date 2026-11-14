mod common;

use common::*;
use huddle_history::{GroupBy, Row};
use huddle_testkit::temp_home::TempHome;

const BUCKET: u64 = 300_000;

fn timed(at_ms: u64, model: &str, lane: &str, ttft: Option<u64>) -> Row {
    Row {
        lane: lane.into(),
        ttft_ms: ttft,
        ..row(at_ms, model)
    }
}

/// Nearest rank over the sorted values, the rule the buckets use.
fn nearest_rank(values: &[u64], pct: usize) -> u64 {
    let mut sorted = values.to_vec();
    sorted.sort_unstable();
    sorted[(pct * sorted.len()).div_ceil(100).max(1) - 1]
}

/// Model `a`: 40 requests of 100 ms spread over 40 buckets, plus 2 slow ones of 5000 ms each
/// in its own bucket, and a request with no first-token time. Every bucket holding a slow
/// request has a p95 of 5000, so a max of bucket p95s reads 5000; the rows say 100.
fn seeded(home: &TempHome) -> (huddle_history::History, Vec<u64>) {
    let mut history = open(home);
    let mut rows = Vec::new();
    let mut values = Vec::new();
    for i in 0..42u64 {
        let ttft = if i < 40 { 100 } else { 5_000 };
        rows.push(timed(i * BUCKET + 10, "a", "zai", Some(ttft)));
        values.push(ttft);
    }
    rows.push(timed(50 * BUCKET, "a", "zai", None));
    for i in 0..10u64 {
        rows.push(timed(i * BUCKET + 20, "b", "kimi", Some(200 + i * 10)));
    }
    history.insert(&rows).expect("insert");
    (history, values)
}

#[test]
fn j1_totals_by_model_carry_row_level_ttft() {
    let home = TempHome::new();
    let (history, a_values) = seeded(&home);
    let totals = history.totals(all(), GroupBy::Model).expect("totals");
    let a = totals.iter().find(|t| t.key == "a").expect("a");
    assert_eq!(a.n, 43);
    assert_eq!(a.ttft_p50, Some(nearest_rank(&a_values, 50)));
    assert_eq!(a.ttft_p95, Some(nearest_rank(&a_values, 95)));
    assert_eq!(a.ttft_p95, Some(100));
    let b_values: Vec<u64> = (0..10).map(|i| 200 + i * 10).collect();
    let b = totals.iter().find(|t| t.key == "b").expect("b");
    assert_eq!(b.ttft_p95, Some(nearest_rank(&b_values, 95)));
}

#[test]
fn j1_totals_by_lane_carry_row_level_ttft() {
    let home = TempHome::new();
    let (history, a_values) = seeded(&home);
    let totals = history.totals(all(), GroupBy::Lane).expect("totals");
    let zai = totals.iter().find(|t| t.key == "zai").expect("zai");
    assert_eq!(zai.ttft_p50, Some(nearest_rank(&a_values, 50)));
    assert_eq!(zai.ttft_p95, Some(nearest_rank(&a_values, 95)));
}

#[test]
fn j1_a_key_with_no_first_token_times_has_no_percentile() {
    let home = TempHome::new();
    let mut history = open(&home);
    history
        .insert(&[timed(10, "a", "zai", None)])
        .expect("insert");
    let totals = history.totals(all(), GroupBy::Model).expect("totals");
    assert_eq!((totals[0].ttft_p50, totals[0].ttft_p95), (None, None));
    assert_eq!(history.ttft_percentiles(all()).expect("all"), (None, None));
}

#[test]
fn j1_all_rows_carry_one_percentile_pair() {
    let home = TempHome::new();
    let (history, mut values) = seeded(&home);
    values.extend((0..10).map(|i| 200 + i * 10));
    let (p50, p95) = history.ttft_percentiles(all()).expect("all");
    assert_eq!(p50, Some(nearest_rank(&values, 50)));
    assert_eq!(p95, Some(nearest_rank(&values, 95)));
}
