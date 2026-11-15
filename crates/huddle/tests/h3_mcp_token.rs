//! The token that guards `/mcp`: the router refuses a request without it, the plugin's
//! `headersHelper` gets it from `huddle mcp headers`, and stdio needs none.

mod e2e_support;

use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use e2e_support::{Daemon, raw_exchange};
use serde_json::{Value, json};

fn initialize() -> String {
    json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "h3-test", "version": "0"}
        }
    })
    .to_string()
}

/// One POST to `/mcp` with `extra` headers: the status line's code and the whole answer.
fn post(port: u16, extra: &[(&str, &str)]) -> (u16, String) {
    let body = initialize();
    let mut head = format!(
        "POST /mcp HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\
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
    (status, raw)
}

fn body_json(raw: &str) -> Value {
    let payload = raw.split_once("\r\n\r\n").map_or("", |(_, rest)| rest);
    serde_json::from_str(payload.trim()).unwrap_or(Value::Null)
}

fn token_path(daemon: &Daemon) -> PathBuf {
    daemon.home.root().join(".local/state/huddle/mcp-token")
}

/// `huddle mcp headers` for the daemon's home: exit code, stdout, stderr.
fn helper(daemon: &Daemon) -> (Option<i32>, String, String) {
    let out = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["mcp", "headers"])
        .env_clear()
        .envs(daemon.home.env())
        .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
        .stdin(Stdio::null())
        .output()
        .unwrap();
    (
        out.status.code(),
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

fn authorization(daemon: &Daemon) -> String {
    let (code, stdout, stderr) = helper(daemon);
    assert_eq!(code, Some(0), "{stderr}");
    let headers: Value = serde_json::from_str(stdout.trim()).expect("a JSON object");
    headers["Authorization"]
        .as_str()
        .expect("an Authorization header")
        .to_string()
}

fn assert_refused(status: u16, raw: &str) {
    assert_eq!(status, 401, "{raw}");
    let body = body_json(raw);
    assert_eq!(body["jsonrpc"], "2.0", "{raw}");
    assert!(body["id"].is_null(), "{raw}");
    let message = body["error"]["message"].as_str().unwrap_or_default();
    assert!(message.contains("/huddle:update"), "{raw}");
    assert!(
        !raw.to_ascii_lowercase().contains("mcp-session-id"),
        "a refused request opened a session: {raw}"
    );
}

#[test]
fn h3_mcp_without_a_token_is_401() {
    let daemon = Daemon::start(&[], &[]);
    let (status, raw) = post(daemon.port, &[]);
    assert_refused(status, &raw);
    // A GET without the token is refused for the token, not for its method.
    let get = raw_exchange(
        daemon.port,
        &format!(
            "GET /mcp HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            daemon.port
        ),
    );
    assert!(get.starts_with("HTTP/1.1 401"), "{get}");
}

/// The 401 names this server `huddle`, the name the plugin's `.mcp.json` reconnects.
#[test]
fn l40_the_401_text_names_the_huddle_server() {
    let daemon = Daemon::start(&[], &[]);
    let (_, raw) = post(daemon.port, &[]);
    let message = body_json(&raw)["error"]["message"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    assert!(message.contains("reconnect huddle in /mcp"), "{raw}");
    assert!(!message.contains("huddle-http"), "{raw}");
}

#[test]
fn h3_mcp_with_the_token_answers() {
    let daemon = Daemon::start(&[], &[]);
    let auth = authorization(&daemon);
    let (status, raw) = post(daemon.port, &[("Authorization", auth.as_str())]);
    assert_eq!(status, 200, "{raw}");
    assert!(raw.contains("\"serverInfo\""), "{raw}");
    // Host and Origin run first: the token does not let a foreign Origin in.
    let (status, raw) = post(
        daemon.port,
        &[
            ("Authorization", auth.as_str()),
            ("Origin", "http://evil.example"),
        ],
    );
    assert_eq!(status, 403, "{raw}");
}

#[test]
fn h3_wrong_token_is_401_and_says_nothing() {
    let daemon = Daemon::start(&[], &[]);
    let auth = authorization(&daemon);
    let (_, missing) = post(daemon.port, &[]);
    let missing_body = body_json(&missing);
    let mut flipped = auth.clone().into_bytes();
    let last = flipped.len() - 1;
    flipped[last] = if flipped[last] == b'0' { b'1' } else { b'0' };
    let flipped = String::from_utf8(flipped).unwrap();
    let token = auth.trim_start_matches("Bearer ");
    for wrong in [
        flipped.as_str(),
        token,
        "Bearer ",
        "Bearer short",
        &format!("Basic {token}"),
        &format!("{auth}0"),
    ] {
        let (status, raw) = post(daemon.port, &[("Authorization", wrong)]);
        assert_refused(status, &raw);
        // The same answer whatever was sent: nothing tells a caller how close it came.
        assert_eq!(body_json(&raw), missing_body, "{wrong:?}");
        assert!(!raw.contains(token), "the answer carries the token");
    }
    // The OAuth probe a client sends after a 401 is answered here, never forwarded.
    for path in [
        "/.well-known/oauth-protected-resource/mcp",
        "/.well-known/oauth-authorization-server",
        "/.well-known/openid-configuration",
    ] {
        let probe = raw_exchange(
            daemon.port,
            &format!(
                "GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
                daemon.port
            ),
        );
        assert!(probe.starts_with("HTTP/1.1 404"), "{probe}");
        assert!(probe.contains("huddle-router"), "{probe}");
    }
    // The dashboard, health and history routes are unchanged.
    let health = raw_exchange(
        daemon.port,
        &format!(
            "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            daemon.port
        ),
    );
    assert!(health.starts_with("HTTP/1.1 200"), "{health}");
}

#[test]
fn h3_token_file_is_0600_and_made_once() {
    let daemon = Daemon::start(&[], &[]);
    let path = token_path(&daemon);
    let mode = fs::metadata(&path)
        .expect("the router made the token")
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o600, "{path:?}");
    let first = fs::read_to_string(&path).unwrap();
    assert_eq!(first.trim().len(), 64, "32 bytes in hex");
    assert!(first.trim().bytes().all(|b| b.is_ascii_hexdigit()));

    // A second router on the same home keeps the token the first one made.
    let mut again = Command::new(env!("CARGO_BIN_EXE_huddle"));
    again
        .args(["router", "serve"])
        .env_clear()
        .envs(daemon.home.env())
        .env(
            "HUDDLE_ROUTER_PORT",
            huddle_testkit::ports::free_port().to_string(),
        )
        .env("HUDDLE_RUNTIME_DIR", daemon.home.root().join("r2"))
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let mut child = again.spawn().unwrap();
    std::thread::sleep(std::time::Duration::from_millis(800));
    let _ = child.kill();
    let _ = child.wait();
    assert_eq!(fs::read_to_string(&path).unwrap(), first);
}

#[test]
fn h3_plugin_config_gets_its_header_from_the_helper() {
    let daemon = Daemon::start(&[], &[]);
    let mcp: Value = serde_json::from_str(
        &fs::read_to_string(
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../plugin/.mcp.json"),
        )
        .unwrap(),
    )
    .unwrap();
    let helper_line = mcp["mcpServers"]["huddle"]["headersHelper"]
        .as_str()
        .expect("huddle has a headersHelper");
    assert!(
        !helper_line.contains("${"),
        "Claude Code expands ${{...}} before sh sees it: {helper_line}"
    );

    // The installed binary lives under the data home; the helper finds it the way the session
    // hook does.
    let data = daemon.home.root().join(".local/share/huddle");
    fs::create_dir_all(data.join("bin")).unwrap();
    std::os::unix::fs::symlink(env!("CARGO_BIN_EXE_huddle"), data.join("bin/huddle")).unwrap();
    let out = Command::new("sh")
        .args(["-c", helper_line])
        .env_clear()
        .envs(daemon.home.env())
        .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
        .env("PATH", "/usr/bin:/bin")
        .stdin(Stdio::piped())
        .output()
        .unwrap();
    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let headers: Value = serde_json::from_slice(&out.stdout).expect("a JSON object");
    let object = headers.as_object().expect("an object");
    assert!(object.values().all(Value::is_string), "{headers}");
    assert_eq!(headers["Authorization"], authorization(&daemon));

    let extra: Vec<(&str, &str)> = object
        .iter()
        .map(|(name, value)| (name.as_str(), value.as_str().unwrap()))
        .collect();
    let (status, raw) = post(daemon.port, &extra);
    assert_eq!(status, 200, "{raw}");
}

/// A 0.0.7 binary under a 0.0.8 plugin takes `mcp headers` as `mcp`, finds stdin closed and
/// fails; the helper then says to run `/huddle:update`.
#[test]
fn h3_an_old_binary_under_the_helper_says_to_update() {
    let home = huddle_testkit::temp_home::TempHome::new();
    let mcp: Value = serde_json::from_str(
        &fs::read_to_string(
            PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../plugin/.mcp.json"),
        )
        .unwrap(),
    )
    .unwrap();
    let helper_line = mcp["mcpServers"]["huddle"]["headersHelper"]
        .as_str()
        .expect("huddle has a headersHelper");
    let bin = home.root().join(".local/share/huddle/bin");
    fs::create_dir_all(&bin).unwrap();
    let old = bin.join("huddle");
    fs::write(
        &old,
        "#!/bin/sh\necho 'huddle mcp: connection closed: initialize request' >&2\nexit 1\n",
    )
    .unwrap();
    fs::set_permissions(&old, fs::Permissions::from_mode(0o755)).unwrap();
    let out = Command::new("sh")
        .args(["-c", helper_line])
        .env_clear()
        .envs(home.env())
        .env("PATH", "/usr/bin:/bin")
        .stdin(Stdio::piped())
        .output()
        .unwrap();
    assert_ne!(out.status.code(), Some(0));
    assert!(out.stdout.is_empty());
    let said = String::from_utf8_lossy(&out.stderr);
    assert!(said.contains("/huddle:update"), "{said}");
}

/// Every file under `root` but `skip`, read as bytes.
fn files_under(root: &Path, skip: &Path, found: &mut Vec<(PathBuf, Vec<u8>)>) {
    for entry in fs::read_dir(root).into_iter().flatten().flatten() {
        let path = entry.path();
        let Ok(kind) = entry.file_type() else {
            continue;
        };
        if kind.is_dir() {
            files_under(&path, skip, found);
        } else if kind.is_file()
            && path != skip
            && let Ok(bytes) = fs::read(&path)
        {
            found.push((path, bytes));
        }
    }
}

#[test]
fn h3_token_is_in_no_log_or_output() {
    let daemon = Daemon::start(&[], &[]);
    let auth = authorization(&daemon);
    let token = auth.trim_start_matches("Bearer ").to_string();
    let _ = post(daemon.port, &[("Authorization", auth.as_str())]);
    let _ = post(daemon.port, &[("Authorization", "Bearer 00")]);
    let _ = post(daemon.port, &[]);

    let status = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "status"])
        .env_clear()
        .envs(daemon.home.env())
        .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let said = format!(
        "{}{}",
        String::from_utf8_lossy(&status.stdout),
        String::from_utf8_lossy(&status.stderr)
    );
    assert!(said.contains("mcp: token ok"), "{said}");
    assert!(!said.contains(&token), "router status prints the token");

    let mut found = Vec::new();
    files_under(daemon.home.root(), &token_path(&daemon), &mut found);
    assert!(!found.is_empty());
    for (path, bytes) in found {
        assert!(
            !String::from_utf8_lossy(&bytes).contains(&token),
            "{path:?} holds the token"
        );
    }
}

#[test]
fn h3_router_status_says_what_is_wrong_with_the_token() {
    let daemon = Daemon::start(&[], &[]);
    let path = token_path(&daemon);
    fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();
    let status = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "status"])
        .env_clear()
        .envs(daemon.home.env())
        .env("HUDDLE_ROUTER_PORT", daemon.port.to_string())
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let said = String::from_utf8_lossy(&status.stdout);
    assert!(said.contains("mcp: token "), "{said}");
    assert!(!said.contains("mcp: token ok"), "{said}");
    assert!(said.contains("0644"), "{said}");

    fs::remove_file(&path).unwrap();
    let (code, stdout, stderr) = helper(&daemon);
    assert_eq!(code, Some(1));
    assert!(stdout.is_empty(), "{stdout}");
    assert!(stderr.contains("/huddle:setup"), "{stderr}");
}

#[test]
fn h3_stdio_needs_no_token() {
    let daemon = Daemon::start(&[], &[]);
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
    let mut stdin = child.stdin.take().unwrap();
    let mut reader = BufReader::new(child.stdout.take().unwrap());
    writeln!(stdin, "{}", initialize()).unwrap();
    let mut line = String::new();
    reader.read_line(&mut line).unwrap();
    let answer: Value = serde_json::from_str(&line).unwrap();
    assert_eq!(answer["result"]["serverInfo"]["name"], "huddle", "{line}");
    drop(stdin);
    let _ = child.wait();
}
