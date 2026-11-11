mod e2e_support;

use e2e_support::{Daemon, raw_exchange, request};
use huddle_pki::ca::{ca_cert_path, load_or_create};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use serde_json::Value;
use std::io::{BufRead, BufReader, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant, SystemTime};

const USAGE_JSON: &str = r#"{"type":"message","usage":{"input_tokens":120,"output_tokens":34}}"#;
const SESSION: &str = "cse_01JqXrugEVi7YXTu";

fn json_header() -> Vec<(String, String)> {
    vec![("content-type".to_string(), "application/json".to_string())]
}

fn start_upstream<F>(plan: F) -> FakeUpstream
where
    F: Fn(usize, usize) -> Step + Send + Sync + 'static,
{
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(plan))
}

fn usage_upstream() -> FakeUpstream {
    start_upstream(|_, _| Step::answer_with_headers(200, json_header(), USAGE_JSON))
}

fn fixture_with(anthropic: &FakeUpstream, zai: &FakeUpstream) -> Daemon {
    let daemon = Daemon::start(
        &["zai"],
        &[
            ("HUDDLE_ROUTER_ANTHROPIC_URL", anthropic.url()),
            ("ZAI_BASE_URL", zai.url()),
            ("ZAI_API_KEY", "abcdefghijklmnop".to_string()),
        ],
    );
    load_or_create(&ca_dir(&daemon), SystemTime::now()).expect("create CA");
    daemon
}

fn ca_dir(daemon: &Daemon) -> PathBuf {
    daemon.router_home.parent().expect("parent").join("ca")
}

fn state(port: u16) -> Value {
    let reply = request(port, "GET", "/huddle-router/state", &[], b"");
    assert_eq!(reply.status, 200, "{}", reply.body);
    serde_json::from_str(&reply.body).expect("state json")
}

fn post_messages(port: u16, body: &str, extra: &[(&str, &str)]) -> u16 {
    request(port, "POST", "/v1/messages", extra, body.as_bytes()).status
}

fn wait_for<T>(what: &str, mut probe: impl FnMut() -> Option<T>) -> T {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if let Some(found) = probe() {
            return found;
        }
        assert!(Instant::now() < deadline, "timed out waiting for {what}");
        std::thread::sleep(Duration::from_millis(50));
    }
}

fn recent_of(port: u16) -> Vec<Value> {
    state(port)["recent"].as_array().expect("recent").clone()
}

/// An open `GET /huddle-router/live` that yields one snapshot per call.
struct Live {
    reader: BufReader<TcpStream>,
}

impl Live {
    fn open(port: u16) -> Live {
        let mut stream = TcpStream::connect(("127.0.0.1", port)).expect("connect");
        stream
            .set_read_timeout(Some(Duration::from_secs(10)))
            .expect("timeout");
        stream
            .write_all(
                format!("GET /huddle-router/live HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n\r\n")
                    .as_bytes(),
            )
            .expect("write");
        let mut reader = BufReader::new(stream);
        let mut head = String::new();
        loop {
            let mut line = String::new();
            reader.read_line(&mut line).expect("head line");
            if line == "\r\n" {
                break;
            }
            head.push_str(&line);
        }
        assert!(head.starts_with("HTTP/1.1 200"), "{head}");
        let lower = head.to_lowercase();
        assert!(lower.contains("text/event-stream"), "{head}");
        assert!(lower.contains("cache-control: no-store"), "{head}");
        Live { reader }
    }

    fn next_snapshot(&mut self) -> Value {
        let mut event = String::new();
        loop {
            let mut line = String::new();
            let read = self.reader.read_line(&mut line).expect("sse line");
            assert!(read > 0, "the stream ended");
            let line = line.trim_end();
            if let Some(name) = line.strip_prefix("event: ") {
                event = name.to_string();
            } else if let Some(data) = line.strip_prefix("data: ") {
                assert_eq!(event, "snapshot");
                return serde_json::from_str(data).expect("snapshot json");
            }
        }
    }
}

fn curl_connect(daemon: &Daemon, body: &str) -> String {
    let ca = ca_cert_path(&ca_dir(daemon));
    let mut curl = Command::new("/usr/bin/curl")
        .args([
            "-sS",
            "--max-time",
            "20",
            "--proxy",
            &format!("http://127.0.0.1:{}", daemon.port),
            "--cacert",
            ca.to_str().expect("utf8 path"),
            "-H",
            "content-type: application/json",
            "-H",
            "x-api-key: client-key",
            "-H",
            &format!("x-claude-code-session-id: {SESSION}"),
            "--data-binary",
            "@-",
            "-w",
            "\n%{http_code}",
            "https://api.anthropic.com/v1/messages",
        ])
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run curl");
    curl.stdin
        .as_mut()
        .expect("curl stdin")
        .write_all(body.as_bytes())
        .expect("write body");
    let out = curl.wait_with_output().expect("curl output");
    String::from_utf8_lossy(&out.stdout).into_owned()
}

#[test]
fn the_root_serves_the_page_with_no_store() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    let reply = request(daemon.port, "GET", "/", &[], b"");

    assert_eq!(reply.status, 200);
    assert!(
        reply
            .header("content-type")
            .is_some_and(|v| v.starts_with("text/html")),
        "{:?}",
        reply.headers
    );
    assert_eq!(reply.header("cache-control"), Some("no-store"));
    assert!(reply.body.contains("<title>"), "{}", reply.body);
    assert!(anthropic.requests().is_empty(), "the page is not forwarded");
}

#[test]
fn the_page_refuses_a_foreign_host() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    let reply = raw_exchange(
        daemon.port,
        "GET / HTTP/1.1\r\nHost: evil.example:80\r\nConnection: close\r\n\r\n",
    );

    assert!(reply.starts_with("HTTP/1.1 421"), "{reply}");
}

#[test]
fn the_live_feed_opens_with_a_snapshot() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    let snapshot = Live::open(daemon.port).next_snapshot();

    assert_eq!(
        snapshot["per_second"].as_array().expect("per_second").len(),
        60
    );
    assert!(snapshot["pid"].as_u64().expect("pid") > 0);
    let lanes = snapshot["lanes"].as_array().expect("lanes");
    assert_eq!(lanes[0]["id"], "anthropic");
    assert_eq!(lanes[0]["enabled"], true);
    let zai_lane = lanes.iter().find(|l| l["id"] == "zai").expect("zai lane");
    assert_eq!(zai_lane["enabled"], true);
    let kimi = lanes.iter().find(|l| l["id"] == "kimi").expect("kimi lane");
    assert_eq!(kimi["enabled"], false);
    assert_eq!(kimi["setup"], "/kimi:setup");
    assert!(snapshot["jobs"].as_array().expect("jobs").is_empty());
}

#[test]
fn a_plain_claude_request_shows_with_session_and_tokens() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);
    let mut live = Live::open(daemon.port);
    live.next_snapshot();

    let status = post_messages(
        daemon.port,
        r#"{"model":"claude-opus-5-5","messages":[]}"#,
        &[("x-claude-code-session-id", SESSION)],
    );
    assert_eq!(status, 200);

    let snapshot = loop {
        let next = live.next_snapshot();
        if !next["recent"].as_array().expect("recent").is_empty() {
            break next;
        }
    };
    let row = &snapshot["recent"][0];
    assert_eq!(row["model"], "claude-opus-5-5");
    assert_eq!(row["lane"], "anthropic");
    assert_eq!(row["status"], 200);
    assert_eq!(row["session"], "cse_01Jq");
    assert_eq!(row["tokens_in"], 120);
    assert_eq!(row["tokens_out"], 34);
    let anthropic_lane = &snapshot["lanes"][0];
    assert_eq!(anthropic_lane["requests"], 1);
    assert_eq!(anthropic_lane["tokens_in"], 120);
    assert!(
        snapshot["per_second"]
            .as_array()
            .expect("ps")
            .iter()
            .any(|n| n.as_u64() > Some(0))
    );
}

#[test]
fn a_lane_request_shows_on_its_lane() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    post_messages(daemon.port, r#"{"model":"glm-5.3","messages":[]}"#, &[]);

    let recent = wait_for("the lane request", || {
        Some(recent_of(daemon.port)).filter(|r| !r.is_empty())
    });
    assert_eq!(recent[0]["lane"], "zai");
    assert_eq!(recent[0]["model"], "glm-5.3");
    assert_eq!(recent[0]["tokens_out"], 34);
    assert_eq!(recent[0]["session"], Value::Null);
}

#[test]
fn a_request_through_connect_shows_with_its_session() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    let out = curl_connect(&daemon, r#"{"model":"claude-opus-5-5","messages":[]}"#);
    assert!(out.ends_with("\n200"), "{out}");

    let recent = wait_for("the tunnelled request", || {
        Some(recent_of(daemon.port)).filter(|r| !r.is_empty())
    });
    assert_eq!(recent[0]["lane"], "anthropic");
    assert_eq!(recent[0]["session"], "cse_01Jq");
    assert_eq!(recent[0]["tokens_in"], 120);
}

#[test]
fn a_slow_stream_is_in_flight_until_it_ends() {
    let sse = |text: &str| format!("event: x\ndata: {text}\n\n");
    let chunks = vec![
        sse(r#"{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}"#),
        sse(r#"{"type":"message_delta","usage":{"output_tokens":9}}"#),
    ];
    let anthropic =
        start_upstream(move |_, _| Step::sse(chunks.clone(), Duration::from_millis(1500)));
    let zai = usage_upstream();
    let daemon = fixture_with(&anthropic, &zai);
    let port = daemon.port;
    let caller = std::thread::spawn(move || {
        post_messages(port, r#"{"model":"claude-opus-5-5","messages":[]}"#, &[])
    });

    let open = wait_for("the open request", || {
        let snapshot = state(port);
        snapshot["in_flight"]
            .as_array()
            .expect("in_flight")
            .first()
            .cloned()
    });
    assert_eq!(open["model"], "claude-opus-5-5");
    assert_eq!(open["lane"], "anthropic");
    assert_eq!(caller.join().expect("caller"), 200);

    let done = wait_for("the finished request", || {
        let snapshot = state(port);
        snapshot["in_flight"]
            .as_array()
            .expect("in_flight")
            .is_empty()
            .then_some(snapshot)
    });
    assert_eq!(done["recent"][0]["tokens_out"], 9);
}

#[test]
fn count_tokens_is_not_a_request_on_the_page() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    let reply = request(
        daemon.port,
        "POST",
        "/v1/messages/count_tokens",
        &[],
        br#"{"model":"claude-opus-5-5","messages":[]}"#,
    );
    assert_eq!(reply.status, 200);
    post_messages(
        daemon.port,
        r#"{"model":"claude-opus-5-5","messages":[]}"#,
        &[],
    );

    let recent = wait_for("the counted request", || {
        Some(recent_of(daemon.port)).filter(|r| !r.is_empty())
    });
    assert_eq!(recent.len(), 1, "{recent:?}");
    assert_eq!(state(daemon.port)["lanes"][0]["requests"], 1);
}

#[test]
fn a_compressed_anthropic_answer_passes_untouched_with_no_tokens() {
    let headers = vec![
        ("content-type".to_string(), "application/json".to_string()),
        ("content-encoding".to_string(), "gzip".to_string()),
    ];
    let anthropic =
        start_upstream(move |_, _| Step::answer_with_headers(200, headers.clone(), USAGE_JSON));
    let zai = usage_upstream();
    let daemon = fixture_with(&anthropic, &zai);

    let reply = request(
        daemon.port,
        "POST",
        "/v1/messages",
        &[],
        br#"{"model":"claude-opus-5-5","messages":[]}"#,
    );

    assert_eq!(reply.header("content-encoding"), Some("gzip"));
    assert_eq!(reply.body, USAGE_JSON);
    let recent = wait_for("the request", || {
        Some(recent_of(daemon.port)).filter(|r| !r.is_empty())
    });
    assert_eq!(recent[0]["tokens_in"], Value::Null);
    assert_eq!(recent[0]["tokens_out"], Value::Null);
}

#[test]
fn the_health_answer_keeps_its_shape() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);

    let reply = request(daemon.port, "GET", "/huddle-router/health", &[], b"");

    assert_eq!(
        reply.body,
        r#"{"ok":true,"name":"huddle-router","providers":["zai"],"components":[]}"#
    );
}
