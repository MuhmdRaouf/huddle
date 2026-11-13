//! D2: one time range for the whole dashboard, presets to 90 days and a custom from-to.

use std::collections::BTreeMap;

use huddle_history::{History, Row};
use huddle_router::history_view::{Query, Refusal, answer, empty_answer, parse};
use huddle_testkit::temp_home::TempHome;
use serde_json::Value;

const HOUR: u64 = 3_600_000;
const DAY: u64 = 24 * HOUR;
const NOW: u64 = 200 * DAY;

fn row(at_ms: u64) -> Row {
    Row {
        at_ms,
        model: "glm-5.3".into(),
        lane: "zai".into(),
        status: 200,
        dur_ms: 50,
        ..Row::default()
    }
}

fn history(rows: &[Row]) -> (TempHome, History) {
    let home = TempHome::new();
    let mut history = History::open(&home.root().join("huddle.db")).expect("open");
    history.insert(rows).expect("insert");
    (home, history)
}

fn ask(history: &History, query: &str, keep_days: u64) -> Value {
    let parsed = parse(query, NOW, keep_days).expect("parses");
    answer(history, &parsed, &BTreeMap::new()).expect("answers")
}

fn ms(value: &Value, key: &str) -> u64 {
    value[key]
        .as_u64()
        .unwrap_or_else(|| panic!("{key} in {value}"))
}

fn bucket_count(value: &Value) -> u64 {
    (ms(value, "to_ms") - ms(value, "from_ms")).div_ceil(ms(value, "width_ms"))
}

#[test]
fn d2_custom_range_buckets_and_clamps() {
    let (_home, history) = history(&[
        row(NOW - 95 * DAY),
        row(NOW - 80 * DAY),
        row(NOW - 90 * 60_000),
    ]);

    let long = ask(
        &history,
        &format!("view=buckets&from={}&to={}", NOW - 100 * DAY, NOW - HOUR),
        90,
    );
    assert_eq!(long["range"], "custom");
    assert_eq!(ms(&long, "clamped_from_ms"), NOW - 90 * DAY);
    assert_eq!(ms(&long, "from_ms"), NOW - 90 * DAY);
    assert_eq!(ms(&long, "to_ms"), NOW - HOUR);
    let buckets = long["buckets"].as_array().expect("buckets");
    assert_eq!(
        buckets.len(),
        2,
        "the 95-day-old row is outside the kept history"
    );
    assert!(
        buckets
            .iter()
            .all(|b| ms(b, "start_ms") + ms(&long, "width_ms") > NOW - 90 * DAY)
    );
    assert!((30..=120).contains(&bucket_count(&long)), "{long}");

    let short = ask(
        &history,
        &format!("view=buckets&from={}&to={}", NOW - 2 * HOUR, NOW),
        90,
    );
    assert!(short.get("clamped_from_ms").is_none(), "{short}");
    assert_eq!(ms(&short, "from_ms"), NOW - 2 * HOUR);
    assert_eq!(short["buckets"].as_array().map(Vec::len), Some(1));
    assert!((30..=120).contains(&bucket_count(&short)), "{short}");

    let search = ask(
        &history,
        &format!("view=search&from={}&to={}", NOW - DAY, NOW),
        90,
    );
    assert_eq!(search["total"], 1);

    for bad in [
        format!("view=buckets&from={}", NOW - HOUR),
        format!("view=buckets&to={}", NOW),
        format!("view=buckets&from={}&to={}", NOW, NOW - HOUR),
        format!("view=buckets&from=x&to={NOW}"),
        format!(
            "view=buckets&from={}&to={}",
            NOW - 120 * DAY,
            NOW - 100 * DAY
        ),
    ] {
        assert!(
            matches!(parse(&bad, NOW, 90), Err(Refusal::Bad(_))),
            "{bad}"
        );
    }
}

#[test]
fn d2_one_and_ninety_day_presets_parse() {
    let (_home, history) = history(&[row(NOW - 30 * 60_000), row(NOW - 60 * DAY)]);
    for (range, from) in [
        ("1h", NOW - HOUR),
        ("90d", NOW - 90 * DAY),
        ("15m", NOW - 900_000),
    ] {
        let got = ask(&history, &format!("view=buckets&range={range}"), 90);
        assert_eq!(got["range"], range);
        assert_eq!(ms(&got, "from_ms"), from, "{range}");
        assert!(got.get("clamped_from_ms").is_none());
        assert!((30..=120).contains(&bucket_count(&got)), "{range}: {got}");
    }
    let capped = ask(&history, "view=buckets&range=90d", 30);
    assert_eq!(ms(&capped, "clamped_from_ms"), NOW - 30 * DAY);
    assert_eq!(capped["buckets"].as_array().map(Vec::len), Some(1));
    for range in ["24h", "7d", "30d"] {
        let got = ask(&history, &format!("view=totals&range={range}"), 90);
        assert_eq!(got["range"], range);
    }
    assert!(matches!(
        parse("view=buckets&range=2d", NOW, 90),
        Err(Refusal::Bad(_))
    ));
}

#[test]
fn d2_meta_view_tells_the_page_how_much_history_is_kept() {
    let parsed = parse("view=meta", NOW, 45).expect("parses");
    assert!(matches!(parsed, Query::Meta { keep_days: 45 }));
    let (_home, history) = history(&[]);
    for got in [
        answer(&history, &parsed, &BTreeMap::new()).expect("answers"),
        empty_answer(&parsed),
    ] {
        assert_eq!(got["view"], "meta");
        assert_eq!(got["keep_days"], 45);
    }
}
