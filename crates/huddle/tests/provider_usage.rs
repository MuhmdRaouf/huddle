use huddle_history::{History, Row};
use huddle_router::clock::now_ms;
use huddle_testkit::temp_home::TempHome;
use std::process::{Command, Stdio};

const DAY_MS: u64 = 86_400_000;

fn usage(home: &TempHome, provider: &str) -> String {
    let output = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["provider", provider, "usage"])
        .env_clear()
        .envs(home.env())
        .stdin(Stdio::null())
        .output()
        .expect("run");
    assert_eq!(output.status.code(), Some(0), "{output:?}");
    String::from_utf8(output.stdout).expect("utf8")
}

fn now() -> u64 {
    u64::try_from(now_ms()).expect("clock")
}

/// A finished request on `lane`; `tokens_in` is the prompt total, cache tokens included.
fn row(lane: &str, model: &str, at_ms: u64, tokens_in: Option<u64>, cache_read: u64) -> Row {
    Row {
        at_ms,
        model: model.into(),
        lane: lane.into(),
        status: 200,
        dur_ms: 1,
        tokens_in,
        cache_read: tokens_in.map(|_| cache_read),
        cache_write: tokens_in.map(|_| 0),
        tokens_out: tokens_in.map(|_| 100),
        ..Row::default()
    }
}

fn store(home: &TempHome, rows: &[Row]) {
    let db = home.root().join(".local/state/huddle/huddle.db");
    History::open(&db)
        .expect("open")
        .insert(rows)
        .expect("insert");
}

#[test]
fn t5_an_empty_home_reports_no_requests() {
    let home = TempHome::new();
    let text = usage(&home, "zai");
    assert!(text.starts_with("Z.ai GLM usage\n"), "{text}");
    assert!(
        text.contains("Est. cost: today $0.0000 \u{b7} 7 days $0.0000 \u{b7} 30 days $0.0000"),
        "{text}"
    );
    assert!(
        text.contains("Today: no agent or /model requests."),
        "{text}"
    );
    assert!(!home.root().join(".local/state/huddle/huddle.db").exists());
}

#[test]
fn t5_only_this_providers_lane_counts() {
    let home = TempHome::new();
    let at = now();
    store(
        &home,
        &[
            row("zai", "glm-5.3", at, Some(10_000), 9_000),
            row("anthropic", "claude-x", at, Some(99), 0),
            row("kimi", "kimi-k3", at, Some(99), 0),
        ],
    );
    let text = usage(&home, "zai");
    assert!(
        text.contains("Est. cost: today $0.0042 \u{b7} 7 days $0.0042 \u{b7} 30 days $0.0042"),
        "{text}"
    );
    assert!(text.contains("Estimated from Z.ai GLM list prices read 2026-10-08; your bill is the provider's. Token counts are exact."), "{text}");
    assert!(text.contains("Today, agents and /model\nMODEL    REQ  IN    OUT  CACHE R  CACHE W  EST COST\nglm-5.3  1    1.0k  100  9.0k     0        $0.0042\nTOTAL    1    1.0k  100  9.0k     0        $0.0042\n"), "{text}");
}

#[test]
fn t5_a_request_outside_the_window_is_not_in_today() {
    let home = TempHome::new();
    store(
        &home,
        &[row(
            "zai",
            "glm-5.3",
            now() - 10 * DAY_MS,
            Some(1_000_000),
            0,
        )],
    );
    let text = usage(&home, "zai");
    assert!(
        text.contains("Today: no agent or /model requests."),
        "{text}"
    );
    assert!(text.contains("30 days $1.40"), "{text}");
}

#[test]
fn t5_a_request_with_unknown_tokens_counts_as_a_request_and_costs_nothing() {
    let home = TempHome::new();
    store(&home, &[row("zai", "glm-5.3", now(), None, 0)]);
    let text = usage(&home, "zai");
    assert!(
        text.contains("glm-5.3  1    0   0    0        0        $0.0000\nTOTAL    1    0   0    0        0        $0.0000\n"),
        "{text}"
    );
}
