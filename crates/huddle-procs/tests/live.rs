use huddle_procs::live::{is_alive, live_executable, process_alive, start_token};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::process::{Child, Command};
use std::time::Duration;

fn sleeper() -> Child {
    Command::new("/bin/sleep").arg("30").spawn().expect("sleep")
}

#[test]
fn a_running_process_is_alive_and_has_a_token() {
    let mut child = sleeper();
    let pid = child.id();
    assert!(is_alive(pid));
    let token = start_token(pid).expect("token");
    assert!(!token.is_empty());
    assert!(process_alive(pid, &token));
    child.kill().expect("kill");
    child.wait().expect("wait");
}

#[test]
fn a_wrong_token_says_the_pid_was_reused() {
    let mut child = sleeper();
    let pid = child.id();
    assert!(!process_alive(pid, "not-a-real-token"));
    assert!(!process_alive(pid, ""));
    child.kill().expect("kill");
    child.wait().expect("wait");
}

#[test]
fn a_dead_process_reads_as_dead() {
    let mut child = sleeper();
    let pid = child.id();
    let token = start_token(pid).expect("token");
    child.kill().expect("kill");
    child.wait().expect("wait");
    assert!(!is_alive(pid));
    assert!(!process_alive(pid, &token));
    assert_eq!(start_token(pid), None);
}

#[test]
fn pid_zero_is_never_alive() {
    assert!(!is_alive(0));
    assert!(!process_alive(0, "x"));
}

#[test]
fn an_exited_unreaped_child_has_no_token() {
    let child = Command::new("/bin/sh")
        .args(["-c", "exit 0"])
        .spawn()
        .expect("spawn");
    let pid = child.id();
    let deadline = std::time::Instant::now() + Duration::from_secs(2);
    let mut gone = false;
    while std::time::Instant::now() < deadline {
        if start_token(pid).is_none() {
            gone = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    assert!(gone, "a zombie reads as no process");
    drop(child);
}

#[test]
fn live_executable_prefers_the_installed_link() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let versioned = root.join("huddle-1.2.3");
    fs::write(&versioned, "#!/bin/sh\n").expect("write");
    fs::set_permissions(&versioned, fs::Permissions::from_mode(0o755)).expect("chmod");
    symlink(&versioned, root.join("huddle")).expect("link");
    assert_eq!(
        fs::canonicalize(live_executable(&root)).expect("real"),
        fs::canonicalize(&versioned).expect("real")
    );
}

#[test]
fn live_executable_falls_back_to_this_binary() {
    let home = TempHome::new();
    let empty = home.root().join("no-link");
    fs::create_dir_all(&empty).expect("dir");
    let picked = live_executable(&empty);
    assert_eq!(
        fs::canonicalize(&picked).ok(),
        fs::canonicalize(std::env::current_exe().expect("exe")).ok()
    );
}
