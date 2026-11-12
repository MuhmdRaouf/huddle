//! K8: `/huddle:<p>:setup` with no key opens a one-time local page that takes the key.
//! Real binary, temp home, real HTTP on loopback, a fake browser through `HUDDLE_BROWSER` and
//! the fake upstream for the key check.

use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

const KEY: &str = "k8-test-key-0123456789abcdef";

/// One run: a temp home, a fake browser that records the URL it is given, and the helper's
/// record. Dropping it stops the helper and the router this home started.
struct World {
    home: TempHome,
    browser_log: PathBuf,
    extra: Vec<(String, String)>,
}

impl World {
    fn new() -> World {
        let home = TempHome::new();
        let browser_log = home.root().join("tmp/browser-url");
        let script = home.root().join("tmp/fake-browser");
        fs::write(
            &script,
            format!(
                "#!/bin/sh\nprintf '%s' \"$1\" > '{}'\n",
                browser_log.display()
            ),
        )
        .expect("script");
        fs::set_permissions(&script, fs::Permissions::from_mode(0o755)).expect("chmod");
        let extra = vec![("HUDDLE_BROWSER".to_owned(), script.display().to_string())];
        World {
            home,
            browser_log,
            extra,
        }
    }

    fn with_upstream(mut self, upstream: &FakeUpstream) -> World {
        self.extra.push(("ZAI_BASE_URL".to_owned(), upstream.url()));
        self
    }

    fn command(&self) -> Command {
        let mut command = self.home.command(env!("CARGO_BIN_EXE_huddle"));
        command.envs(self.extra.iter().map(|(k, v)| (k, v)));
        command
    }

    fn setup(&self) -> (Option<i32>, String, Duration) {
        let started = Instant::now();
        let output = self
            .command()
            .args(["provider", "zai", "setup"])
            .stdin(Stdio::null())
            .output()
            .expect("run");
        (
            output.status.code(),
            String::from_utf8_lossy(&output.stdout).into_owned(),
            started.elapsed(),
        )
    }

    fn record_path(&self) -> PathBuf {
        self.home.root().join("r/huddle/key-page-zai.toml")
    }

    fn record(&self) -> toml::Table {
        let text = fs::read_to_string(self.record_path()).expect("record");
        text.parse().expect("toml")
    }

    fn port(&self) -> u16 {
        self.record()["port"].as_integer().expect("port") as u16
    }

    fn pid(&self) -> u32 {
        self.record()["pid"].as_integer().expect("pid") as u32
    }

    /// The URL the fake browser received.
    fn opened_url(&self) -> String {
        let deadline = Instant::now() + Duration::from_secs(3);
        loop {
            if let Ok(url) = fs::read_to_string(&self.browser_log)
                && !url.is_empty()
            {
                return url;
            }
            assert!(Instant::now() < deadline, "the fake browser got no URL");
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    /// Runs setup and returns the one-time path `/key/zai/<token>`.
    fn start_page(&self) -> String {
        let (code, _, _) = self.setup();
        assert_eq!(code, Some(6));
        let url = self.opened_url();
        url.split_once(&format!("127.0.0.1:{}", self.port()))
            .expect("url")
            .1
            .to_owned()
    }

    fn keystore_file(&self) -> PathBuf {
        self.home.root().join(".config/zai-plugin-cc/env")
    }
}

impl Drop for World {
    fn drop(&mut self) {
        if let Ok(text) = fs::read_to_string(self.record_path())
            && let Ok(table) = text.parse::<toml::Table>()
            && let Some(pid) = table.get("pid").and_then(|v| v.as_integer())
        {
            // SAFETY: the pid is the helper this test started.
            unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
        }
        let _ = self
            .command()
            .args(["router", "stop"])
            .stdin(Stdio::null())
            .output();
    }
}

struct Reply {
    status: u16,
    headers: String,
    body: String,
}

/// One HTTP/1.1 request with `Connection: close`.
fn http(port: u16, method: &str, path: &str, headers: &[(&str, &str)], body: &str) -> Reply {
    let mut text = format!("{method} {path} HTTP/1.1\r\n");
    if !headers.iter().any(|(n, _)| n.eq_ignore_ascii_case("host")) {
        text.push_str(&format!("Host: 127.0.0.1:{port}\r\n"));
    }
    for (name, value) in headers {
        text.push_str(&format!("{name}: {value}\r\n"));
    }
    text.push_str(&format!(
        "Connection: close\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    ));
    let mut stream = TcpStream::connect(("127.0.0.1", port)).expect("connect");
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .expect("timeout");
    let _ = stream.write_all(text.as_bytes());
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    let raw = String::from_utf8_lossy(&raw).into_owned();
    let (head, body) = raw.split_once("\r\n\r\n").unwrap_or((&raw, ""));
    let status = head
        .split_whitespace()
        .nth(1)
        .and_then(|s| s.parse().ok())
        .unwrap_or(0);
    Reply {
        status,
        headers: head.to_owned(),
        body: body.to_owned(),
    }
}

fn post_key(port: u16, path: &str, key: &str) -> Reply {
    let origin = format!("http://127.0.0.1:{port}");
    http(
        port,
        "POST",
        path,
        &[("Content-Type", "application/json"), ("Origin", &origin)],
        &format!("{{\"key\": \"{key}\"}}"),
    )
}

fn alive(pid: u32) -> bool {
    // SAFETY: signal 0 only probes the pid.
    let found = unsafe { libc::kill(pid as libc::pid_t, 0) } == 0;
    found && !is_zombie(pid)
}

fn is_zombie(pid: u32) -> bool {
    let out = Command::new("ps")
        .args(["-o", "stat=", "-p", &pid.to_string()])
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_owned())
        .unwrap_or_default();
    out.starts_with('Z')
}

fn wait_gone(pid: u32, within: Duration) -> bool {
    let deadline = Instant::now() + within;
    while Instant::now() < deadline {
        if !alive(pid) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    !alive(pid)
}

fn upstream(refuse: Arc<AtomicBool>) -> FakeUpstream {
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |_, _| {
            if refuse.load(Ordering::SeqCst) {
                Step::answer(401, "{}")
            } else {
                Step::answer(200, "{}")
            }
        }))
}

fn json(body: &str) -> serde_json::Value {
    serde_json::from_str(body).expect("json")
}

#[test]
fn k8_setup_without_a_key_opens_a_one_time_page() {
    let world = World::new();
    let (code, stdout, took) = world.setup();
    assert_eq!(code, Some(6), "{stdout}");
    assert!(took < Duration::from_secs(2), "{took:?}");
    let url = world.opened_url();
    let port = world.port();
    let prefix = format!("http://127.0.0.1:{port}/key/zai/");
    let token = url.strip_prefix(&prefix).expect("url shape");
    assert_eq!(token.len(), 64, "{token}");
    assert!(token.bytes().all(|b| b.is_ascii_hexdigit()));
    assert_eq!(
        stdout,
        format!(
            "zai setup: needs a key.\nOpened the key page in your browser:\n  {url}  works once, closes in 10 min\nOr, in a terminal: huddle provider zai\n"
        )
    );
    let record = world.record_path();
    let mode = fs::metadata(&record).expect("meta").permissions().mode() & 0o777;
    assert_eq!(mode, 0o600);
    let text = fs::read_to_string(&record).expect("record");
    assert!(!text.contains(token), "{text}");
    assert!(world.record().contains_key("expires_at_ms"));
}

#[test]
fn k8_the_page_needs_the_token_the_host_and_the_origin() {
    let world = World::new();
    let path = world.start_page();
    let port = world.port();
    let bad = format!("/key/zai/{}", "0".repeat(64));
    assert_eq!(http(port, "GET", &bad, &[], "").status, 404);
    assert_eq!(http(port, "GET", "/", &[], "").status, 404);
    assert_eq!(
        http(port, "GET", &path, &[("Host", "evil.example")], "").status,
        403
    );
    assert_eq!(
        http(
            port,
            "GET",
            &path,
            &[("Host", &format!("localhost:{port}"))],
            ""
        )
        .status,
        403
    );
    let json_type = ("Content-Type", "application/json");
    let body = format!("{{\"key\": \"{KEY}\"}}");
    assert_eq!(
        http(port, "POST", &path, &[json_type], &body).status,
        403,
        "no Origin"
    );
    assert_eq!(
        http(
            port,
            "POST",
            &path,
            &[json_type, ("Origin", "http://evil.example")],
            &body
        )
        .status,
        403,
        "foreign Origin"
    );
    let origin = format!("http://127.0.0.1:{port}");
    let big = format!("{{\"key\": \"{}\"}}", "a".repeat(5000));
    assert_eq!(
        http(port, "POST", &path, &[json_type, ("Origin", &origin)], &big).status,
        413
    );
    assert_eq!(http(port, "GET", &path, &[], "").status, 200, "still up");
    assert!(!world.keystore_file().exists());
}

#[test]
fn k8_a_saved_key_finishes_the_setup_and_the_helper_exits() {
    let upstream = upstream(Arc::new(AtomicBool::new(false)));
    let world = World::new().with_upstream(&upstream);
    let path = world.start_page();
    let (port, pid) = (world.port(), world.pid());
    let reply = post_key(port, &path, KEY);
    assert_eq!(reply.status, 200, "{}", reply.body);
    let body = json(&reply.body);
    assert_eq!(body["state"], "ready", "{body}");
    assert!(body["steps"].as_array().is_some_and(|s| !s.is_empty()));
    assert!(!reply.body.contains(KEY));
    let stored = fs::read_to_string(world.keystore_file()).expect("keystore");
    assert!(stored.contains(KEY));
    let lane = fs::read_to_string(world.home.root().join(".config/huddle/providers.toml"))
        .expect("providers.toml");
    assert!(lane.contains("zai"), "{lane}");
    assert!(wait_gone(pid, Duration::from_secs(2)), "helper still alive");
    assert!(!world.record_path().exists());
}

#[test]
fn k8_a_refused_key_stores_nothing_and_the_page_stays() {
    let refuse = Arc::new(AtomicBool::new(true));
    let upstream = upstream(refuse.clone());
    let world = World::new().with_upstream(&upstream);
    let path = world.start_page();
    let (port, pid) = (world.port(), world.pid());
    let reply = post_key(port, &path, KEY);
    assert_eq!(reply.status, 200, "{}", reply.body);
    assert_eq!(json(&reply.body)["state"], "refused");
    assert!(!world.keystore_file().exists());
    assert!(alive(pid));
    refuse.store(false, Ordering::SeqCst);
    let again = post_key(port, &path, KEY);
    assert_eq!(json(&again.body)["state"], "ready", "{}", again.body);
    assert!(
        fs::read_to_string(world.keystore_file())
            .expect("keystore")
            .contains(KEY)
    );
    assert!(wait_gone(pid, Duration::from_secs(2)));
}

#[test]
fn k8_the_page_carries_its_security_headers_and_loads_nothing_outside() {
    let world = World::new();
    let path = world.start_page();
    let reply = http(world.port(), "GET", &path, &[], "");
    assert_eq!(reply.status, 200);
    let head = reply.headers.to_ascii_lowercase();
    let csp_line = reply
        .headers
        .lines()
        .find(|l| {
            l.to_ascii_lowercase()
                .starts_with("content-security-policy:")
        })
        .expect("csp");
    let nonce = csp_line
        .split("'nonce-")
        .nth(1)
        .and_then(|s| s.split('\'').next())
        .expect("nonce");
    assert!(nonce.len() >= 16);
    assert_eq!(
        csp_line.split_once(':').expect("csp").1.trim(),
        format!(
            "default-src 'none'; style-src 'nonce-{nonce}'; script-src 'nonce-{nonce}'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"
        )
    );
    assert!(head.contains("cache-control: no-store"));
    assert!(head.contains("referrer-policy: no-referrer"));
    assert!(head.contains("x-content-type-options: nosniff"));
    assert!(head.contains("content-type: text/html"));
    let html = &reply.body;
    assert!(html.contains(&format!("<style nonce=\"{nonce}\">")));
    assert!(html.contains(&format!("<script nonce=\"{nonce}\">")));
    assert!(
        !html.contains("style=\""),
        "inline style attributes break the CSP"
    );
    assert!(!html.contains("{{"), "unfilled placeholder");
    let urls: Vec<&str> = html
        .match_indices("http")
        .map(|(i, _)| &html[i..])
        .collect();
    assert!(
        urls.iter()
            .all(|u| u.starts_with("https://z.ai/manage-apikey/apikey-list")),
        "{urls:?}"
    );
    assert!(html.contains("Z.ai GLM"));
    let second = http(world.port(), "GET", &path, &[], "");
    let second_csp = second
        .headers
        .lines()
        .find(|l| {
            l.to_ascii_lowercase()
                .starts_with("content-security-policy:")
        })
        .expect("csp");
    assert_ne!(csp_line, second_csp, "a fresh nonce per response");
}

fn files_under(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        let Ok(meta) = fs::symlink_metadata(&path) else {
            continue;
        };
        if meta.is_dir() {
            files_under(&path, out);
        } else if meta.is_file() {
            out.push(path);
        }
    }
}

#[test]
fn k8_the_key_is_never_written_anywhere_but_the_keystore() {
    let upstream = upstream(Arc::new(AtomicBool::new(false)));
    let world = World::new().with_upstream(&upstream);
    let path = world.start_page();
    let (port, pid) = (world.port(), world.pid());
    assert_eq!(json(&post_key(port, &path, KEY).body)["state"], "ready");
    assert!(wait_gone(pid, Duration::from_secs(2)));
    let _ = world
        .command()
        .args(["router", "stop"])
        .stdin(Stdio::null())
        .output();
    std::thread::sleep(Duration::from_millis(300));
    let mut files = Vec::new();
    files_under(world.home.root(), &mut files);
    let keystore = world.keystore_file();
    let holders: Vec<&PathBuf> = files
        .iter()
        .filter(|f| fs::read(f).is_ok_and(|b| b.windows(KEY.len()).any(|w| w == KEY.as_bytes())))
        .collect();
    assert_eq!(holders, vec![&keystore], "{holders:?}");
}

#[test]
fn k8_the_helper_exits_after_its_life() {
    let world = World::new();
    let mut child = world
        .command()
        .args(["provider", "zai", "key-page"])
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn");
    {
        let mut stdin = child.stdin.take().expect("stdin");
        stdin
            .write_all(format!("{}\nlife_ms=1000\n", "ab".repeat(32)).as_bytes())
            .expect("token");
    }
    let started = Instant::now();
    let deadline = started + Duration::from_secs(5);
    while child.try_wait().expect("wait").is_none() {
        assert!(Instant::now() < deadline, "the helper outlived its life");
        std::thread::sleep(Duration::from_millis(25));
    }
    assert!(started.elapsed() >= Duration::from_millis(900));
    assert!(!world.record_path().exists(), "record removed");
}
