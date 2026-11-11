mod e2e_support;

use e2e_support::{Daemon, raw_exchange};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::PathBuf;
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

fn huddle(home: &TempHome, port: u16, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::null())
        .output()
        .expect("run")
}

fn config_file(home: &TempHome) -> PathBuf {
    home.root().join(".config/huddle/config.toml")
}

fn out(output: &Output) -> String {
    String::from_utf8_lossy(&output.stdout).into_owned()
}

fn err(output: &Output) -> String {
    String::from_utf8_lossy(&output.stderr).into_owned()
}

#[test]
fn add_and_remove_report_each_change_and_keep_the_file_in_step() {
    let home = TempHome::new();
    let port = home.router_port();
    let file = config_file(&home);

    let added = huddle(&home, port, &["add-dashboard-host", "HUB.raouf.me"]);
    assert_eq!(added.status.code(), Some(0), "{}", err(&added));
    assert_eq!(
        out(&added),
        "huddle: hub.raouf.me added to the dashboard hosts\n"
    );
    let text = fs::read_to_string(&file).expect("config file");
    assert!(text.starts_with("# huddle settings."), "{text}");
    assert!(text.contains("hosts = [\"hub.raouf.me\"]"), "{text}");

    let again = huddle(&home, port, &["add-dashboard-host", "hub.raouf.me"]);
    assert_eq!(again.status.code(), Some(0));
    assert_eq!(
        out(&again),
        "huddle: hub.raouf.me is already a dashboard host\n"
    );
    assert_eq!(fs::read_to_string(&file).expect("config file"), text);

    let bad = huddle(&home, port, &["add-dashboard-host", "https://x"]);
    assert_eq!(bad.status.code(), Some(2));
    assert!(err(&bad).starts_with("huddle: "), "{}", err(&bad));
    assert_eq!(fs::read_to_string(&file).expect("config file"), text);

    let removed = huddle(&home, port, &["remove-dashboard-host", "hub.raouf.me"]);
    assert_eq!(removed.status.code(), Some(0), "{}", err(&removed));
    assert_eq!(
        out(&removed),
        "huddle: hub.raouf.me removed from the dashboard hosts\n"
    );
    assert!(
        fs::read_to_string(&file)
            .expect("config file")
            .contains("hosts = []")
    );

    let missing = huddle(&home, port, &["remove-dashboard-host", "hub.raouf.me"]);
    assert_eq!(missing.status.code(), Some(0));
    assert_eq!(
        out(&missing),
        "huddle: hub.raouf.me is not a dashboard host\n"
    );
}

#[test]
fn a_word_without_a_host_is_a_usage_error() {
    let home = TempHome::new();
    let output = huddle(&home, home.router_port(), &["add-dashboard-host"]);
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(err(&output), "usage: huddle add-dashboard-host <host>\n");
    let output = huddle(
        &home,
        home.router_port(),
        &["remove-dashboard-host", "a", "b"],
    );
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(err(&output), "usage: huddle remove-dashboard-host <host>\n");
}

fn host_status(port: u16, host: &str, path: &str) -> u16 {
    let reply = raw_exchange(
        port,
        &format!("GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n"),
    );
    reply
        .split_whitespace()
        .nth(1)
        .and_then(|code| code.parse().ok())
        .unwrap_or(0)
}

#[test]
fn a_started_router_serves_an_added_host_within_the_poll() {
    let daemon = Daemon::start(&[], &[]);
    let host = "hub.raouf.me";
    assert_eq!(host_status(daemon.port, host, "/"), 421);

    let added = huddle(&daemon.home, daemon.port, &["add-dashboard-host", host]);
    assert_eq!(added.status.code(), Some(0), "{}", err(&added));

    let deadline = Instant::now() + Duration::from_secs(5);
    while host_status(daemon.port, host, "/") != 200 {
        assert!(Instant::now() < deadline, "the host never reached the page");
        std::thread::sleep(Duration::from_millis(100));
    }
    assert_eq!(host_status(daemon.port, host, "/huddle-router/state"), 200);
    assert_eq!(host_status(daemon.port, host, "/huddle-router/health"), 421);
}

#[test]
fn router_status_lists_the_hosts_or_the_file_error() {
    let daemon = Daemon::start(&[], &[]);
    let status = |daemon: &Daemon| huddle(&daemon.home, daemon.port, &["router", "status"]);

    let none = status(&daemon);
    assert_eq!(none.status.code(), Some(0), "{}", err(&none));
    assert!(
        out(&none).contains("dashboard hosts: none\n"),
        "{}",
        out(&none)
    );

    let added = huddle(
        &daemon.home,
        daemon.port,
        &["add-dashboard-host", "hub.raouf.me"],
    );
    assert_eq!(added.status.code(), Some(0));
    let listed = status(&daemon);
    assert!(
        out(&listed).contains("dashboard hosts: hub.raouf.me\n"),
        "{}",
        out(&listed)
    );

    fs::write(config_file(&daemon.home), "this is = not [toml").expect("break file");
    let broken = status(&daemon);
    assert_eq!(broken.status.code(), Some(0));
    let text = out(&broken);
    let path = config_file(&daemon.home).display().to_string();
    assert!(
        text.contains(&format!("dashboard hosts: {path}: ")),
        "{text}"
    );
}
