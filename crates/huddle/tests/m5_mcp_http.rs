mod e2e_support;

use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Command, Stdio};

use e2e_support::{Daemon, raw_exchange};
use serde_json::{Value, json};

const VERSION_FILE: &str = include_str!("../../../VERSION");

/// One raw HTTP exchange with `/mcp`: the status, the `Mcp-Session-Id` header and every JSON
/// message the body carried (plain JSON or `data:` lines of an event stream).
struct Answer {
    status: u16,
    session: Option<String>,
    messages: Vec<Value>,
    raw: String,
}

fn post(port: u16, host: &str, extra: &[(&str, &str)], body: &Value) -> Answer {
    send(port, "POST", host, extra, &body.to_string())
}

fn send(port: u16, method: &str, host: &str, extra: &[(&str, &str)], body: &str) -> Answer {
    let mut head = format!(
        "{method} /mcp HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\
         Content-Type: application/json\r\nAccept: application/json, text/event-stream\r\n\
         Content-Length: {}\r\n",
        body.len()
    );
    for (name, value) in extra {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    let raw = raw_exchange(port, &format!("{head}\r\n{body}"));
    let status = raw
        .split_whitespace()
        .nth(1)
        .and_then(|code| code.parse().ok())
        .unwrap_or(0);
    let session = raw.lines().find_map(|line| {
        let (name, value) = line.split_once(':')?;
        name.eq_ignore_ascii_case("mcp-session-id")
            .then(|| value.trim().to_string())
    });
    let payload = raw.split_once("\r\n\r\n").map_or("", |(_, rest)| rest);
    let messages = payload
        .lines()
        .filter_map(|line| {
            let line = line.trim();
            serde_json::from_str::<Value>(line.strip_prefix("data:").unwrap_or(line).trim()).ok()
        })
        .filter(|value| value.get("jsonrpc").is_some())
        .collect();
    Answer {
        status,
        session,
        messages,
        raw,
    }
}

fn initialize() -> Value {
    json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "m5-test", "version": "0"}
        }
    })
}

/// A session over HTTP, past the handshake.
struct HttpSession {
    port: u16,
    id: String,
}

impl HttpSession {
    fn open(port: u16) -> HttpSession {
        let host = format!("127.0.0.1:{port}");
        let first = post(port, &host, &[], &initialize());
        assert_eq!(first.status, 200, "{}", first.raw);
        assert_eq!(first.messages[0]["result"]["serverInfo"]["name"], "huddle");
        let id = first.session.expect("a session id");
        let session = HttpSession { port, id };
        let ack = session.post(&json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
        assert_eq!(ack.status, 202, "{}", ack.raw);
        session
    }

    fn post(&self, body: &Value) -> Answer {
        post(
            self.port,
            &format!("127.0.0.1:{}", self.port),
            &[
                ("Mcp-Session-Id", self.id.as_str()),
                ("Mcp-Protocol-Version", "2025-06-18"),
            ],
            body,
        )
    }

    fn call(&self, body: &Value) -> Value {
        let answer = self.post(body);
        assert_eq!(answer.status, 200, "{}", answer.raw);
        answer.messages.into_iter().next().expect("one message")
    }
}

/// The same tools over `huddle mcp` stdio, pointed at the daemon's port.
struct Stdio2 {
    child: Child,
    reader: BufReader<std::process::ChildStdout>,
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
        Stdio2 { child, reader }
    }

    fn call(&mut self, message: Value) -> Value {
        let stdin = self.child.stdin.as_mut().unwrap();
        writeln!(stdin, "{message}").unwrap();
        stdin.flush().unwrap();
        let mut line = String::new();
        self.reader.read_line(&mut line).unwrap();
        serde_json::from_str(&line).unwrap()
    }

    fn notify(&mut self, message: Value) {
        let stdin = self.child.stdin.as_mut().unwrap();
        writeln!(stdin, "{message}").unwrap();
        stdin.flush().unwrap();
    }
}

impl Drop for Stdio2 {
    fn drop(&mut self) {
        drop(self.child.stdin.take());
        let _ = self.child.wait();
    }
}

fn tool_names(list: &Value) -> Vec<String> {
    let mut names: Vec<String> = list["result"]["tools"]
        .as_array()
        .unwrap()
        .iter()
        .map(|tool| tool["name"].as_str().unwrap().to_string())
        .collect();
    names.sort();
    names
}

fn version_call() -> Value {
    json!({
        "jsonrpc": "2.0", "id": 3, "method": "tools/call",
        "params": {"name": "huddle_version", "arguments": {}}
    })
}

#[test]
fn m5_http_lists_tools_and_answers_like_stdio() {
    let daemon = Daemon::start(&[], &[]);
    let session = HttpSession::open(daemon.port);
    let http_tools = session.call(&json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}));
    let http_version = session.call(&version_call())["result"]["structuredContent"].clone();

    let mut stdio = Stdio2::start(&daemon);
    stdio.call(initialize());
    stdio.notify(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
    let stdio_tools = stdio.call(json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}));
    let stdio_version = stdio.call(version_call())["result"]["structuredContent"].clone();

    assert_eq!(tool_names(&http_tools), tool_names(&stdio_tools));
    assert!(tool_names(&http_tools).contains(&"huddle_version".to_string()));
    assert_eq!(http_version["version"], VERSION_FILE.trim());
    assert_eq!(http_version["version"], stdio_version["version"]);
    assert_eq!(http_version["router_up"], true);
    assert_eq!(stdio_version["router_up"], true);
}

#[test]
fn m5_each_session_has_its_own_id() {
    let daemon = Daemon::start(&[], &[]);
    let first = HttpSession::open(daemon.port);
    let second = HttpSession::open(daemon.port);
    assert_ne!(first.id, second.id);
    let answer = post(
        daemon.port,
        &format!("127.0.0.1:{}", daemon.port),
        &[("Mcp-Session-Id", "no-such-session")],
        &json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"}),
    );
    assert_eq!(answer.status, 404, "{}", answer.raw);
}

#[test]
fn m5_a_foreign_host_or_origin_is_refused() {
    let daemon = Daemon::start(&[], &[]);
    let port = daemon.port;
    for host in [
        "evil.example".to_string(),
        format!("evil.example:{port}"),
        "127.0.0.1".to_string(),
        format!("127.0.0.1:{}", port.wrapping_add(1)),
        format!("[::1]:{port}"),
    ] {
        let answer = post(port, &host, &[], &initialize());
        assert!(
            answer.status == 403 || answer.status == 421,
            "{host}: {} {}",
            answer.status,
            answer.raw
        );
        assert!(answer.messages.is_empty(), "{host} got an answer");
    }
    for origin in ["http://evil.example", "https://127.0.0.1", "null"] {
        let answer = post(
            port,
            &format!("127.0.0.1:{port}"),
            &[("Origin", origin)],
            &initialize(),
        );
        assert_eq!(answer.status, 403, "{origin}: {}", answer.raw);
        assert!(answer.messages.is_empty(), "{origin} got an answer");
    }
    for origin in [
        format!("http://127.0.0.1:{port}"),
        format!("http://localhost:{port}"),
    ] {
        let answer = post(
            port,
            &format!("localhost:{port}"),
            &[("Origin", origin.as_str())],
            &initialize(),
        );
        assert_eq!(answer.status, 200, "{origin}: {}", answer.raw);
    }
}

#[test]
fn m5_the_dashboard_host_never_serves_mcp() {
    let daemon = Daemon::start(&[], &[]);
    let host = "hub.raouf.me";
    let added = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["add-dashboard-host", host])
        .env_clear()
        .envs(daemon.home.env())
        .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
        .stdin(Stdio::null())
        .output()
        .unwrap();
    assert_eq!(added.status.code(), Some(0));
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    loop {
        let page = raw_exchange(
            daemon.port,
            &format!("GET / HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n"),
        );
        if page.starts_with("HTTP/1.1 200") {
            break;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "the host never reached the page"
        );
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
    for method in ["POST", "GET", "DELETE"] {
        let answer = send(daemon.port, method, host, &[], &initialize().to_string());
        assert_eq!(answer.status, 421, "{method}: {}", answer.raw);
        assert!(answer.messages.is_empty());
    }
}
