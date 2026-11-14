mod common;

use common::*;
use huddle_history::{GroupBy, Range};
use huddle_testkit::temp_home::TempHome;

const HOUR: u64 = 3_600_000;

/// One request every 7 hours for 40 days, so a range is cut into slices and every day holds
/// requests on both sides of any zone's midnight.
fn seeded_forty_days(home: &TempHome) -> huddle_history::History {
    let mut history = open(home);
    let rows: Vec<_> = (0..40 * 24 / 7)
        .map(|i| row(10 * DAY + i * 7 * HOUR + 1, "m"))
        .collect();
    history.insert(&rows).expect("insert");
    history
}

fn span() -> Range {
    Range {
        from_ms: 10 * DAY,
        to_ms: 50 * DAY,
    }
}

#[test]
fn k1_zone_day_totals_match_a_row_by_row_count() {
    let home = TempHome::new();
    let history = seeded_forty_days(&home);
    for offset_min in [0, 120, -300, 330, 840, -840] {
        let offset_ms = i64::from(offset_min) * 60_000;
        let mut want = std::collections::BTreeMap::<i64, u64>::new();
        for i in 0..(40 * 24 / 7) {
            let at = (10 * DAY + i * 7 * HOUR + 1) as i64 + offset_ms;
            *want.entry(at.div_euclid(DAY as i64)).or_default() += 1;
        }
        let got: Vec<u64> = history
            .totals_in(span(), GroupBy::Day, offset_min)
            .expect("totals")
            .iter()
            .map(|t| t.n)
            .collect();
        assert_eq!(
            got,
            want.values().copied().collect::<Vec<_>>(),
            "{offset_min}"
        );
    }
}

#[test]
fn k1_zone_buckets_are_unique_aligned_and_complete_across_slices() {
    let home = TempHome::new();
    let history = seeded_forty_days(&home);
    for offset_min in [0, 120, -300, 330] {
        let offset_ms = i64::from(offset_min) * 60_000;
        let buckets = history
            .buckets_in(span(), DAY, None, None, offset_min)
            .expect("buckets");
        let starts: Vec<u64> = buckets.iter().map(|b| b.start_ms).collect();
        let mut sorted = starts.clone();
        sorted.dedup();
        assert_eq!(starts, sorted, "{offset_min}: one bucket per day, in order");
        assert!(
            starts
                .iter()
                .all(|s| (*s as i64 + offset_ms).rem_euclid(DAY as i64) == 0),
            "{offset_min}: starts sit on the zone's midnight"
        );
        assert_eq!(buckets.iter().map(|b| b.n).sum::<u64>(), 40 * 24 / 7);
    }
}
