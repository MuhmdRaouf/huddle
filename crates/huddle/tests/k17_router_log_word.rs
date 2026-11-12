//! K17: `huddle router log` reads the router's log files and needs no router.

use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::process::{Command, Output};

fn huddle(home: &TempHome, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", free_port().to_string())
        .output()
        .expect("run huddle")
}

fn state_dir(home: &TempHome) -> std::path::PathBuf {
    let dir = home.root().join(".local/state/huddle");
    fs::create_dir_all(&dir).expect("state dir");
    dir
}

fn ago(minutes: i64) -> String {
    huddle_router::clock::iso_utc_ms(huddle_router::clock::now_ms() - minutes * 60_000)
}

fn stdout(output: &Output) -> String {
    String::from_utf8_lossy(&output.stdout).into_owned()
}

fn write_logs(home: &TempHome) {
    let dir = state_dir(home);
    let events = [
        format!("{} huddle-router: start pid=1 reason=hook", ago(300)),
        format!(
            "{} huddle-router: Anthropic path failing (ETIMEDOUT)",
            ago(20)
        ),
        format!("{} huddle-router: Anthropic path recovered", ago(10)),
    ];
    let requests = [
        format!("{} POST /v1/messages model=- -> anthropic 200", ago(30)),
        events[1].clone(),
        format!("{} POST /v1/messages model=- -> anthropic 502", ago(15)),
        events[2].clone(),
    ];
    fs::write(dir.join("router-events.log"), events.join("\n") + "\n").expect("events");
    fs::write(dir.join("router.log"), requests.join("\n") + "\n").expect("requests");
}

#[test]
fn k17_router_log_word_prints_the_last_events() {
    let home = TempHome::new();
    write_logs(&home);

    let events = huddle(&home, &["router", "log"]);
    assert_eq!(events.status.code(), Some(0));
    let text = stdout(&events);
    let lines: Vec<&str> = text.lines().collect();
    assert_eq!(lines.len(), 3, "{text}");
    assert!(lines[0].contains("start pid=1"), "{text}");
    assert!(lines[2].contains("recovered"), "{text}");
    assert!(!text.contains("POST"), "{text}");

    let since = stdout(&huddle(&home, &["router", "log", "--since", "30m"]));
    assert_eq!(since.lines().count(), 2, "{since}");
    assert!(!since.contains("start pid=1"), "{since}");

    let requests = stdout(&huddle(&home, &["router", "log", "--requests"]));
    assert_eq!(requests.lines().count(), 2, "{requests}");
    assert!(
        requests.lines().all(|line| line.contains("POST")),
        "{requests}"
    );

    let all = stdout(&huddle(&home, &["router", "log", "--all"]));
    let all: Vec<&str> = all.lines().collect();
    assert_eq!(all.len(), 5, "{all:?}");
    assert!(all[0].contains("start pid=1"), "{all:?}");
    assert!(all[1].contains("POST") && all[1].contains("200"), "{all:?}");
    assert!(all[4].contains("recovered"), "{all:?}");
    println!("sample of `huddle router log --all`:\n{}", all.join("\n"));
}

#[test]
fn k17_router_log_word_reads_rotated_files_and_caps_at_forty() {
    let home = TempHome::new();
    let dir = state_dir(&home);
    let old: Vec<String> = (0..30)
        .map(|n| format!("{} huddle-router: probe old{n:02}", ago(600 - n)))
        .collect();
    let new: Vec<String> = (0..30)
        .map(|n| format!("{} huddle-router: probe new{n:02}", ago(100 - n)))
        .collect();
    fs::write(dir.join("router-events.log.1"), old.join("\n") + "\n").expect("old");
    fs::write(dir.join("router-events.log"), new.join("\n") + "\n").expect("new");

    let text = stdout(&huddle(&home, &["router", "log"]));
    let lines: Vec<&str> = text.lines().collect();
    assert_eq!(lines.len(), 40, "{text}");
    assert!(lines[0].contains("old20"), "{text}");
    assert!(lines[39].contains("new29"), "{text}");

    let since = stdout(&huddle(&home, &["router", "log", "--since", "12h"]));
    assert_eq!(since.lines().count(), 60, "{since}");
}

#[test]
fn k17_router_log_word_never_needs_a_router() {
    let home = TempHome::new();
    let none = huddle(&home, &["router", "log"]);
    assert_eq!(none.status.code(), Some(0), "{none:?}");
    assert!(stdout(&none).is_empty());

    write_logs(&home);
    let output = huddle(&home, &["router", "log"]);
    assert_eq!(output.status.code(), Some(0));
    assert!(stdout(&output).contains("Anthropic path failing"));

    let bad = huddle(&home, &["router", "log", "--since", "soon"]);
    assert_eq!(bad.status.code(), Some(2));
}
