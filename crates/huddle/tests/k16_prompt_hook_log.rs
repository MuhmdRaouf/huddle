//! K16: the prompt hook says in router.log why it restarts the router, and the router says who
//! started it.

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

fn log_of(home: &TempHome) -> String {
    std::fs::read_to_string(home.root().join(".local/state/huddle/router.log")).unwrap_or_default()
}

#[test]
fn k16_the_prompt_hook_logs_why_it_restarts_the_router() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    assert_eq!(
        huddle(&home, port, &["setup", "--install"]).status.code(),
        Some(0)
    );
    assert!(wait_for(port, true), "router up after setup");
    assert_eq!(
        huddle(&home, port, &["router", "stop"]).status.code(),
        Some(0)
    );
    assert!(wait_for(port, false), "router stopped");

    huddle(&home, port, &["hook", "prompt-submit"]);
    assert!(wait_for(port, true), "the router came back");
    thread::sleep(Duration::from_millis(500));

    let log = log_of(&home);
    assert!(
        log.contains("huddle-router: prompt-submit probe: no router record"),
        "{log}"
    );
    assert!(log.contains("by=prompt-submit"), "{log}");
    assert!(log.contains("by=setup"), "{log}");
    assert!(
        log.contains("exit pid=") && log.contains("reason=SIGTERM"),
        "{log}"
    );
}
