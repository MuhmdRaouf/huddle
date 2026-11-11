mod e2e_support;

use e2e_support::{Daemon, raw_exchange, request};
use huddle_pki::ca::{ca_cert_path, load_or_create};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
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
    daemon.home.root().join(".local/share/huddle/ca")
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
    assert_eq!(row["session"], SESSION);
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
    assert_eq!(recent[0]["session"], SESSION);
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

/// A router started from a home the test prepared first: `prepare` runs before the spawn with
/// the home and the port the router will take. No provider key is in the environment.
struct Prepared {
    _home: TempHome,
    port: u16,
    child: std::process::Child,
}

impl Drop for Prepared {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn start_prepared(providers: &str, prepare: impl FnOnce(&TempHome, u16)) -> Prepared {
    let home = TempHome::new();
    let port = free_port();
    let router_home = home.root().join(".config/huddle");
    std::fs::create_dir_all(&router_home).expect("mkdir router home");
    std::fs::write(
        router_home.join("providers.json"),
        format!("{{\"providers\":[{providers}]}}"),
    )
    .expect("write providers");
    prepare(&home, port);
    let child = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "serve"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn router");
    let prepared = Prepared {
        _home: home,
        port,
        child,
    };
    wait_for("health", || {
        request_ok(port, "/huddle-router/health").then_some(())
    });
    prepared
}

fn request_ok(port: u16, path: &str) -> bool {
    e2e_support::try_request(port, "GET", path, &[], b"").is_some_and(|reply| reply.status == 200)
}

fn write_settings(home: &TempHome, text: &str) {
    let dir = home.root().join(".claude");
    std::fs::create_dir_all(&dir).expect("mkdir claude");
    std::fs::write(dir.join("settings.json"), text).expect("write settings");
}

fn wiring_of(prepared: &Prepared) -> Value {
    wait_for("a cached wiring answer", || {
        let wiring = state(prepared.port)["wiring"].clone();
        (wiring["proxy"] == true || wiring["ca"] == true).then_some(wiring)
    })
}

#[test]
fn settings_that_point_at_this_router_show_as_wired() {
    let prepared = start_prepared("", |home, port| {
        let ca = home.root().join("ca.pem");
        std::fs::write(&ca, "pem").expect("write ca");
        write_settings(
            home,
            &format!(
                r#"{{"env":{{"HTTPS_PROXY":"http://127.0.0.1:{port}","NODE_EXTRA_CA_CERTS":"{}"}}}}"#,
                ca.display()
            ),
        );
    });
    let wiring = wiring_of(&prepared);
    assert_eq!(wiring["proxy"], true);
    assert_eq!(wiring["ca"], true);
}

#[test]
fn a_proxy_on_another_port_or_a_missing_ca_file_shows_as_not_wired() {
    let prepared = start_prepared("", |home, port| {
        write_settings(
            home,
            &format!(
                r#"{{"env":{{"HTTPS_PROXY":"http://127.0.0.1:{}","NODE_EXTRA_CA_CERTS":"/nowhere/ca.pem"}}}}"#,
                port.wrapping_add(1)
            ),
        );
    });
    // The first request starts the read; the second sees its result.
    state(prepared.port);
    std::thread::sleep(Duration::from_millis(1500));
    let wiring = state(prepared.port)["wiring"].clone();
    assert_eq!(wiring["proxy"], false);
    assert_eq!(wiring["ca"], false);
}

#[test]
fn no_settings_file_shows_as_not_wired() {
    let prepared = start_prepared("", |_, _| {});
    // The first request starts the read; the second sees its result.
    state(prepared.port);
    std::thread::sleep(Duration::from_millis(1500));
    let wiring = state(prepared.port)["wiring"].clone();
    assert_eq!(wiring["proxy"], false);
    assert_eq!(wiring["ca"], false);
}

fn lane_of<'a>(snapshot: &'a Value, id: &str) -> &'a Value {
    snapshot["lanes"]
        .as_array()
        .expect("lanes")
        .iter()
        .find(|lane| lane["id"] == id)
        .expect("lane")
}

#[test]
fn a_stored_key_shows_as_yes_and_a_missing_one_as_no() {
    use std::os::unix::fs::PermissionsExt;
    let prepared = start_prepared("\"zai\",\"kimi\"", |home, _| {
        let dir = home.root().join(".config/zai-plugin-cc");
        std::fs::create_dir_all(&dir).expect("mkdir");
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700)).expect("chmod");
        let file = dir.join("env");
        std::fs::write(&file, "ZAI_API_KEY=abcdefghijklmnop\n").expect("write key");
        std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o600)).expect("chmod");
    });
    let snapshot = wait_for("a cached key answer", || {
        let snapshot = state(prepared.port);
        (lane_of(&snapshot, "zai")["key"] == true).then_some(snapshot)
    });
    assert_eq!(lane_of(&snapshot, "kimi")["key"], false);
    assert!(lane_of(&snapshot, "anthropic")["key"].is_null());
    assert!(!snapshot.to_string().contains("abcdefghijklmnop"));
}

#[test]
fn a_running_job_with_a_live_driver_is_listed() {
    use huddle_jobs::id::iso_ms;
    use huddle_jobs::liveness::{ProcessRecord, driver_record_path, write_record};
    use huddle_jobs::model::{Access, Job, JobState};
    use huddle_jobs::store::JobStore;
    let prepared = start_prepared("", |home, _| {
        let vars = home.env();
        let borrowed: Vec<(&str, &str)> =
            vars.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
        let paths =
            huddle_config::paths::Paths::from_env(&huddle_config::env::Env::with(&borrowed))
                .expect("paths");
        let now = huddle_jobs::id::now_ms();
        let job = Job {
            id: "j-e2e".to_string(),
            harness: "opencode".to_string(),
            title: "add lane tests".to_string(),
            task: "add lane tests".to_string(),
            access: Access::Readonly,
            effort: None,
            state: JobState::Running,
            repo_root: "/work/huddle".to_string(),
            base_sha: "0".repeat(40),
            base_branch: None,
            worktree: String::new(),
            branch: None,
            attempts: Vec::new(),
            failure: None,
            decision: None,
            created_at: iso_ms(now),
            updated_at: iso_ms(now),
            version: 0,
        };
        let store = JobStore::for_repo(&paths, std::path::Path::new("/work/huddle"));
        store.create(&job).expect("create job");
        let pid = std::process::id();
        let record = ProcessRecord {
            pid,
            start_token: huddle_procs::live::start_token(pid).expect("token"),
        };
        write_record(&driver_record_path(&store.job_dir("j-e2e")), &record).expect("record");
    });
    let snapshot = wait_for("the job row", || {
        let snapshot = state(prepared.port);
        (!snapshot["jobs"].as_array().expect("jobs").is_empty()).then_some(snapshot)
    });
    let job = &snapshot["jobs"][0];
    assert_eq!(job["id"], "j-e2e");
    assert_eq!(job["harness"], "opencode");
    assert_eq!(job["repo"], "huddle");
    assert_eq!(job["state"], "running");
}

const LOG_SESSION: &str = "5d8c6baf-e3f2-45aa-a5d0-6db99ffbed99";
const LOGGED_REQUEST: &str = "req_011CVe2eJoin";

/// A Claude Code log with one finished assistant turn that carries `LOGGED_REQUEST`.
fn write_session_log(daemon: &Daemon) {
    let dir = daemon.home.root().join(".claude/projects/-tmp-proj");
    std::fs::create_dir_all(&dir).expect("mkdir projects");
    let at = huddle_jobs::id::iso_ms(huddle_jobs::id::now_ms() - 60_000);
    let line = format!(
        r#"{{"type":"assistant","timestamp":"{at}","cwd":"/tmp/proj","gitBranch":"main","sessionId":"{LOG_SESSION}","requestId":"{LOGGED_REQUEST}","message":{{"model":"claude-opus-5-5","stop_reason":"end_turn","usage":{{"input_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":9}},"content":[{{"type":"text","text":"PRIVATE REPLY TEXT"}}]}}}}"#
    );
    std::fs::write(
        dir.join(format!("{LOG_SESSION}.jsonl")),
        format!("{line}\n"),
    )
    .expect("log");
}

fn logged_upstream() -> FakeUpstream {
    start_upstream(|_, _| {
        let mut headers = json_header();
        headers.push(("request-id".to_string(), LOGGED_REQUEST.to_string()));
        Step::answer_with_headers(200, headers, r#"{"type":"message"}"#)
    })
}

#[test]
fn a_request_is_credited_to_the_agent_whose_log_carries_its_request_id() {
    let (anthropic, zai) = (logged_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);
    write_session_log(&daemon);
    let _viewer = Live::open(daemon.port);

    let status = post_messages(
        daemon.port,
        r#"{"model":"claude-opus-5-5","messages":[]}"#,
        &[("x-claude-code-session-id", LOG_SESSION)],
    );
    assert_eq!(status, 200);

    let snapshot = wait_for("the request credited to its agent", || {
        let snapshot = state(daemon.port);
        snapshot["recent"][0]["agent"]
            .is_string()
            .then_some(snapshot)
    });
    let row = &snapshot["recent"][0];
    assert_eq!(row["agent"], format!("{LOG_SESSION}:main"));
    assert_eq!(row["session"], LOG_SESSION);
    assert_eq!(row["upstream_id"], LOGGED_REQUEST);
    assert_eq!(row["tokens_in"], 1105);
    assert_eq!(row["cache_read"], 1000);
    assert_eq!(row["cache_write"], 100);
    assert_eq!(row["tokens_out"], 9);
    let session = &snapshot["sessions"][0];
    assert_eq!(session["id"], LOG_SESSION);
    assert_eq!(session["state"], "waiting");
    assert_eq!(session["cwd"], "/tmp/proj");
    let text = snapshot.to_string();
    assert!(!text.contains("PRIVATE REPLY TEXT"), "no message text");
    assert!(!text.contains("req_011CVe2eJoin\"]"), "no request id list");
}

#[test]
fn a_session_with_a_request_in_flight_is_working() {
    let held = start_upstream(|_, _| Step::sse_head_then_silence());
    let (anthropic, zai) = (held, usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);
    write_session_log(&daemon);
    let _viewer = Live::open(daemon.port);
    let port = daemon.port;
    std::thread::spawn(move || {
        post_messages(
            port,
            r#"{"model":"claude-opus-5-5","messages":[]}"#,
            &[("x-claude-code-session-id", LOG_SESSION)],
        )
    });

    let snapshot = wait_for("the session working", || {
        let snapshot = state(daemon.port);
        (snapshot["sessions"][0]["state"] == "working").then_some(snapshot)
    });

    assert_eq!(snapshot["in_flight"][0]["session"], LOG_SESSION);
}

#[test]
fn the_router_card_the_checks_and_the_hosts_fill_while_a_page_is_open() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);
    let _viewer = Live::open(daemon.port);

    let snapshot = wait_for("plugin checks and a process sample", || {
        let snapshot = state(daemon.port);
        let ready = snapshot["plugins"]["state"] == "ready"
            && snapshot["wiring"]["state"] != "checking"
            && snapshot["router"]["rss_bytes"].is_u64();
        ready.then_some(snapshot)
    });

    assert_eq!(snapshot["wiring"]["state"], "bad");
    assert_eq!(snapshot["router"]["viewers"], 1);
    assert!(snapshot["router"]["cpu_pct"].is_f64());
    assert!(snapshot["router"]["connections"].is_u64());
    assert!(snapshot["plugins"]["rows"][0]["requests"].is_u64());
    assert!(
        snapshot["plugins"]["checks"]
            .as_array()
            .is_some_and(|c| !c.is_empty())
    );
    assert!(snapshot["hosts"].is_array());
}

#[test]
fn with_no_page_open_no_session_log_or_check_is_read() {
    let (anthropic, zai) = (usage_upstream(), usage_upstream());
    let daemon = fixture_with(&anthropic, &zai);
    write_session_log(&daemon);

    std::thread::sleep(Duration::from_millis(300));
    let snapshot = state(daemon.port);
    std::thread::sleep(Duration::from_millis(600));
    let snapshot_later = state(daemon.port);

    for shown in [&snapshot, &snapshot_later] {
        assert_eq!(shown["sessions"].as_array().map(Vec::len), Some(0));
        assert_eq!(shown["plugins"]["state"], "checking");
        assert_eq!(shown["harnesses"]["state"], "checking");
        assert!(shown["router"]["rss_bytes"].is_null());
    }
}
