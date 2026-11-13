use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Stdio};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_history::{History, Row};
use huddle_testkit::temp_home::TempHome;
use serde_json::{Value, json};

struct Session {
    child: Child,
    reader: BufReader<std::process::ChildStdout>,
    lines: Vec<String>,
    next_id: u64,
}

impl Session {
    fn start(home: &TempHome, extra_env: &[(&str, &str)]) -> Session {
        let mut command = home.command(env!("CARGO_BIN_EXE_huddle"));
        command.envs(extra_env.iter().copied());
        let mut child = command
            .arg("mcp")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let reader = BufReader::new(child.stdout.take().unwrap());
        let mut session = Session {
            child,
            reader,
            lines: Vec::new(),
            next_id: 1,
        };
        session.call(
            "initialize",
            json!({
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "m4-test", "version": "0"}
            }),
        );
        session.send(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
        session
    }

    fn send(&mut self, message: Value) {
        let stdin = self.child.stdin.as_mut().unwrap();
        writeln!(stdin, "{message}").unwrap();
        stdin.flush().unwrap();
    }

    fn call(&mut self, method: &str, params: Value) -> Value {
        let id = self.next_id;
        self.next_id += 1;
        self.send(json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params}));
        let mut line = String::new();
        self.reader.read_line(&mut line).unwrap();
        self.lines.push(line.clone());
        serde_json::from_str(&line).unwrap()
    }

    fn tool(&mut self, name: &str, arguments: Value) -> Value {
        let answer = self.call("tools/call", json!({"name": name, "arguments": arguments}));
        assert!(answer["error"].is_null(), "{answer}");
        assert_ne!(answer["result"]["isError"], true, "{answer}");
        answer["result"]["structuredContent"].clone()
    }

    fn finish(mut self) -> Vec<String> {
        drop(self.child.stdin.take());
        let _ = self.child.wait();
        self.lines
    }
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_millis() as u64
}

fn paths_of(home: &TempHome) -> Paths {
    let pairs = home.env();
    let borrowed: Vec<(&str, &str)> = pairs
        .iter()
        .map(|(name, value)| (name.as_str(), value.as_str()))
        .collect();
    Paths::from_env(&Env::with(&borrowed)).unwrap()
}

fn err_row(at_ms: u64, lane: &str, model: &str, status: u16, reason: &str) -> Row {
    Row {
        at_ms,
        model: model.to_string(),
        lane: lane.to_string(),
        status,
        reason: Some(reason.to_string()),
        dur_ms: 10,
        ..Row::default()
    }
}

fn seed_errors(home: &TempHome) -> u64 {
    let paths = paths_of(home);
    std::fs::create_dir_all(&paths.state_dir).unwrap();
    let mut history = History::open(&paths.state_dir.join("huddle.db")).unwrap();
    let now = now_ms();
    history
        .insert(&[
            err_row(
                now - 300_000,
                "zai",
                "glm-5.3",
                429,
                "quota exceeded for this plan",
            ),
            err_row(
                now - 200_000,
                "zai",
                "glm-5.3",
                429,
                "quota exceeded for this plan",
            ),
            err_row(
                now - 100_000,
                "zai",
                "glm-5.3",
                429,
                "quota exceeded for this plan",
            ),
            err_row(now - 90_000, "kimi", "kimi-k2.6", 529, "overloaded"),
            err_row(now - 80_000, "kimi", "kimi-k2.6", 500, "boom"),
            err_row(now - 70_000, "zai", "glm-5.3", 200, "fine"),
            err_row(
                now - 3 * 24 * 3_600_000,
                "zai",
                "glm-5.3",
                429,
                "quota exceeded for this plan",
            ),
        ])
        .unwrap();
    now
}

fn find<'a>(answer: &'a Value, provider: &str, status: u64) -> &'a Value {
    answer["groups"]
        .as_array()
        .unwrap()
        .iter()
        .find(|group| group["provider"] == provider && group["status"] == status)
        .unwrap_or_else(|| panic!("no group {provider} {status} in {answer}"))
}

#[test]
fn m4_errors_group_by_provider_and_reason() {
    let home = TempHome::new();
    let now = seed_errors(&home);
    let mut session = Session::start(&home, &[]);

    let answer = session.tool("huddle_errors", json!({"range": "24h"}));
    assert_eq!(answer["total"], 5);
    let quota = find(&answer, "zai", 429);
    assert_eq!(quota["count"], 3);
    assert_eq!(quota["model"], "glm-5.3");
    assert_eq!(quota["reason"], "quota used up");
    assert!(quota["first_ms"].as_u64().unwrap() >= now - 300_000 - 1_000);
    assert!(quota["last_ms"].as_u64().unwrap() <= now - 100_000 + 1_000);
    assert!(quota["first_ms"].as_u64() < quota["last_ms"].as_u64());
    assert_eq!(find(&answer, "kimi", 529)["reason"], "overloaded");
    assert_eq!(find(&answer, "kimi", 500)["count"], 1);
    assert_eq!(answer["groups"].as_array().unwrap().len(), 3);
    assert_eq!(answer["groups"][0]["count"], 3);

    let week = session.tool("huddle_errors", json!({"range": "7d"}));
    assert_eq!(find(&week, "zai", 429)["count"], 4);

    let narrow = session.tool("huddle_errors", json!({"range": "15m"}));
    assert_eq!(narrow["total"], 5);
    let text = narrow.to_string();
    assert!(!text.contains("quota exceeded for this plan"), "{text}");
    session.finish();
}

#[test]
fn m4_errors_are_empty_without_a_database() {
    let home = TempHome::new();
    let mut session = Session::start(&home, &[]);
    let answer = session.tool("huddle_errors", json!({}));
    assert_eq!(answer["total"], 0);
    assert_eq!(answer["groups"].as_array().unwrap().len(), 0);
    session.finish();
}

#[test]
fn m4_status_up_and_down() {
    let home = TempHome::new();
    let paths = paths_of(&home);
    std::fs::create_dir_all(&paths.state_dir).unwrap();
    std::fs::write(
        paths.state_dir.join("router-events.log"),
        "2026-11-13T09:00:00.000Z huddle-router: start v0.0.7 pid=11 port=4835 by=test\n\
         2026-11-13T09:05:00.000Z huddle-router: exit pid=11 reason=SIGTERM\n",
    )
    .unwrap();

    let mut down = Session::start(&home, &[]);
    let answer = down.tool("huddle_router_status", json!({}));
    assert_eq!(answer["up"], false);
    assert!(
        answer["last_exit"]
            .as_str()
            .unwrap()
            .contains("exit pid=11 reason=SIGTERM"),
        "{answer}"
    );
    assert!(
        answer["start_command"]
            .as_str()
            .unwrap()
            .contains("huddle router start"),
        "{answer}"
    );
    assert!(answer["pid"].is_null());
    down.finish();

    let started = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "start"])
        .output()
        .unwrap();
    assert!(started.status.success(), "{started:?}");
    assert!(home.settle_router(Duration::from_secs(20)));

    let mut up = Session::start(&home, &[]);
    let answer = up.tool("huddle_router_status", json!({}));
    assert_eq!(answer["up"], true);
    assert!(answer["pid"].as_u64().unwrap() > 0);
    assert!(!answer["version"].as_str().unwrap().is_empty());
    assert!(answer["uptime_ms"].as_u64().is_some());
    assert!(answer["providers"].is_array());
    assert_eq!(answer["anthropic_path"]["state"], "ok");
    assert!(answer["events"].as_array().unwrap().len() <= 5);
    assert!(answer["last_exit"].is_null());
    up.finish();
}

fn cli_log(home: &TempHome, args: &[&str]) -> Vec<String> {
    let out = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "log"])
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
    String::from_utf8(out.stdout)
        .unwrap()
        .lines()
        .map(str::to_owned)
        .collect()
}

fn tool_log(session: &mut Session, arguments: Value) -> Vec<String> {
    session.tool("huddle_router_log", arguments)["lines"]
        .as_array()
        .unwrap()
        .iter()
        .map(|line| line.as_str().unwrap().to_owned())
        .collect()
}

#[test]
fn m4_log_matches_the_cli_word() {
    let home = TempHome::new();
    let paths = paths_of(&home);
    std::fs::create_dir_all(&paths.state_dir).unwrap();
    let stamp = |minutes_ago: u64| {
        huddle_router::clock::iso_utc_ms((now_ms() - minutes_ago * 60_000) as i64)
    };
    let events: String = (0..60)
        .map(|n| format!("{} huddle-router: probe number {n}\n", stamp(120 - n)))
        .collect();
    let requests: String = (0..5)
        .map(|n| format!("{} POST /v1/messages 200 {n}ms\n", stamp(50 - n)))
        .collect();
    std::fs::write(paths.state_dir.join("router-events.log"), &events).unwrap();
    std::fs::write(
        paths.state_dir.join("router.log"),
        format!("{events}{requests}"),
    )
    .unwrap();

    let mut session = Session::start(&home, &[]);
    let default = tool_log(&mut session, json!({}));
    assert_eq!(default, cli_log(&home, &[]));
    assert_eq!(default.len(), 40);
    assert!(default.iter().all(|line| line.contains("huddle-router:")));

    let requests = tool_log(&mut session, json!({"kind": "requests"}));
    assert_eq!(requests, cli_log(&home, &["--requests"]));
    assert_eq!(requests.len(), 5);

    let all_since = tool_log(&mut session, json!({"kind": "all", "since": "90m"}));
    assert_eq!(all_since, cli_log(&home, &["--all", "--since", "90m"]));

    let counted = tool_log(&mut session, json!({"lines": 3}));
    assert_eq!(counted, default[default.len() - 3..]);

    let refused = session.call(
        "tools/call",
        json!({"name": "huddle_router_log", "arguments": {"since": "soon"}}),
    );
    assert!(
        refused["error"].is_object() || refused["result"]["isError"] == true,
        "{refused}"
    );
    session.finish();
}
