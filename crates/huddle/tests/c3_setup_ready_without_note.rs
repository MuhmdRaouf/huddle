//! C3: `/huddle:<p>:setup` waits for the key page. When the key is stored but the state folder
//! refuses the ready note, the command still reports the real state: ready, exit 0. Real
//! binary, temp home, real HTTP on loopback, a fake browser and a fake upstream for the key check.

use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

const KEY: &str = "c3-test-key-0123456789abcdef";

struct World {
    home: TempHome,
    browser_log: PathBuf,
    upstream_url: String,
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
        let state = home.root().join(".local/state/huddle");
        fs::create_dir_all(&state).expect("mkdir");
        fs::write(state.join("settings-ledger.toml"), "{}").expect("ledger");
        // A folder where the ready note belongs: the page's server cannot write the note.
        fs::create_dir_all(state.join("key-page-zai.ready")).expect("blocker");
        fs::write(
            home.root().join("tmp/browser-path"),
            script.display().to_string(),
        )
        .expect("browser path");
        World {
            home,
            browser_log,
            upstream_url: upstream.url(),
        }
    }

    fn command(&self) -> Command {
        let browser = fs::read_to_string(self.home.root().join("tmp/browser-path")).expect("path");
        let mut command = self.home.command(env!("CARGO_BIN_EXE_huddle"));
        command
            .env("HUDDLE_BROWSER", browser)
            .env("ZAI_BASE_URL", &self.upstream_url);
        command
    }

    fn record(&self) -> toml::Table {
        fs::read_to_string(self.home.root().join("r/huddle/key-page-zai.toml"))
            .expect("record")
            .parse()
            .expect("toml")
    }

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

fn post_key(port: u16, path: &str, key: &str) -> String {
    let body = format!("{{\"key\": \"{key}\"}}");
    let text = format!(
        "POST {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Type: application/json\r\n\
         Origin: http://127.0.0.1:{port}\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    );
    let mut stream = TcpStream::connect(("127.0.0.1", port)).expect("connect");
    stream
        .set_read_timeout(Some(Duration::from_secs(20)))
        .expect("timeout");
    let _ = stream.write_all(text.as_bytes());
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    String::from_utf8_lossy(&raw).into_owned()
}

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

#[test]
fn c3_setup_is_ready_when_the_note_cannot_be_written() {
    let upstream = tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(|_, _| Step::answer(200, "{}")));
    let world = World::new(&upstream);
    let child = world
        .command()
        .args(["provider", "zai", "setup"])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn");
    let (port, path) = world.page();
    let reply = post_key(port, &path, KEY);
    assert!(reply.contains("\"state\":\"ready\""), "{reply}");
    let (code, stdout) = finish(child, Duration::from_secs(30));
    assert_eq!(code, Some(0), "{stdout}");
    assert!(stdout.starts_with("zai setup: ready.\n"), "{stdout}");
    assert!(stdout.contains("key: stored"), "{stdout}");
    assert!(!stdout.contains(KEY));
}
