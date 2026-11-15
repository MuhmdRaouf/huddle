use std::fs;
use std::path::Path;
use std::process::Command;
use std::sync::{Arc, Barrier};
use std::thread;

use huddle_testkit::ports;
use huddle_testkit::temp_home::TempHome;

const FIRST: u16 = 27000;
const LAST: u16 = 27007;

// The pid of a child that has exited and been reaped.
fn dead_pid() -> u32 {
    let mut child = Command::new("true").spawn().unwrap();
    let pid = child.id();
    child.wait().unwrap();
    pid
}

fn write_lease(dir: &Path, port: u16, owner: u32) {
    fs::create_dir_all(dir).unwrap();
    fs::write(dir.join(port.to_string()), format!("{owner}\n")).unwrap();
}

#[test]
fn t9_lease_of_a_dead_process_is_taken_over() {
    let home = TempHome::new();
    let dir = home.root().join("leases");
    let owner = dead_pid();
    for port in FIRST..=LAST {
        write_lease(&dir, port, owner);
    }
    let port = ports::free_in(&dir, FIRST, LAST).unwrap();
    assert!((FIRST..=LAST).contains(&port));
}

#[test]
fn t9_lease_of_a_live_process_is_kept() {
    let home = TempHome::new();
    let dir = home.root().join("leases");
    let owner = std::process::id();
    for port in FIRST..=LAST {
        write_lease(&dir, port, owner);
    }
    assert!(ports::free_in(&dir, FIRST, LAST).is_err());
    for port in FIRST..=LAST {
        let held = fs::read_to_string(dir.join(port.to_string())).unwrap();
        assert_eq!(held.trim(), owner.to_string());
    }
}

#[test]
fn t9_a_lease_taken_records_its_owner() {
    let home = TempHome::new();
    let dir = home.root().join("leases");
    let port = ports::free_in(&dir, FIRST, FIRST).unwrap();
    let held = fs::read_to_string(dir.join(port.to_string())).unwrap();
    assert_eq!(held.trim(), std::process::id().to_string());
}

#[test]
fn t9_two_takers_never_get_the_same_port() {
    let home = TempHome::new();
    let dir = home.root().join("leases");
    let owner = dead_pid();
    for round in 0..200 {
        write_lease(&dir, FIRST, owner);
        let start = Arc::new(Barrier::new(8));
        let takers: Vec<_> = (0..8)
            .map(|_| {
                let (dir, start) = (dir.clone(), start.clone());
                thread::spawn(move || {
                    start.wait();
                    ports::free_in(&dir, FIRST, FIRST).is_ok()
                })
            })
            .collect();
        let winners = takers
            .into_iter()
            .map(|taker| taker.join().unwrap())
            .filter(|won| *won)
            .count();
        assert_eq!(winners, 1, "round {round}");
        let _ = fs::remove_file(dir.join(FIRST.to_string()));
    }
}
