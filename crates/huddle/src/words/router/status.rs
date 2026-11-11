//! The `huddle router status` word: what answers on the port, and the record it wrote.

use std::io::Write;

use huddle_config::dashboard_hosts;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::start::{HEALTH_WAIT, HealthAnswer, RouterRecord, health, read_record};
use huddle_procs::live::is_alive;
use serde::Serialize;

pub fn run(args: &[String], env: &Env) -> i32 {
    let Some(port) = super::router_port(env) else {
        return 2;
    };
    let json = args.iter().any(|argument| argument == "--json");
    let Some(answer) = health(port, HEALTH_WAIT) else {
        return not_running(json);
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    // The record speaks for the daemon only while its pid lives and its port is the probed
    // one; a stale record leaves pid, port, version and started unknown.
    let record = read_record(&paths)
        .filter(|record| record.pid > 0 && is_alive(record.pid) && record.port == port);
    if json {
        answer_json(&answer, record.as_ref())
    } else {
        answer_plain(port, &answer, record.as_ref(), &paths)
    }
}

/// Nothing answers on the port: one line, exit 6, on stdout for `--json` and stderr otherwise.
fn not_running(json: bool) -> i32 {
    if json {
        let _ = writeln!(std::io::stdout(), "{{\"running\":false}}");
    } else {
        let _ = writeln!(std::io::stderr(), "huddle-router: not running");
    }
    6
}

fn answer_plain(
    port: u16,
    answer: &HealthAnswer,
    record: Option<&RouterRecord>,
    paths: &Paths,
) -> i32 {
    let pid = match record {
        Some(record) => record.pid.to_string(),
        None => "unknown".to_string(),
    };
    let version = record
        .map(|record| record.version.as_str())
        .filter(|version| !version.is_empty())
        .unwrap_or("unknown");
    let _ = writeln!(
        std::io::stdout(),
        "huddle-router: http://127.0.0.1:{port}\npid {pid} · version {version} · providers {}",
        answer.providers.join(", ")
    );
    let _ = writeln!(std::io::stdout(), "{}", hosts_line(paths));
    0
}

/// The dashboard hosts the next config read yields, or the error that makes the router accept
/// loopback only.
fn hosts_line(paths: &Paths) -> String {
    match dashboard_hosts::read(&dashboard_hosts::config_path(paths)) {
        Ok(hosts) if hosts.is_empty() => "dashboard hosts: none".to_string(),
        Ok(hosts) => format!("dashboard hosts: {}", hosts.join(", ")),
        Err(error) => format!("dashboard hosts: {error}"),
    }
}

/// The script answer: the keys in order, the record's parts only while the record is trusted.
#[derive(Serialize)]
struct StatusAnswer<'a> {
    running: bool,
    name: &'a str,
    providers: &'a [String],
    #[serde(skip_serializing_if = "Option::is_none")]
    pid: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    port: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    version: Option<&'a str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    started: Option<&'a str>,
}

fn answer_json(answer: &HealthAnswer, record: Option<&RouterRecord>) -> i32 {
    let line = StatusAnswer {
        running: true,
        name: &answer.name,
        providers: &answer.providers,
        pid: record.map(|record| record.pid),
        port: record.map(|record| record.port),
        version: record
            .map(|record| record.version.as_str())
            .filter(|version| !version.is_empty()),
        started: record
            .map(|record| record.started.as_str())
            .filter(|started| !started.is_empty()),
    };
    let text = match serde_json::to_string(&line) {
        Ok(text) => text,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let _ = writeln!(std::io::stdout(), "{text}");
    0
}
