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
        .env_remove("HUDDLE_HOME") // the default layout under HOME
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

/// A design budget of 50 ms; a debug build on a loaded CI runner gets room, but a hook that
/// waits for the router does not fit.
const CEILING: Duration = Duration::from_millis(800);

#[test]
fn before_setup_a_prompt_changes_nothing() {
    let home = TempHome::new();
    let port = free_port();

    let out = huddle(&home, port, &["hook", "prompt-submit"]);

    assert_silent(&out);
    assert!(!port_open(port), "no router before setup");
}

#[test]
fn a_prompt_brings_a_dead_router_back_without_waiting() {
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
    assert_eq!(stop.status.code(), Some(0));
    assert!(wait_for(port, false), "router stopped");

    let started = Instant::now();
    let out = huddle(&home, port, &["hook", "prompt-submit"]);

    assert!(
        started.elapsed() < CEILING,
        "the hook waited: {:?}",
        started.elapsed()
    );
    assert_silent(&out);
    assert!(wait_running(&home, port), "the router came back");
}

#[test]
fn a_prompt_with_the_router_up_returns_at_once() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let setup = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(setup.status.code(), Some(0));
    assert!(wait_for(port, true));

    let started = Instant::now();
    let out = huddle(&home, port, &["hook", "prompt-submit"]);

    assert!(started.elapsed() < CEILING, "{:?}", started.elapsed());
    assert_silent(&out);
    assert!(port_open(port));
}
