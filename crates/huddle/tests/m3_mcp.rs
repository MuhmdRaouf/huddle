use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Stdio};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_history::{History, Row};
use huddle_testkit::temp_home::TempHome;
use serde_json::{Value, json};

const SENTINEL_KEY: &str = "sk-m3-sentinel-0123456789abcdef";

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
                "clientInfo": {"name": "m3-test", "version": "0"}
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

fn row(at_ms: u64, lane: &str, model: &str, status: u16, session: &str, tokens: u64) -> Row {
    Row {
        at_ms,
        session: Some(session.to_string()),
        project: Some("huddle".to_string()),
        model: model.to_string(),
        lane: lane.to_string(),
        status,
        dur_ms: 10,
        tokens_in: Some(tokens),
        cache_read: Some(tokens * 2),
        cache_write: Some(tokens * 3),
        tokens_out: Some(tokens * 4),
        ..Row::default()
    }
}

fn seed_history(home: &TempHome) {
    let paths = paths_of(home);
    std::fs::create_dir_all(&paths.state_dir).unwrap();
    let mut history = History::open(&paths.state_dir.join("huddle.db")).unwrap();
    let now = now_ms();
    let hour = 3_600_000;
    history
        .insert(&[
            row(now - 60_000, "zai", "glm-5.3", 200, "s1", 1_000),
            row(now - 120_000, "zai", "glm-5.3", 500, "s1", 1_000),
            row(now - 180_000, "kimi", "kimi-k2.6", 200, "s2", 2_000),
            row(now - 5 * hour, "zai", "glm-5.3-flash", 200, "s3", 3_000),
            row(now - 3 * 24 * hour, "kimi", "kimi-k2.6", 200, "s2", 4_000),
        ])
        .unwrap();
}

fn group<'a>(answer: &'a Value, key: &str) -> &'a Value {
    answer["groups"]
        .as_array()
        .unwrap()
        .iter()
        .find(|group| group["key"] == key)
        .unwrap_or_else(|| panic!("no group {key} in {answer}"))
}

#[test]
fn m3_usage_groups_and_ranges() {
    let home = TempHome::new();
    seed_history(&home);
    let mut session = Session::start(&home, &[]);

    let by_provider = session.tool("huddle_usage", json!({"range": "24h"}));
    assert_eq!(by_provider["group_by"], "provider");
    let zai = group(&by_provider, "zai");
    assert_eq!(zai["requests"], 3);
    assert_eq!(zai["errors"], 1);
    assert_eq!(zai["tokens"]["fresh"], 5_000);
    assert_eq!(zai["tokens"]["cache_read"], 10_000);
    assert_eq!(zai["tokens"]["cache_write"], 15_000);
    assert_eq!(zai["tokens"]["output"], 20_000);
    assert!(zai["cost_usd"].as_f64().unwrap() > 0.0);
    assert_eq!(group(&by_provider, "kimi")["requests"], 1);
    assert_eq!(by_provider["groups"].as_array().unwrap().len(), 2);

    let short = session.tool("huddle_usage", json!({"range": "15m"}));
    assert_eq!(group(&short, "zai")["requests"], 2);

    let week = session.tool("huddle_usage", json!({"range": "7d", "group_by": "model"}));
    assert_eq!(group(&week, "kimi-k2.6")["requests"], 2);
    assert_eq!(group(&week, "glm-5.3-flash")["requests"], 1);

    let by_session = session.tool(
        "huddle_usage",
        json!({"range": "7d", "group_by": "session"}),
    );
    assert_eq!(group(&by_session, "s1")["requests"], 2);

    let by_project = session.tool(
        "huddle_usage",
        json!({"range": "7d", "group_by": "project"}),
    );
    assert_eq!(group(&by_project, "huddle")["requests"], 5);

    let now = now_ms();
    let window = session.tool(
        "huddle_usage",
        json!({"from_ms": now - 400_000, "to_ms": now, "group_by": "provider"}),
    );
    assert_eq!(window["range"]["from_ms"], now - 400_000);
    assert_eq!(group(&window, "zai")["requests"], 2);
    assert_eq!(group(&window, "kimi")["requests"], 1);

    let refused = session.call(
        "tools/call",
        json!({"name": "huddle_usage", "arguments": {"range": "2w"}}),
    );
    assert!(
        refused["error"].is_object() || refused["result"]["isError"] == true,
        "{refused}"
    );
    session.finish();
}

#[test]
fn m3_usage_is_empty_without_a_database() {
    let home = TempHome::new();
    let mut session = Session::start(&home, &[]);
    let answer = session.tool("huddle_usage", json!({}));
    assert_eq!(answer["groups"].as_array().unwrap().len(), 0);
    session.finish();
}

fn quota_of<'a>(answer: &'a Value, id: &str) -> &'a Value {
    answer["providers"]
        .as_array()
        .unwrap()
        .iter()
        .find(|provider| provider["id"] == id)
        .unwrap_or_else(|| panic!("no provider {id} in {answer}"))
}

#[test]
fn m3_quota_reads_the_router_or_says_unknown() {
    let home = TempHome::new();
    let paths = paths_of(&home);
    std::fs::create_dir_all(&paths.config_dir).unwrap();
    huddle_config::providers_file::set_enabled(&paths.config_dir, "zai", true).unwrap();

    let mut down = Session::start(&home, &[]);
    let answer = down.tool("huddle_quota", json!({}));
    assert_eq!(answer["router_up"], false);
    let zai = quota_of(&answer, "zai");
    assert_eq!(zai["status"], "unknown");
    assert!(zai["note"].as_str().unwrap().contains("router down"));
    down.finish();

    let started = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "start"])
        .output()
        .unwrap();
    assert!(started.status.success(), "{started:?}");
    assert!(home.settle_router(Duration::from_secs(20)));

    let mut up = Session::start(&home, &[]);
    let answer = up.tool("huddle_quota", json!({}));
    assert_eq!(answer["router_up"], true);
    let anthropic = quota_of(&answer, "anthropic");
    assert_eq!(anthropic["kind"], "plan");
    assert_eq!(anthropic["windows"].as_array().unwrap().len(), 2);
    assert_eq!(quota_of(&answer, "zai")["status"], "unknown");
    up.finish();
}

#[test]
fn m3_providers_never_carry_a_key() {
    let home = TempHome::new();
    let paths = paths_of(&home);
    std::fs::create_dir_all(&paths.config_dir).unwrap();
    huddle_config::providers_file::set_enabled(&paths.config_dir, "zai", true).unwrap();
    let agents = paths.claude_dir.join("agents");
    std::fs::create_dir_all(&agents).unwrap();
    std::fs::write(agents.join("glm-5.3.md"), "managed-by: huddle\n").unwrap();

    let mut session = Session::start(&home, &[("ZAI_API_KEY", SENTINEL_KEY)]);
    let answer = session.tool("huddle_providers", json!({}));
    let providers = answer["providers"].as_array().unwrap();
    assert_eq!(providers.len(), 5);
    let zai = providers.iter().find(|p| p["id"] == "zai").unwrap();
    assert_eq!(zai["lane"], true);
    assert_eq!(zai["key_present"], true);
    assert_eq!(zai["agents"], json!(["glm-5.3"]));
    assert!(zai["models"].as_array().unwrap().len() >= 2);
    assert_eq!(zai["next_step"], "ready");
    let kimi = providers.iter().find(|p| p["id"] == "kimi").unwrap();
    assert_eq!(kimi["lane"], false);
    assert_eq!(kimi["key_present"], false);
    assert!(
        kimi["next_step"]
            .as_str()
            .unwrap()
            .contains("/huddle:kimi:setup")
    );
    for line in session.finish() {
        assert!(!line.contains(SENTINEL_KEY), "{line}");
    }
}
