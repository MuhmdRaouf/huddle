mod e2e_support;

use e2e_support::{Daemon, raw_exchange};
use serde_json::{Value, json};

/// One raw HTTP exchange with `/mcp`.
struct Answer {
    status: u16,
    content_type: String,
    session: Option<String>,
    body: String,
}

impl Answer {
    /// The JSON message of the body (plain JSON or `data:` lines of an event stream).
    fn message(&self) -> Option<Value> {
        self.body.lines().find_map(|line| {
            let line = line.trim();
            serde_json::from_str::<Value>(line.strip_prefix("data:").unwrap_or(line).trim())
                .ok()
                .filter(|value| value.get("jsonrpc").is_some())
        })
    }

    /// Asserts a JSON-RPC error answer with this HTTP status and returns its code.
    fn error_code(&self, status: u16) -> i64 {
        assert_eq!(self.status, status, "{}", self.body);
        assert!(
            self.content_type.starts_with("application/json"),
            "content type {:?}: {}",
            self.content_type,
            self.body
        );
        let message = self.message().unwrap_or_else(|| panic!("{}", self.body));
        assert!(
            message["error"]["message"]
                .as_str()
                .is_some_and(|text| !text.is_empty()),
            "{message}"
        );
        message["error"]["code"].as_i64().expect("an error code")
    }
}

/// One POST to the daemon's `/mcp` with its token, as the plugin's `headersHelper` sends it.
fn send(daemon: &Daemon, extra: &[(&str, &str)], body: &[u8]) -> Answer {
    let port = daemon.port;
    let auth = daemon.mcp_authorization();
    let mut head = format!(
        "POST /mcp HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\
         Content-Type: application/json\r\nAccept: application/json, text/event-stream\r\n\
         Content-Length: {}\r\n",
        body.len()
    );
    for (name, value) in [("Authorization", auth.as_str())].iter().chain(extra) {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    head.push_str("\r\n");
    let mut request = head.into_bytes();
    request.extend_from_slice(body);
    let raw = raw_exchange_bytes(port, &request);
    let status = raw
        .split_whitespace()
        .nth(1)
        .and_then(|code| code.parse().ok())
        .unwrap_or(0);
    let header = |wanted: &str| {
        raw.lines().find_map(|line| {
            let (name, value) = line.split_once(':')?;
            name.eq_ignore_ascii_case(wanted)
                .then(|| value.trim().to_string())
        })
    };
    Answer {
        status,
        content_type: header("content-type").unwrap_or_default(),
        session: header("mcp-session-id"),
        body: raw
            .split_once("\r\n\r\n")
            .map_or("", |(_, rest)| rest)
            .to_string(),
    }
}

fn raw_exchange_bytes(port: u16, request: &[u8]) -> String {
    use std::io::{Read, Write};
    let mut stream = std::net::TcpStream::connect(("127.0.0.1", port)).expect("connect");
    stream
        .set_read_timeout(Some(std::time::Duration::from_secs(5)))
        .expect("timeout");
    // The router may answer and close before the whole body is written.
    let _ = stream.write_all(request);
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    String::from_utf8_lossy(&raw).into_owned()
}

fn call(daemon: &Daemon, extra: &[(&str, &str)], body: &Value) -> Answer {
    send(daemon, extra, body.to_string().as_bytes())
}

fn initialize() -> Value {
    json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "h4-test", "version": "0"}
        }
    })
}

/// A session id, past the handshake.
fn open(daemon: &Daemon) -> String {
    let first = call(daemon, &[], &initialize());
    assert_eq!(first.status, 200, "{}", first.body);
    let id = first.session.expect("a session id");
    let ack = call(
        daemon,
        &[
            ("Mcp-Session-Id", &id),
            ("Mcp-Protocol-Version", "2025-06-18"),
        ],
        &json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
    );
    assert_eq!(ack.status, 202, "{}", ack.body);
    id
}

fn in_session(id: &str) -> [(&'static str, &str); 2] {
    [
        ("Mcp-Session-Id", id),
        ("Mcp-Protocol-Version", "2025-06-18"),
    ]
}

#[test]
fn h4_oversized_body_is_a_json_rpc_error() {
    let daemon = Daemon::start(&[], &[]);
    let padding = "x".repeat(5 * 1024 * 1024);
    let body =
        json!({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"pad": padding}});
    let answer = call(&daemon, &[], &body);
    assert_eq!(answer.error_code(413), -32600);
}

#[test]
fn h4_truncated_json_is_a_json_rpc_error() {
    let daemon = Daemon::start(&[], &[]);
    let answer = send(&daemon, &[], br#"{"jsonrpc": "2.0", "id": 1, "me"#);
    assert_eq!(answer.error_code(400), -32700);
}

#[test]
fn h4_batch_is_refused_as_json_rpc() {
    let daemon = Daemon::start(&[], &[]);
    let batch = json!([{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}]);
    let answer = call(&daemon, &[], &batch);
    assert_eq!(answer.error_code(400), -32600);
}

#[test]
fn h4_missing_session_is_a_json_rpc_error() {
    let daemon = Daemon::start(&[], &[]);
    let list = json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"});
    let answer = call(&daemon, &[], &list);
    assert_eq!(answer.error_code(400), -32600);
}

#[test]
fn h4_unknown_session_is_a_json_rpc_error() {
    let daemon = Daemon::start(&[], &[]);
    let list = json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list"});
    let answer = call(&daemon, &[("Mcp-Session-Id", "no-such-session")], &list);
    assert_eq!(answer.error_code(404), -32001);
}

#[test]
fn h4_resources_list_is_method_not_found() {
    let daemon = Daemon::start(&[], &[]);
    let id = open(&daemon);
    let answer = call(
        &daemon,
        &in_session(&id),
        &json!({"jsonrpc": "2.0", "id": 2, "method": "resources/list"}),
    );
    let message = answer
        .message()
        .unwrap_or_else(|| panic!("{}", answer.body));
    assert_eq!(message["error"]["code"], -32601, "{message}");
    assert!(message.get("result").is_none(), "{message}");
}

#[test]
fn h4_router_status_lists_the_same_providers_as_health() {
    let daemon = Daemon::start(&["zai"], &[]);
    let health = raw_exchange(
        daemon.port,
        &format!(
            "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            daemon.port
        ),
    );
    let health: Value =
        serde_json::from_str(health.split_once("\r\n\r\n").expect("a body").1).expect("json");
    let id = open(&daemon);
    let answer = call(
        &daemon,
        &in_session(&id),
        &json!({"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                "params": {"name": "huddle_router_status", "arguments": {}}}),
    );
    let status = answer.message().expect("an answer")["result"]["structuredContent"].clone();
    assert_eq!(health["providers"], json!(["zai"]));
    assert_eq!(status["providers"], health["providers"], "{status}");
}
