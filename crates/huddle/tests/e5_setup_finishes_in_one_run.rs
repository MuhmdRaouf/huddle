//! E5: `/huddle:<p>:setup` with no key waits for the key page. The page's server finishes the
//! setup itself; the command prints the ready report when it is still there, and the next
//! prompt prints it once when it is not. Real binary, temp home, real HTTP on loopback, a fake
//! browser and a fake upstream for the key check.

use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

const KEY: &str = "e5-test-key-0123456789abcdef";

struct World {
    home: TempHome,
    browser_log: PathBuf,
    extra: Vec<(String, String)>,
}

impl World {
    fn new(upstream: &FakeUpstream) -> World {
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
        // /huddle:setup's ledger: any file at the path counts for the setup word.
        let ledger = home.root().join(".local/state/huddle/settings-ledger.toml");
        fs::create_dir_all(ledger.parent().expect("parent")).expect("mkdir");
        fs::write(&ledger, "{}").expect("ledger");
        let extra = vec![
            ("HUDDLE_BROWSER".to_owned(), script.display().to_string()),
            ("ZAI_BASE_URL".to_owned(), upstream.url()),
        ];
        World {
            home,
            browser_log,
            extra,
        }
    }

    fn with(mut self, name: &str, value: &str) -> World {
        self.extra.push((name.to_owned(), value.to_owned()));
        self
    }

    fn command(&self) -> Command {
        let mut command = self.home.command(env!("CARGO_BIN_EXE_huddle"));
        command.envs(self.extra.iter().map(|(k, v)| (k, v)));
        command
    }

    fn spawn_setup(&self) -> Child {
        self.command()
            .args(["provider", "zai", "setup"])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn")
    }

    fn record(&self) -> toml::Table {
        fs::read_to_string(self.home.root().join("r/huddle/key-page-zai.toml"))
            .expect("record")
            .parse()
            .expect("toml")
    }

    fn pid(&self) -> u32 {
        self.record()["pid"].as_integer().expect("pid") as u32
    }

    /// The path `/key/zai/<token>` the fake browser received, and the helper's port.
    fn page(&self) -> (u16, String) {
        let deadline = Instant::now() + Duration::from_secs(20);
        let url = loop {
            if let Ok(url) = fs::read_to_string(&self.browser_log)
                && !url.is_empty()
            {
                break url;
            }
            assert!(Instant::now() < deadline, "the fake browser got no URL");
            std::thread::sleep(Duration::from_millis(20));
        };
        let port = self.record()["port"].as_integer().expect("port") as u16;
        let path = url
            .split_once(&format!("127.0.0.1:{port}"))
            .expect("url")
            .1
            .to_owned();
        (port, path)
    }

    fn keystore_file(&self) -> PathBuf {
        self.home.root().join(".config/huddle/keys/zai")
    }

    fn prompt_hook(&self) -> String {
        let out = self
            .command()
            .args(["hook", "prompt-submit"])
            .stdin(Stdio::null())
            .output()
            .expect("hook");
        assert_eq!(out.status.code(), Some(0));
        String::from_utf8_lossy(&out.stdout).into_owned()
    }
}

impl Drop for World {
    fn drop(&mut self) {
        if let Ok(text) = fs::read_to_string(self.home.root().join("r/huddle/key-page-zai.toml"))
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
    body: String,
}

fn http(port: u16, method: &str, path: &str, headers: &[(&str, &str)], body: &str) -> Reply {
    let mut text = format!("{method} {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n");
    for (name, value) in headers {
        text.push_str(&format!("{name}: {value}\r\n"));
    }
    text.push_str(&format!(
        "Connection: close\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    ));
    let mut stream = TcpStream::connect(("127.0.0.1", port)).expect("connect");
    stream
        .set_read_timeout(Some(Duration::from_secs(20)))
        .expect("timeout");
    let _ = stream.write_all(text.as_bytes());
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    let raw = String::from_utf8_lossy(&raw).into_owned();
    let body = raw.split_once("\r\n\r\n").map_or("", |(_, body)| body);
    Reply {
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

fn accepting() -> FakeUpstream {
    upstream(Arc::new(AtomicBool::new(false)))
}

/// Waits for the child and returns its exit code and stdout.
fn finish(mut child: Child, within: Duration) -> (Option<i32>, String) {
    let deadline = Instant::now() + within;
    while child.try_wait().expect("wait").is_none() {
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            panic!("the command did not end within {within:?}");
        }
        std::thread::sleep(Duration::from_millis(25));
    }
    let mut text = String::new();
    if let Some(mut out) = child.stdout.take() {
        let _ = out.read_to_string(&mut text);
    }
    (child.wait().expect("status").code(), text)
}

fn alive(pid: u32) -> bool {
    // SAFETY: signal 0 only probes the pid.
    let found = unsafe { libc::kill(pid as libc::pid_t, 0) } == 0;
    let stat = Command::new("ps")
        .args(["-o", "stat=", "-p", &pid.to_string()])
        .output()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_owned())
        .unwrap_or_default();
    found && !stat.starts_with('Z')
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

fn json(body: &str) -> serde_json::Value {
    serde_json::from_str(body).expect("json")
}

fn files_under(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        match entry.file_type() {
            Ok(kind) if kind.is_dir() => files_under(&path, out),
            Ok(kind) if kind.is_file() => out.push(path),
            _ => {}
        }
    }
}

#[test]
fn e5_setup_waits_for_the_key_and_finishes() {
    let upstream = accepting();
    let world = World::new(&upstream);
    let child = world.spawn_setup();
    let (port, path) = world.page();
    assert!(alive(world.pid()));
    let reply = post_key(port, &path, KEY);
    assert_eq!(json(&reply.body)["state"], "ready", "{}", reply.body);
    let (code, stdout) = finish(child, Duration::from_secs(20));
    assert_eq!(code, Some(0), "{stdout}");
    assert!(stdout.starts_with("zai setup: ready.\n"), "{stdout}");
    assert!(stdout.contains("key: stored"), "{stdout}");
    assert!(!stdout.contains(KEY));
    assert_eq!(
        world.prompt_hook(),
        "",
        "the command printed it: the prompt stays silent"
    );
}

#[test]
fn e5_server_finishes_setup_after_the_command_left() {
    let upstream = accepting();
    let world = World::new(&upstream).with("HUDDLE_KEY_WAIT_MS", "300");
    let (code, stdout) = finish(world.spawn_setup(), Duration::from_secs(20));
    assert_eq!(code, Some(0), "{stdout}");
    let (port, path) = world.page();
    assert_eq!(
        stdout,
        format!(
            "http://127.0.0.1:{port}{path}\nSetup finishes by itself when you save the key. Nothing else to run.\n"
        )
    );
    let pid = world.pid();
    assert_eq!(json(&post_key(port, &path, KEY).body)["state"], "ready");
    assert!(wait_gone(pid, Duration::from_secs(3)));
    let lane = fs::read_to_string(world.home.root().join(".config/huddle/providers.toml"))
        .expect("providers.toml");
    assert!(lane.contains("zai"), "{lane}");
    assert!(
        fs::read_to_string(world.keystore_file())
            .expect("keystore")
            .contains(KEY)
    );
}

#[test]
fn e5_next_prompt_reports_ready_once() {
    let upstream = accepting();
    let world = World::new(&upstream).with("HUDDLE_KEY_WAIT_MS", "300");
    finish(world.spawn_setup(), Duration::from_secs(20));
    let (port, path) = world.page();
    let pid = world.pid();
    post_key(port, &path, KEY);
    assert!(wait_gone(pid, Duration::from_secs(3)));
    let first = world.prompt_hook();
    assert!(first.starts_with("zai setup: ready.\n"), "{first}");
    assert!(first.contains("key: stored"), "{first}");
    assert!(!first.contains(KEY));
    assert_eq!(world.prompt_hook(), "", "the report prints once");
}

#[test]
fn e5_key_page_confirms_and_counts_down() {
    let upstream = accepting();
    let world = World::new(&upstream).with("HUDDLE_KEY_WAIT_MS", "300");
    finish(world.spawn_setup(), Duration::from_secs(20));
    let (port, path) = world.page();
    let html = http(port, "GET", &path, &[], "").body;
    assert!(html.contains("Key saved, setup done"), "confirmation");
    assert!(html.contains("window.close()"), "closes the tab");
    assert!(html.contains("You can close this tab."), "fallback text");
    assert!(html.contains("data-countdown=\"10\""), "ten seconds");
    assert!(!html.contains("style=\""));
}

#[test]
fn e5_refused_key_can_be_retried() {
    let refuse = Arc::new(AtomicBool::new(true));
    let upstream = upstream(refuse.clone());
    let world = World::new(&upstream);
    let child = world.spawn_setup();
    let (port, path) = world.page();
    assert_eq!(json(&post_key(port, &path, KEY).body)["state"], "refused");
    assert!(!world.keystore_file().exists());
    std::thread::sleep(Duration::from_millis(500));
    assert!(alive(world.pid()), "the page stays for another try");
    refuse.store(false, Ordering::SeqCst);
    assert_eq!(json(&post_key(port, &path, KEY).body)["state"], "ready");
    let (code, stdout) = finish(child, Duration::from_secs(20));
    assert_eq!(code, Some(0), "{stdout}");
    assert!(stdout.starts_with("zai setup: ready.\n"), "{stdout}");
}

#[test]
fn e5_setup_ends_after_the_page_expires() {
    let upstream = accepting();
    let world = World::new(&upstream).with("HUDDLE_KEY_PAGE_LIFE_MS", "1000");
    let (code, stdout) = finish(world.spawn_setup(), Duration::from_secs(20));
    assert_eq!(code, Some(6), "{stdout}");
    assert!(stdout.starts_with("zai setup: needs a key.\n"), "{stdout}");
    assert!(
        stdout.ends_with("Or, in a terminal: huddle provider zai\n"),
        "{stdout}"
    );
    assert_eq!(world.prompt_hook(), "");
}

#[test]
fn e5_key_never_leaves_the_keystore_path() {
    let upstream = accepting();
    let world = World::new(&upstream);
    let child = world.spawn_setup();
    let (port, path) = world.page();
    let pid = world.pid();
    let reply = post_key(port, &path, KEY);
    assert!(!reply.body.contains(KEY));
    let (_, stdout) = finish(child, Duration::from_secs(20));
    assert!(!stdout.contains(KEY));
    assert!(wait_gone(pid, Duration::from_secs(3)));
    let hook = world.prompt_hook();
    assert!(!hook.contains(KEY));
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
