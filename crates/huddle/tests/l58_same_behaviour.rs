//! L58, acceptance check 6 of 0.0.9: the behaviour a user sees is unchanged except three things.
//! Each test asserts one exception and its neighbour that must not change:
//!
//! - the tools need the router up (and `huddle mcp` on stdio, which the plugin no longer starts,
//!   still works without it);
//! - a body over 1 MiB that meets a dead pooled upstream socket is not retried (and a smaller one
//!   still is);
//! - `huddle setup` is about 3 s faster: measured in `L58-run.md`, not asserted here. Its ledger,
//!   settings patch and byte-for-byte restore are asserted by `uninstall_word.rs`
//!   (`t2_install_leaves_toml_ledgers_and_uninstall_restores_the_bytes`,
//!   `uninstall_restores_settings_byte_for_byte_and_stops_the_router`).

mod e2e_support;

use std::fs::{self, File};
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant, SystemTime};

use e2e_support::try_request;
use huddle_pki::ca::{ca_cert_path, load_or_create};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use serde_json::{Value, json};

/// A `huddle router serve` of `home`, on the port the home names, stopped on drop.
struct Router {
    child: Child,
}

impl Router {
    fn start(home: &TempHome, extra: &[(&str, String)]) -> Router {
        let mut command = home.command(env!("CARGO_BIN_EXE_huddle"));
        command
            .args(["router", "serve"])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        for (name, value) in extra {
            command.env(name, value);
        }
        let mut child = command.spawn().expect("spawn huddle router serve");
        let port = home.router_port();
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            if try_request(port, "GET", "/huddle-router/health", &[], b"")
                .is_some_and(|reply| reply.status == 200)
            {
                return Router { child };
            }
            assert!(
                child.try_wait().expect("try wait").is_none(),
                "the router exited before answering health"
            );
            assert!(Instant::now() < deadline, "the router never answered");
            std::thread::sleep(Duration::from_millis(50));
        }
    }
}

impl Drop for Router {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// `huddle mcp headers` for the home: the `Authorization` value the plugin's helper prints.
fn authorization(home: &TempHome) -> String {
    let out = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["mcp", "headers"])
        .stdin(Stdio::null())
        .output()
        .expect("run huddle mcp headers");
    let headers: Value =
        serde_json::from_slice(&out.stdout).expect("huddle mcp headers prints JSON");
    headers["Authorization"]
        .as_str()
        .expect("an Authorization header")
        .to_string()
}

/// `initialize` over the router's `/mcp`: the HTTP status, or none when nothing answers.
fn initialize_over_http(port: u16, auth: &str) -> Option<u16> {
    let body = json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "l58-test", "version": "0"}
        }
    })
    .to_string();
    try_request(
        port,
        "POST",
        "/mcp",
        &[
            ("Content-Type", "application/json"),
            ("Accept", "application/json, text/event-stream"),
            ("Authorization", auth),
        ],
        body.as_bytes(),
    )
    .map(|reply| reply.status)
}

/// What `huddle mcp` on stdio answers to `huddle_quota` with this home's router down.
fn stdio_quota_text(home: &TempHome) -> String {
    let mut child = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .arg("mcp")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("start huddle mcp");
    let mut stdin = child.stdin.take().expect("stdin");
    let mut reader = BufReader::new(child.stdout.take().expect("stdout"));
    let mut send = |message: Value| {
        writeln!(stdin, "{message}").expect("write");
        stdin.flush().expect("flush");
    };
    send(json!({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2025-06-18",
            "capabilities": {},
            "clientInfo": {"name": "l58-test", "version": "0"}
        }
    }));
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .expect("read the initialize answer");
    send(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
    send(json!({
        "jsonrpc": "2.0", "id": 2, "method": "tools/call",
        "params": {"name": "huddle_quota", "arguments": {}}
    }));
    line.clear();
    reader.read_line(&mut line).expect("read the quota answer");
    let answer: Value = serde_json::from_str(&line).expect("a JSON-RPC line");
    drop(stdin);
    let _ = child.wait();
    answer.to_string()
}

#[test]
fn l58_the_tools_need_the_router_up_and_come_back_with_it() {
    let home = TempHome::new();
    let port = home.router_port();

    // Down: nothing answers `/mcp`, while the stdio server (which the plugin no longer starts)
    // still answers and reports `router_up` false.
    assert_eq!(initialize_over_http(port, "Bearer none"), None);
    let quota = stdio_quota_text(&home);
    assert!(quota.contains(r#"\"router_up\":false"#), "{quota}");

    // Up: the same call answers, with the token the plugin's helper prints.
    let router = Router::start(&home, &[]);
    let auth = authorization(&home);
    assert_eq!(initialize_over_http(port, &auth), Some(200));

    // Down again: the tools stop, they are not cached anywhere.
    drop(router);
    let deadline = Instant::now() + Duration::from_secs(10);
    while initialize_over_http(port, &auth).is_some() {
        assert!(Instant::now() < deadline, "the router kept answering");
        std::thread::sleep(Duration::from_millis(50));
    }
}

const BODY_JSON_HEAD: &[u8] =
    br#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":""#;
const BODY_JSON_TAIL: &[u8] = br#""}]}"#;
const KIB: usize = 1024;

/// A request body of `len` bytes whose model leaves for Anthropic.
fn write_body(dir: &Path, name: &str, len: usize) -> PathBuf {
    let path = dir.join(name);
    let mut file = File::create(&path).expect("create the body file");
    file.write_all(BODY_JSON_HEAD).expect("write head");
    let filler = vec![b'x'; len - BODY_JSON_HEAD.len() - BODY_JSON_TAIL.len()];
    file.write_all(&filler).expect("write filler");
    file.write_all(BODY_JSON_TAIL).expect("write tail");
    path
}

/// `curl` posting `file` through the router's CONNECT: the status code text and stderr.
fn post_through_proxy(port: u16, ca: &Path, file: &Path) -> (String, String) {
    let out = Command::new("/usr/bin/curl")
        .args([
            "-sS",
            "--max-time",
            "60",
            "--proxy",
            &format!("http://127.0.0.1:{port}"),
            "--cacert",
            ca.to_str().expect("utf8 path"),
            "-H",
            "content-type: application/json",
            "-H",
            "x-api-key: client-key",
            "-H",
            "Expect:",
            "--data-binary",
            &format!("@{}", file.to_str().expect("utf8 path")),
            "-o",
            "/dev/null",
            "-w",
            "%{http_code}",
            "https://api.anthropic.com/v1/messages",
        ])
        .env_clear()
        .stdin(Stdio::null())
        .output()
        .expect("run curl");
    (
        String::from_utf8_lossy(&out.stdout).into_owned(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

struct Fixture {
    home: TempHome,
    upstream: FakeUpstream,
    _router: Router,
    ca: PathBuf,
}

/// A router in front of an upstream that answers the first request of the first connection (the
/// warm-up that leaves a pooled socket), resets that socket in the middle of the second request
/// after `reset_after` body bytes, and answers `200` to anything else.
fn fixture(reset_after: usize) -> Fixture {
    let upstream = tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |conn, req| match (conn, req) {
            (1, 1) => Step::answer(200, "warm"),
            (1, 2) => Step::ReadThenReset(reset_after),
            _ => Step::answer(200, "retried"),
        }));
    let home = TempHome::new();
    let ca_dir = home.root().join(".local/share/huddle/ca");
    load_or_create(&ca_dir, SystemTime::now()).expect("create CA");
    let router = Router::start(&home, &[("HUDDLE_ROUTER_ANTHROPIC_URL", upstream.url())]);
    let ca = ca_cert_path(&ca_dir);
    let small = write_body(home.root(), "warm.json", KIB);
    let (status, stderr) = post_through_proxy(home.router_port(), &ca, &small);
    assert_eq!(status, "200", "warm-up: {stderr}");
    assert_eq!(
        upstream.connections(),
        1,
        "the warm-up left one pooled socket"
    );
    Fixture {
        home,
        upstream,
        _router: router,
        ca,
    }
}

#[test]
fn l58_a_body_over_one_mib_is_not_retried_after_a_dead_pooled_socket() {
    let f = fixture(2 * 1024 * KIB);
    let body = write_body(f.home.root(), "big.json", 3 * 1024 * KIB);

    let (status, _) = post_through_proxy(f.home.router_port(), &f.ca, &body);

    assert_ne!(status, "200", "the client sees the cut");
    assert_eq!(
        f.upstream.connections(),
        1,
        "no second attempt reached the upstream"
    );
    let cut = f.upstream.requests().pop().expect("the large request");
    assert!(!cut.complete, "the upstream never got the whole body");
}

#[test]
fn l58_a_body_under_one_mib_is_still_retried_after_a_dead_pooled_socket() {
    let f = fixture(64 * KIB);
    let body = write_body(f.home.root(), "mid.json", 900 * KIB);

    let (status, stderr) = post_through_proxy(f.home.router_port(), &f.ca, &body);

    assert_eq!(status, "200", "the retry answers: {stderr}");
    assert_eq!(f.upstream.connections(), 2, "one fresh connection");
    let full: Vec<_> = f
        .upstream
        .requests()
        .into_iter()
        .filter(|request| request.complete && request.body.len() == 900 * KIB)
        .collect();
    assert_eq!(full.len(), 1, "one full copy of the body");
    assert_eq!(full[0].connection, 2);
    let _ = fs::remove_file(body);
}
