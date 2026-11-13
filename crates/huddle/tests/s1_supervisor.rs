//! S1: `router serve` is a small parent that holds the socket and restarts a crashed worker.

use huddle_testkit::temp_home::TempHome;
use std::fs::{self, File};
use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
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
    fn started() -> Fixture {
        let f = Fixture {
            home: TempHome::new(),
        };
        let start = f.huddle(&["router", "start"]);
        assert!(start.status.success(), "{start:?}");
        f.pids_when(|pids| pids.worker > 0);
        f
    }

    fn huddle(&self, args: &[&str]) -> std::process::Output {
        self.home
            .command(env!("CARGO_BIN_EXE_huddle"))
            .args(args)
            .output()
            .expect("run huddle")
    }

    fn port(&self) -> u16 {
        self.home.router_port()
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

    /// One health request on a fresh connection; the whole answer or nothing.
    fn health(&self) -> Option<String> {
        let addr: SocketAddr = format!("127.0.0.1:{}", self.port()).parse().ok()?;
        let mut stream = TcpStream::connect_timeout(&addr, Duration::from_secs(2)).ok()?;
        stream
            .set_read_timeout(Some(Duration::from_secs(10)))
            .ok()?;
        let request = format!(
            "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            self.port()
        );
        stream.write_all(request.as_bytes()).ok()?;
        let mut text = String::new();
        stream.read_to_string(&mut text).ok()?;
        text.contains("huddle-router").then_some(text)
    }

    fn log(&self) -> String {
        fs::read_to_string(self.home.root().join(".local/state/huddle/router.log"))
            .unwrap_or_default()
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

#[test]
fn s1_a_killed_worker_is_back_within_a_second() {
    let f = Fixture::started();
    let before = f.pids_when(|pids| pids.worker > 0);
    assert!(f.health().is_some());
    let killed = Instant::now();
    kill_9(before.worker);
    let answer = loop {
        if let Some(text) = f.health() {
            break text;
        }
        assert!(killed.elapsed() < Duration::from_secs(5), "never answered");
    };
    assert!(
        killed.elapsed() < Duration::from_secs(1),
        "back after {:?}",
        killed.elapsed()
    );
    assert!(answer.contains("huddle-router"));
    let after = f.pids_when(|pids| pids.worker > 0 && pids.worker != before.worker);
    assert_eq!(after.parent, before.parent, "the parent stays");
    assert!(alive(after.parent));
    assert!(f.log().contains("huddle-router: worker exited (signal 9)"));
}

#[test]
fn s1_a_request_during_the_restart_is_served_not_refused() {
    let f = Fixture::started();
    let before = f.pids_when(|pids| pids.worker > 0);
    kill_9(before.worker);
    while alive(before.worker) {
        thread::sleep(Duration::from_millis(1));
    }
    // The worker is dead; the parent still holds the socket, so this connects and waits.
    let addr: SocketAddr = format!("127.0.0.1:{}", f.port()).parse().expect("addr");
    let mut stream = TcpStream::connect_timeout(&addr, Duration::from_secs(1)).expect("connect");
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .expect("timeout");
    let request = format!(
        "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
        f.port()
    );
    stream.write_all(request.as_bytes()).expect("write");
    let mut text = String::new();
    stream.read_to_string(&mut text).expect("read");
    assert!(text.starts_with("HTTP/1.1 200"), "{text}");
}

#[test]
fn s1_fast_crashes_back_off() {
    let f = Fixture::started();
    let mut pids = f.pids_when(|pids| pids.worker > 0);
    for _ in 0..3 {
        kill_9(pids.worker);
        let dead = pids.worker;
        pids = f.pids_when(|now| now.worker > 0 && now.worker != dead);
    }
    let deadline = Instant::now() + Duration::from_secs(5);
    let waits = loop {
        let waits: Vec<String> = f
            .log()
            .lines()
            .filter_map(|line| line.split("restarted in ").nth(1))
            .map(|rest| rest.trim_end_matches(" ms").to_string())
            .collect();
        if waits.len() >= 3 || Instant::now() >= deadline {
            break waits;
        }
        thread::sleep(Duration::from_millis(20));
    };
    assert_eq!(waits, ["0", "1000", "2000"], "{}", f.log());
}

#[test]
fn s1_the_parent_stops_when_asked() {
    let f = Fixture::started();
    let pids = f.pids_when(|pids| pids.worker > 0);
    // SAFETY: the pid is the parent this test's router started.
    unsafe { libc::kill(pids.parent, libc::SIGTERM) };
    let deadline = Instant::now() + Duration::from_secs(15);
    while alive(pids.parent) || alive(pids.worker) {
        assert!(Instant::now() < deadline, "parent or worker still alive");
        thread::sleep(Duration::from_millis(20));
    }
    let addr: SocketAddr = format!("127.0.0.1:{}", f.port()).parse().expect("addr");
    assert!(
        TcpStream::connect_timeout(&addr, Duration::from_millis(500)).is_err(),
        "the socket is closed"
    );
    let lock = File::options()
        .read(true)
        .write(true)
        .open(f.home.root().join("r/huddle/router.lock"))
        .expect("lock file");
    assert!(lock.try_lock().is_ok(), "the lock is released");
    assert!(
        !f.home.root().join("r/huddle/router.toml").exists(),
        "no ghost record"
    );
}
