//! L11: the worker runs on four runtime threads and the supervisor on one, so an idle router
//! holds few threads.

use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};

const WORKER_LIMIT: usize = 10;
const SUPERVISOR_LIMIT: usize = 4;

struct Pids {
    parent: u32,
    worker: u32,
}

fn record(home: &TempHome) -> Option<Pids> {
    let text = fs::read_to_string(home.root().join("r/huddle/router.toml")).ok()?;
    let doc: toml::Table = text.parse().ok()?;
    let number = |key: &str| doc.get(key).and_then(toml::Value::as_integer).unwrap_or(0) as u32;
    Some(Pids {
        parent: number("pid"),
        worker: number("worker"),
    })
}

/// The thread count of a process: `Threads` in `/proc` on Linux, one `ps -M` row per thread
/// elsewhere.
fn threads(pid: u32) -> usize {
    if let Ok(status) = fs::read_to_string(format!("/proc/{pid}/status")) {
        return status
            .lines()
            .find_map(|line| line.strip_prefix("Threads:"))
            .and_then(|count| count.trim().parse().ok())
            .unwrap_or(0);
    }
    let out = Command::new("ps")
        .args(["-M", "-p", &pid.to_string()])
        .output()
        .expect("ps");
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .skip(1)
        .filter(|line| !line.trim().is_empty())
        .count()
}

/// Polls a process's thread count every 100 ms for up to 15 s and stops at the first sample
/// within `limit`: the blocking pool's short-lived threads leave once their keep-alive passes,
/// so the idle count shows between them. `Err` holds every sample. A sample of 0 means the
/// process is gone and panics.
fn settled(pid: u32, limit: usize) -> Result<usize, Vec<usize>> {
    let deadline = Instant::now() + Duration::from_secs(15);
    let mut samples = Vec::new();
    while Instant::now() < deadline {
        let count = threads(pid);
        assert!(count > 0, "process {pid} is gone: it shows 0 threads");
        if count <= limit {
            return Ok(count);
        }
        samples.push(count);
        thread::sleep(Duration::from_millis(100));
    }
    Err(samples)
}

/// Starts a router in a temp home, gives it 2 s to start every thread it owns (so a count taken
/// before they exist cannot pass) and returns both settled thread counts.
fn idle_threads() -> (Result<usize, Vec<usize>>, Result<usize, Vec<usize>>) {
    let home = TempHome::new();
    let start = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "start"])
        .output()
        .expect("run huddle");
    assert!(start.status.success(), "{start:?}");
    let deadline = Instant::now() + Duration::from_secs(15);
    let pids = loop {
        if let Some(pids) = record(&home).filter(|pids| pids.worker > 0) {
            break pids;
        }
        assert!(Instant::now() < deadline, "the record never named a worker");
        thread::sleep(Duration::from_millis(10));
    };
    thread::sleep(Duration::from_secs(2));
    let counts = (
        settled(pids.worker, WORKER_LIMIT),
        settled(pids.parent, SUPERVISOR_LIMIT),
    );
    let _ = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "stop"])
        .output();
    counts
}

#[test]
fn l11_fresh_worker_has_at_most_10_threads() {
    let (worker, _) = idle_threads();
    if let Err(samples) = worker {
        panic!("no sample of the worker was within {WORKER_LIMIT} threads: {samples:?}");
    }
}

#[test]
fn l11_supervisor_has_at_most_4_threads() {
    let (_, supervisor) = idle_threads();
    if let Err(samples) = supervisor {
        panic!("no sample of the supervisor was within {SUPERVISOR_LIMIT} threads: {samples:?}");
    }
}

/// A blocking thread that finished a read leaves within a second, so over 10 consecutive
/// samples taken 100 ms apart at least 9 show the idle count.
#[test]
fn l44_a_finished_blocking_thread_leaves_within_a_second() {
    let home = TempHome::new();
    let start = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "start"])
        .output()
        .expect("run huddle");
    assert!(start.status.success(), "{start:?}");
    let deadline = Instant::now() + Duration::from_secs(15);
    let pids = loop {
        if let Some(pids) = record(&home).filter(|pids| pids.worker > 0) {
            break pids;
        }
        assert!(Instant::now() < deadline, "the record never named a worker");
        thread::sleep(Duration::from_millis(10));
    };
    thread::sleep(Duration::from_secs(2));
    let settled_until = Instant::now() + Duration::from_secs(15);
    let mut windows = Vec::new();
    let mut passed = false;
    while Instant::now() < settled_until && !passed {
        let window: Vec<usize> = (0..10)
            .map(|_| {
                let count = threads(pids.worker);
                assert!(count > 0, "the worker is gone: it shows 0 threads");
                thread::sleep(Duration::from_millis(100));
                count
            })
            .collect();
        passed = window
            .iter()
            .filter(|count| **count <= WORKER_LIMIT)
            .count()
            >= 9;
        windows.push(window);
    }
    let _ = home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "stop"])
        .output();
    assert!(
        passed,
        "no window had 9 of 10 samples within {WORKER_LIMIT}: {windows:?}"
    );
}
