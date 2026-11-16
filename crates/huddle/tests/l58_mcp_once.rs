//! L58, acceptance check 4 of 0.0.9: a Claude session starts no `huddle mcp` process and lists the
//! eleven huddle tools once. The plugin declares one HTTP server; the router answers it.

mod e2e_support;

use std::collections::BTreeSet;
use std::fs;
use std::path::PathBuf;
use std::process::{Command, Stdio};

use e2e_support::{Daemon, raw_exchange};
use serde_json::{Value, json};

const TOOLS: usize = 11;

fn plugin_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../plugin")
}

#[test]
fn l58_the_plugin_declares_one_http_mcp_server() {
    let text = fs::read_to_string(plugin_dir().join(".mcp.json")).expect("plugin/.mcp.json");
    let doc: Value = serde_json::from_str(&text).expect("json");
    let servers = doc["mcpServers"].as_object().expect("mcpServers");
    assert_eq!(servers.len(), 1, "one server: {servers:?}");

    let (name, server) = servers.iter().next().expect("one server");
    assert_eq!(name, "huddle");
    assert_eq!(server["type"], "http");
    assert!(
        server["url"]
            .as_str()
            .is_some_and(|url| { url.starts_with("http://127.0.0.1:") && url.ends_with("/mcp") }),
        "the router's /mcp: {server}"
    );
    // An entry that starts a process names a command; none does, and no text in the file runs
    // `huddle mcp` as a server.
    assert!(server.get("command").is_none(), "{server}");
    assert!(server.get("args").is_none(), "{server}");
    let headers_helper = server["headersHelper"].as_str().unwrap_or_default();
    assert!(
        headers_helper.contains("mcp headers"),
        "the helper only prints headers: {headers_helper}"
    );
    assert!(
        !text.contains("\"command\""),
        "no entry starts a process: {text}"
    );
}

/// One POST to `/mcp`: the session id the answer carries and its JSON-RPC message.
fn post(port: u16, auth: &str, session: Option<&str>, body: &Value) -> (Option<String>, Value) {
    let body = body.to_string();
    let mut head = format!(
        "POST /mcp HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\
         Content-Type: application/json\r\nAccept: application/json, text/event-stream\r\n\
         Authorization: {auth}\r\nContent-Length: {}\r\n",
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

/// What a Claude session does on connect: `initialize`, `notifications/initialized`, then
/// `tools/list`. Answers the tool names in the order the server listed them.
fn list_tools(daemon: &Daemon) -> Vec<String> {
    let port = daemon.port;
    let auth = daemon.mcp_authorization();
    let initialize = json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "l58-test", "version": "0"}
        }
    });
    let (session, answer) = post(port, &auth, None, &initialize);
    assert_eq!(answer["result"]["serverInfo"]["name"], "huddle", "{answer}");
    let session = session.expect("a session id");
    post(
        port,
        &auth,
        Some(&session),
        &json!({"jsonrpc": "2.0", "method": "notifications/initialized"}),
    );
    let (_, listed) = post(
        port,
        &auth,
        Some(&session),
        &json!({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}),
    );
    listed["result"]["tools"]
        .as_array()
        .unwrap_or_else(|| panic!("a tools list: {listed}"))
        .iter()
        .map(|tool| tool["name"].as_str().expect("a tool name").to_string())
        .collect()
}

#[test]
fn l58_a_session_lists_eleven_tools_once() {
    let daemon = Daemon::start(&[], &[]);
    let names = list_tools(&daemon);

    assert_eq!(names.len(), TOOLS, "{names:?}");
    let distinct: BTreeSet<_> = names.iter().collect();
    assert_eq!(distinct.len(), TOOLS, "a name is listed twice: {names:?}");
    for name in &names {
        assert!(name.starts_with("huddle_"), "{name}");
    }
}

/// The pids of the processes that run `huddle mcp` and name `home` in their command or
/// environment. `ps axeww` prints the environment after the command, so the home ties the
/// process to this test's temp home.
fn mcp_processes_of(home: &str) -> Vec<String> {
    let listing = Command::new("ps")
        .args(["axeww", "-o", "pid=,command="])
        .output()
        .expect("ps");
    assert!(listing.status.success(), "ps failed");
    String::from_utf8_lossy(&listing.stdout)
        .lines()
        .filter(|line| line.contains(home) && line.contains("huddle mcp"))
        .map(|line| line.chars().take(120).collect())
        .collect()
}

#[test]
fn l58_no_mcp_process_runs_beside_the_router() {
    let daemon = Daemon::start(&[], &[]);
    let home = daemon.home.root().display().to_string();
    assert_eq!(list_tools(&daemon).len(), TOOLS);
    assert_eq!(
        mcp_processes_of(&home),
        Vec::<String>::new(),
        "a session over HTTP starts no `huddle mcp`"
    );

    // The scan sees a `huddle mcp` process of this home when one runs, so an empty answer above
    // means none ran.
    let mut stdio = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .arg("mcp")
        .env_clear()
        .envs(daemon.home.env())
        .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("start huddle mcp");
    let mut seen = Vec::new();
    for _ in 0..50 {
        seen = mcp_processes_of(&home);
        if !seen.is_empty() {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
    drop(stdio.stdin.take());
    let _ = stdio.wait();
    assert_eq!(seen.len(), 1, "the scan finds a stdio server: {seen:?}");
}
