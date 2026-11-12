use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::net::{SocketAddr, TcpStream};
use std::process::{Command, Output};
use std::thread;
use std::time::{Duration, Instant};

fn huddle(home: &TempHome, port: u16, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .output()
        .expect("run huddle")
}

fn port_open(port: u16) -> bool {
    let addr: SocketAddr = format!("127.0.0.1:{port}").parse().expect("addr");
    TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok()
}

fn wait_for(port: u16, want_open: bool) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if port_open(port) == want_open {
            return true;
        }
        thread::sleep(Duration::from_millis(50));
    }
    false
}

struct StopOnDrop<'a>(&'a TempHome, u16);
impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        let _ = huddle(self.0, self.1, &["router", "stop"]);
    }
}

/// Waits until the router answers status: its record then names a pid a stop can kill. Then
/// waits out the detached `router start` the hook spawned: it lives at most its own 3 s
/// budget, and any process of this run still alive at the end fails the suite guard.
fn wait_running(home: &TempHome, port: u16) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut up = false;
    while Instant::now() < deadline {
        if huddle(home, port, &["router", "status"]).status.code() == Some(0) {
            up = true;
            break;
        }
        thread::sleep(Duration::from_millis(50));
    }
    // A router that starts late under load still writes its record; the home's drop stops it.
    home.settle_router(Duration::from_secs(10));
    thread::sleep(Duration::from_secs(4));
    up
}

fn assert_silent(out: &Output) {
    assert_eq!(
        out.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(String::from_utf8_lossy(&out.stdout), "");
    assert_eq!(String::from_utf8_lossy(&out.stderr), "");
}

#[test]
fn before_setup_a_session_start_changes_nothing() {
    let home = TempHome::new();
    let port = free_port();

    let out = huddle(&home, port, &["hook", "session-start"]);

    assert_silent(&out);
    assert!(!port_open(port), "no router before setup");
    assert!(
        !home.root().join(".config/huddle").exists(),
        "no state written"
    );
}

#[test]
fn a_session_start_brings_a_stopped_router_back_without_waiting() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);

    let setup = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        setup.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&setup.stderr)
    );
    assert!(wait_for(port, true), "router up after setup");

    let stop = huddle(&home, port, &["router", "stop"]);
    assert_eq!(
        stop.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&stop.stderr)
    );
    assert!(wait_for(port, false), "router stopped");

    let started = Instant::now();
    let out = huddle(&home, port, &["hook", "session-start"]);
    let elapsed = started.elapsed();

    assert_silent(&out);
    assert!(
        elapsed < Duration::from_secs(3),
        "the hook returned at once: {elapsed:?}"
    );
    assert!(wait_running(&home, port), "the stopped router came back");
}

#[test]
fn a_running_router_is_left_alone() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let setup = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        setup.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&setup.stderr)
    );
    assert!(wait_for(port, true));

    let out = huddle(&home, port, &["hook", "session-start"]);

    assert_silent(&out);
    assert!(port_open(port));
}

#[test]
fn unknown_and_old_hook_words_exit_zero_silently() {
    let home = TempHome::new();
    let port = free_port();
    for word in ["bogus", "pre-tool-use", "idle-wait", "external"] {
        let out = huddle(&home, port, &["hook", word]);
        assert_silent(&out);
    }
    let bare = huddle(&home, port, &["hook"]);
    assert_silent(&bare);
}

#[test]
fn a_broken_home_still_exits_zero() {
    let home = TempHome::new();
    let port = free_port();
    let bad_home = home.root().join("not-a-dir");
    std::fs::write(&bad_home, "x").expect("write");

    let out = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["hook", "session-start"])
        .env_clear()
        .env("HOME", &bad_home)
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .output()
        .expect("run");

    assert_silent(&out);
}
