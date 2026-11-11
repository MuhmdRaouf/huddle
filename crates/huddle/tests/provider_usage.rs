use huddle_router::events::{iso_utc_ms, now_ms, utc_date};
use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    process::{Command, Stdio},
};

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

#[test]
fn an_empty_home_reports_no_requests() {
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
}

#[test]
fn only_this_providers_provider_route_counts() {
    let home = TempHome::new();
    let dir = home.root().join(".local/state/huddle/events");
    fs::create_dir_all(&dir).expect("mkdir");
    let now = now_ms();
    let ts = iso_utc_ms(now);
    let today = format!(
        concat!(
            "{{\"ts\":\"{ts}\",\"plugin\":\"zai\",\"model\":\"glm-5.3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":1000,\"output_tokens\":100,\"cache_read_input_tokens\":9000}}}}\n",
            "{{\"ts\":\"{ts}\",\"plugin\":\"zai\",\"model\":\"claude-x\",\"route\":\"anthropic\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":99}}}}\n",
            "{{\"ts\":\"{ts}\",\"plugin\":\"kimi\",\"model\":\"kimi-k3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":99}}}}\n",
            "not json\n",
        ),
        ts = ts
    );
    fs::write(dir.join(format!("{}.jsonl", utc_date(now))), today).expect("write");
    let text = usage(&home, "zai");
    assert!(
        text.contains("Est. cost: today $0.0042 \u{b7} 7 days $0.0042 \u{b7} 30 days $0.0042"),
        "{text}"
    );
    assert!(text.contains("Estimated from Z.ai GLM list prices read 2026-10-08; your bill is the provider's. Token counts are exact."), "{text}");
    assert!(text.contains("Today, agents and /model\nMODEL    REQ  IN    OUT  CACHE R  CACHE W  EST COST\nglm-5.3  1    1.0k  100  9.0k     0        $0.0042\nTOTAL    1    1.0k  100  9.0k     0        $0.0042\n"), "{text}");
}

#[test]
fn an_event_outside_the_window_is_not_in_today() {
    let home = TempHome::new();
    let dir = home.root().join(".local/state/huddle/events");
    fs::create_dir_all(&dir).expect("mkdir");
    let now = now_ms();
    let ten_days_ago = now - 10 * 86_400_000;
    let line = format!(
        "{{\"ts\":\"{}\",\"plugin\":\"zai\",\"model\":\"glm-5.3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":1000000}}}}\n",
        iso_utc_ms(ten_days_ago)
    );
    fs::write(dir.join(format!("{}.jsonl", utc_date(ten_days_ago))), line).expect("write");
    let text = usage(&home, "zai");
    assert!(
        text.contains("Today: no agent or /model requests."),
        "{text}"
    );
    assert!(text.contains("30 days $1.40"), "{text}");
}

#[test]
fn a_torn_last_line_is_not_counted() {
    let home = TempHome::new();
    let dir = home.root().join(".local/state/huddle/events");
    fs::create_dir_all(&dir).expect("mkdir");
    let now = now_ms();
    let ts = iso_utc_ms(now);
    let whole = format!(
        "{{\"ts\":\"{ts}\",\"plugin\":\"zai\",\"model\":\"glm-5.3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":1000}}}}\n",
        ts = ts
    );
    let torn = format!(
        "{{\"ts\":\"{ts}\",\"plugin\":\"zai\",\"model\":\"glm-5.3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":1000,",
        ts = ts
    );
    let path = dir.join(format!("{}.jsonl", utc_date(now)));
    fs::write(&path, format!("{whole}{torn}")).expect("write");
    let text = usage(&home, "zai");
    assert!(
        text.contains("glm-5.3  1    1.0k  0    0        0        $0.0014\nTOTAL    1    1.0k  0    0        0        $0.0014\n"),
        "{text}"
    );
    fs::write(&path, format!("{whole}{whole}")).expect("complete");
    let text = usage(&home, "zai");
    assert!(
        text.contains("glm-5.3  2    2.0k  0    0        0        $0.0028\nTOTAL    2    2.0k  0    0        0        $0.0028\n"),
        "{text}"
    );
}

#[test]
fn a_line_that_does_not_parse_is_skipped() {
    let home = TempHome::new();
    let dir = home.root().join(".local/state/huddle/events");
    fs::create_dir_all(&dir).expect("mkdir");
    let now = now_ms();
    let ts = iso_utc_ms(now);
    let good = format!(
        "{{\"ts\":\"{ts}\",\"plugin\":\"zai\",\"model\":\"glm-5.3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":1,\"usage\":{{\"input_tokens\":1000}}}}\n",
        ts = ts
    );
    // Marked as this provider's provider route, but cut mid-JSON: the skip must come from the
    // parse gate, not from the plugin or route filters.
    let torn = format!(
        "{{\"ts\":\"{ts}\",\"plugin\":\"zai\",\"route\":\"provider\",\"model\":\"glm-5.3\",\"stat",
        ts = ts
    );
    fs::write(
        dir.join(format!("{}.jsonl", utc_date(now))),
        format!("{good}{torn}\n"),
    )
    .expect("write");
    let text = usage(&home, "zai");
    assert!(text.contains("glm-5.3  1    1.0k"), "{text}");
}
