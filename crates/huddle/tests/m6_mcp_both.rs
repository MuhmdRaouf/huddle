mod e2e_support;

use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};

use e2e_support::{Daemon, raw_exchange};
use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use serde_json::{Map, Value, json};

fn plugin_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../plugin")
}

fn mcp_json() -> Value {
    let text = fs::read_to_string(plugin_dir().join(".mcp.json")).expect("plugin/.mcp.json");
    serde_json::from_str(&text).expect("json")
}

#[test]
fn m6_the_plugin_declares_both_servers() {
    let servers = &mcp_json()["mcpServers"];
    let mut names: Vec<&str> = servers
        .as_object()
        .expect("mcpServers")
        .keys()
        .map(String::as_str)
        .collect();
    names.sort_unstable();
    assert_eq!(names, ["huddle", "huddle-http"]);

    let http = &servers["huddle-http"];
    assert_eq!(http["type"], "http");
    assert_eq!(http["url"], "http://127.0.0.1:4835/mcp");

    let stdio = &servers["huddle"];
    assert_eq!(stdio["command"], "sh");
    assert!(
        stdio["type"].is_null() || stdio["type"] == "stdio",
        "the huddle server is stdio"
    );
}

/// The stdio line runs the installed binary with `mcp`, from the data home the hooks use.
#[test]
fn m6_the_stdio_line_runs_the_installed_binary() {
    let stdio = &mcp_json()["mcpServers"]["huddle"];
    let args: Vec<&str> = stdio["args"]
        .as_array()
        .expect("args")
        .iter()
        .map(|arg| arg.as_str().expect("string"))
        .collect();
    assert_eq!(args[0], "-c");
    assert!(
        !args[1].contains("${"),
        "Claude Code expands ${{...}} before sh sees it: {}",
        args[1]
    );
    for (key, home_rel) in [
        ("HUDDLE_DATA_HOME", ""),
        ("XDG_DATA_HOME", "huddle"),
        ("HOME", ".local/share/huddle"),
    ] {
        let home = TempHome::new();
        let mut data = home.root().to_path_buf();
        data.push(format!("data-{key}"));
        if !home_rel.is_empty() {
            data.push(home_rel);
        }
        fs::create_dir_all(data.join("bin")).unwrap();
        write_executable(&data.join("bin/huddle"), "#!/bin/sh\necho \"ran $*\"\n").unwrap();
        let mut command = Command::new("sh");
        command.args(&args).env_clear().env("PATH", "/usr/bin:/bin");
        match key {
            "HOME" => {
                // HOME/.local/share/huddle
                let base = data.parent().unwrap().parent().unwrap().parent().unwrap();
                command.env("HOME", base);
            }
            "XDG_DATA_HOME" => {
                command.env("XDG_DATA_HOME", data.parent().unwrap());
                command.env("HOME", "/nonexistent");
            }
            _ => {
                command.env("HUDDLE_DATA_HOME", &data);
                command
                    .env("XDG_DATA_HOME", "/nonexistent")
                    .env("HOME", "/nonexistent");
            }
        }
        let out = command.output().unwrap();
        assert_eq!(
            String::from_utf8_lossy(&out.stdout).trim(),
            "ran mcp",
            "{key}: {}",
            String::from_utf8_lossy(&out.stderr)
        );
    }
}

fn raw_post(port: u16, session: Option<&str>, body: &Value) -> (Option<String>, Value) {
    let body = body.to_string();
    let mut head = format!(
        "POST /mcp HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\
         Content-Type: application/json\r\nAccept: application/json, text/event-stream\r\n\
         Content-Length: {}\r\n",
        body.len()
    );
    if let Some(id) = session {
        head.push_str(&format!(
            "Mcp-Session-Id: {id}\r\nMcp-Protocol-Version: 2025-06-18\r\n"
        ));
    }
    let raw = raw_exchange(port, &format!("{head}\r\n{body}"));
    let id = raw.lines().find_map(|line| {
        let (name, value) = line.split_once(':')?;
        name.eq_ignore_ascii_case("mcp-session-id")
            .then(|| value.trim().to_string())
    });
    let payload = raw.split_once("\r\n\r\n").map_or("", |(_, rest)| rest);
    let message = payload
        .lines()
        .filter_map(|line| {
            let line = line.trim();
            serde_json::from_str::<Value>(line.strip_prefix("data:").unwrap_or(line).trim()).ok()
        })
        .find(|value| value.get("jsonrpc").is_some())
        .unwrap_or(Value::Null);
    (id, message)
}

fn initialize() -> Value {
    json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "m6-test", "version": "0"}
        }
    })
}

trait Transport {
    fn request(&mut self, method: &str, params: Value) -> Value;
}

struct Http {
    port: u16,
    session: String,
    next_id: u64,
}

impl Http {
    fn open(port: u16) -> Http {
        let (session, answer) = raw_post(port, None, &initialize());
        assert_eq!(answer["result"]["serverInfo"]["name"], "huddle", "{answer}");
        let session = session.expect("a session id");
        raw_post(
            port,
            Some(&session),
            &json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
        );
        Http {
            port,
            session,
            next_id: 2,
        }
    }
}

impl Transport for Http {
    fn request(&mut self, method: &str, params: Value) -> Value {
        let id = self.next_id;
        self.next_id += 1;
        let body = json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params});
        raw_post(self.port, Some(&self.session), &body).1
    }
}

struct Stdio2 {
    child: Child,
    reader: BufReader<std::process::ChildStdout>,
    next_id: u64,
}

impl Stdio2 {
    fn start(daemon: &Daemon) -> Stdio2 {
        let mut child = Command::new(env!("CARGO_BIN_EXE_huddle"))
            .arg("mcp")
            .env_clear()
            .envs(daemon.home.env())
            .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let reader = BufReader::new(child.stdout.take().unwrap());
        let mut stdio = Stdio2 {
            child,
            reader,
            next_id: 1,
        };
        stdio.request("initialize", initialize()["params"].clone());
        let line = json!({"jsonrpc": "2.0", "method": "notifications/initialized"});
        let pipe = stdio.child.stdin.as_mut().unwrap();
        writeln!(pipe, "{line}").unwrap();
        pipe.flush().unwrap();
        stdio
    }
}

impl Transport for Stdio2 {
    fn request(&mut self, method: &str, params: Value) -> Value {
        let id = self.next_id;
        self.next_id += 1;
        let body = json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params});
        let pipe = self.child.stdin.as_mut().unwrap();
        writeln!(pipe, "{body}").unwrap();
        pipe.flush().unwrap();
        let mut line = String::new();
        self.reader.read_line(&mut line).unwrap();
        serde_json::from_str(&line).unwrap()
    }
}

impl Drop for Stdio2 {
    fn drop(&mut self) {
        drop(self.child.stdin.take());
        let _ = self.child.wait();
    }
}

/// Drops the fields that differ by when or where a call ran: times, ages, durations, pids and the
/// binary's path (the router and the stdio server reach the same binary by different paths).
fn steady(value: &Value) -> Value {
    match value {
        Value::Object(map) => Value::Object(
            map.iter()
                .filter(|(key, _)| !is_run_specific(key))
                .map(|(key, value)| (key.clone(), steady(value)))
                .collect::<Map<_, _>>(),
        ),
        Value::Array(items) => Value::Array(items.iter().map(steady).collect()),
        other => other.clone(),
    }
}

fn is_run_specific(key: &str) -> bool {
    key == "binary_path"
        || key.ends_with("_ms")
        || key.ends_with("_at")
        || key.ends_with("_s")
        || key.contains("pid")
        || key.contains("uptime")
        || key.contains("age")
}

/// What a tool answered: the typed content, or the error text of a failed call.
fn outcome(answer: &Value) -> Value {
    let result = &answer["result"];
    if result["isError"] == true {
        json!({"error": result["content"]})
    } else {
        steady(&result["structuredContent"])
    }
}

fn calls() -> Vec<(&'static str, Value)> {
    vec![
        ("huddle_version", json!({})),
        (
            "huddle_run",
            json!({"harness": "no-such-harness", "task": "do nothing"}),
        ),
        ("huddle_status", json!({"all_repos": true})),
        ("huddle_result", json!({"id": "nosuchjob"})),
        ("huddle_cancel", json!({"id": "nosuchjob"})),
        (
            "huddle_usage",
            json!({"range": "24h", "group_by": "provider"}),
        ),
        ("huddle_errors", json!({"range": "24h"})),
        ("huddle_router_status", json!({})),
        ("huddle_router_log", json!({"kind": "all", "lines": 20})),
        ("huddle_providers", json!({})),
        ("huddle_quota", json!({})),
    ]
}

fn names(list: &Value) -> Vec<String> {
    let mut names: Vec<String> = list["result"]["tools"]
        .as_array()
        .expect("tools")
        .iter()
        .map(|tool| tool["name"].as_str().unwrap().to_string())
        .collect();
    names.sort();
    names
}

#[test]
fn m6_both_transports_answer_every_tool_the_same() {
    let daemon = Daemon::start(&[], &[]);
    let mut http = Http::open(daemon.port);
    let mut stdio = Stdio2::start(&daemon);

    let http_list = http.request("tools/list", json!({}));
    let stdio_list = stdio.request("tools/list", json!({}));
    assert_eq!(names(&http_list), names(&stdio_list));

    let mut called: Vec<String> = calls().iter().map(|(tool, _)| tool.to_string()).collect();
    called.sort();
    assert_eq!(called, names(&http_list), "every tool is called");

    for (tool, arguments) in calls() {
        let params = json!({"name": tool, "arguments": arguments});
        let over_http = outcome(&http.request("tools/call", params.clone()));
        let over_stdio = outcome(&stdio.request("tools/call", params));
        assert!(!over_http.is_null(), "{tool} answered nothing");
        if tool == "huddle_router_log" {
            // Every call adds a line to the log, so the two answers differ by the calls between them.
            assert_eq!(
                shape(&over_http),
                shape(&over_stdio),
                "{tool} has another shape"
            );
            continue;
        }
        assert_eq!(over_http, over_stdio, "{tool} answers differently");
    }
}

/// The field names of an answer, with arrays reduced to what they hold.
fn shape(value: &Value) -> Value {
    match value {
        Value::Object(map) => {
            Value::Object(map.iter().map(|(k, v)| (k.clone(), shape(v))).collect())
        }
        Value::Array(items) => Value::Array(items.first().map(shape).into_iter().collect()),
        Value::String(_) => json!("string"),
        other => other.clone(),
    }
}
