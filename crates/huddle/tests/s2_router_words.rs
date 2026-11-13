//! S2: the router words and hooks work with the parent and its worker.

use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::PathBuf;
use std::process::Output;
use std::thread;
use std::time::{Duration, Instant};

struct Fixture {
    home: TempHome,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct Pids {
    parent: i32,
    worker: i32,
}

impl Fixture {
    /// A home whose router is running with a worker; `setup` also writes the ledger the hooks
    /// look for.
    fn started(setup: bool) -> Fixture {
        let f = Fixture {
            home: TempHome::new(),
        };
        let args: &[&str] = if setup {
            &["setup", "--install"]
        } else {
            &["router", "start"]
        };
        let start = f.huddle(args);
        assert!(start.status.success(), "{start:?}");
        f.pids_when(|pids| pids.worker > 0);
        f
    }

    fn huddle(&self, args: &[&str]) -> Output {
        self.huddle_with(env!("CARGO_BIN_EXE_huddle"), args)
    }

    fn huddle_with(&self, binary: &str, args: &[&str]) -> Output {
        self.home
            .command(binary)
            .args(args)
            .output()
            .expect("run huddle")
    }

    fn record(&self) -> Option<Pids> {
        let text = fs::read_to_string(self.home.root().join("r/huddle/router.toml")).ok()?;
        let doc: toml::Table = text.parse().ok()?;
        let number = |key: &str| doc.get(key).and_then(toml::Value::as_integer).unwrap_or(0);
        Some(Pids {
            parent: number("pid") as i32,
            worker: number("worker") as i32,
        })
    }

    fn pids_when(&self, want: impl Fn(Pids) -> bool) -> Pids {
        let deadline = Instant::now() + Duration::from_secs(15);
        loop {
            if let Some(pids) = self.record()
                && want(pids)
            {
                return pids;
            }
            assert!(Instant::now() < deadline, "the record never named a worker");
            thread::sleep(Duration::from_millis(10));
        }
    }

    fn health_ok(&self) -> bool {
        let Ok(addr) = format!("127.0.0.1:{}", self.home.router_port()).parse::<SocketAddr>()
        else {
            return false;
        };
        let Ok(mut stream) = TcpStream::connect_timeout(&addr, Duration::from_secs(2)) else {
            return false;
        };
        let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
        let request = format!(
            "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            self.home.router_port()
        );
        let mut text = String::new();
        stream.write_all(request.as_bytes()).is_ok()
            && stream.read_to_string(&mut text).is_ok()
            && text.contains("huddle-router")
    }

    fn state_file(&self, name: &str) -> String {
        fs::read_to_string(self.home.root().join(".local/state/huddle").join(name))
            .unwrap_or_default()
    }

    fn status_json(&self) -> serde_json::Value {
        let out = self.huddle(&["router", "status", "--json"]);
        serde_json::from_slice(&out.stdout).unwrap_or(serde_json::Value::Null)
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = self.huddle(&["router", "stop"]);
    }
}

fn alive(pid: i32) -> bool {
    // SAFETY: signal 0 only checks that the process exists.
    unsafe { libc::kill(pid, 0) == 0 }
}

fn kill_9(pid: i32) {
    // SAFETY: the pid is a worker this test's router started.
    unsafe { libc::kill(pid, libc::SIGKILL) };
}

fn wait_dead(pids: &[i32]) {
    let deadline = Instant::now() + Duration::from_secs(15);
    while pids.iter().any(|pid| alive(*pid)) {
        assert!(Instant::now() < deadline, "still alive: {pids:?}");
        thread::sleep(Duration::from_millis(20));
    }
}

fn stdout_of(out: &Output) -> String {
    String::from_utf8_lossy(&out.stdout).into_owned()
}

#[test]
fn s2_stop_ends_parent_and_worker() {
    let f = Fixture::started(false);
    let pids = f.pids_when(|pids| pids.worker > 0);
    let out = f.huddle(&["router", "stop"]);
    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert!(
        stdout_of(&out).contains(&format!(
            "stopped (parent {}, worker {})",
            pids.parent, pids.worker
        )),
        "{}",
        stdout_of(&out)
    );
    wait_dead(&[pids.parent, pids.worker]);
    assert!(!f.health_ok());
}

#[test]
fn s2_update_hands_over_from_a_supervised_router() {
    let f = Fixture::started(false);
    let old = f.pids_when(|pids| pids.worker > 0);
    // The next version: another file with the same contents, so the record names another binary.
    let next: PathBuf = f.home.root().join("huddle-next");
    fs::copy(env!("CARGO_BIN_EXE_huddle"), &next).expect("copy the binary");
    let out = f.huddle_with(next.to_str().expect("path"), &["router", "start"]);
    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert!(stdout_of(&out).contains("updated"), "{}", stdout_of(&out));
    let fresh = f.pids_when(|pids| pids.worker > 0 && pids.parent != old.parent);
    assert!(f.health_ok(), "the new router answers");
    wait_dead(&[old.parent, old.worker]);
    assert!(alive(fresh.parent) && alive(fresh.worker));
    let next_record =
        fs::read_to_string(f.home.root().join("r/huddle/router.toml")).expect("record");
    assert!(next_record.contains("huddle-next"), "{next_record}");
}

#[test]
fn s2_status_names_both_pids_and_restarts() {
    let f = Fixture::started(false);
    let pids = f.pids_when(|pids| pids.worker > 0);
    let before = f.status_json();
    assert_eq!(before["pid"], pids.parent);
    assert_eq!(before["worker"], pids.worker);
    assert_eq!(before["restarts"], 0);
    let plain = stdout_of(&f.huddle(&["router", "status"]));
    assert!(
        plain.contains(&format!(
            "parent pid {} · worker pid {} · restarts 0",
            pids.parent, pids.worker
        )),
        "{plain}"
    );

    kill_9(pids.worker);
    let after = f.pids_when(|now| now.worker > 0 && now.worker != pids.worker);
    let status = f.status_json();
    assert_eq!(status["pid"], after.parent);
    assert_eq!(status["worker"], after.worker);
    assert_eq!(status["restarts"], 1);
    assert_eq!(status["last_restart"], "signal 9");
    let plain = stdout_of(&f.huddle(&["router", "status"]));
    assert!(plain.contains("restarts 1 (last: signal 9)"), "{plain}");
    let log = stdout_of(&f.huddle(&["router", "log"]));
    let line = log
        .lines()
        .find(|l| l.contains("worker exited (signal 9), restarted in "))
        .unwrap_or_else(|| panic!("{log}"));
    let waited: u64 = line
        .rsplit("restarted in ")
        .next()
        .and_then(|rest| rest.split(' ').next())
        .and_then(|ms| ms.parse().ok())
        .unwrap_or(u64::MAX);
    assert!(waited < 1000, "{line}");
}

#[test]
fn s2_hooks_never_start_a_second_parent() {
    let f = Fixture::started(true);
    let before = f.pids_when(|pids| pids.worker > 0);
    kill_9(before.worker);
    for event in ["prompt-submit", "session-start"] {
        let out = f.huddle(&["hook", event]);
        assert_eq!(out.status.code(), Some(0), "{out:?}");
    }
    let after = f.pids_when(|now| now.worker > 0 && now.worker != before.worker);
    assert_eq!(after.parent, before.parent, "the same parent");
    thread::sleep(Duration::from_secs(1));
    assert_eq!(f.record().map(|pids| pids.parent), Some(before.parent));
    let events = f.state_file("router-events.log");
    assert!(!events.contains("starting the router"), "{events}");
    assert_eq!(events.matches("supervisor pid=").count(), 1, "{events}");
}

#[test]
fn s2_a_held_lock_stops_the_session_hook_too() {
    let f = Fixture::started(true);
    let pids = f.pids_when(|pids| pids.worker > 0);
    // The parent stays alive and keeps the lock while its socket is unreachable: stop the
    // worker and the listener cannot be dialled only when the parent is gone, so model the
    // busy router with a lock held by the test on a stopped router.
    let out = f.huddle(&["router", "stop"]);
    assert_eq!(out.status.code(), Some(0), "{out:?}");
    wait_dead(&[pids.parent, pids.worker]);
    let vars = f.home.env();
    let pairs: Vec<(&str, &str)> = vars.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    let paths = huddle_config::paths::Paths::from_env(&huddle_config::env::Env::with(&pairs))
        .expect("paths");
    let _holder = huddle_daemon::singleton::lock(&huddle_daemon::serve::lock_path(&paths), "test")
        .expect("hold the router lock");
    let out = f.huddle(&["hook", "session-start"]);
    assert_eq!(out.status.code(), Some(0), "{out:?}");
    thread::sleep(Duration::from_secs(1));
    let events = f.state_file("router-events.log");
    assert!(events.contains("hook: router busy ("), "{events}");
    assert!(f.record().is_none(), "no second router started");
}

#[test]
fn s2_the_dashboard_state_names_both_pids() {
    let f = Fixture::started(false);
    let pids = f.pids_when(|pids| pids.worker > 0);
    let port = f.home.router_port();
    let mut stream = TcpStream::connect(("127.0.0.1", port)).expect("connect");
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .expect("timeout");
    let request = format!(
        "GET /huddle-router/state HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(request.as_bytes()).expect("write");
    let mut text = String::new();
    stream.read_to_string(&mut text).expect("read");
    let body = text.split("\r\n\r\n").nth(1).expect("body");
    let state: serde_json::Value = serde_json::from_str(body).expect("json");
    assert_eq!(state["router"]["supervision"]["parent_pid"], pids.parent);
    assert_eq!(state["router"]["supervision"]["worker_pid"], pids.worker);
    assert_eq!(state["router"]["supervision"]["restarts"], 0);
}
