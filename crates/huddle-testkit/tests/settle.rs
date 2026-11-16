use std::process::Child;
use std::time::{Duration, Instant};

use huddle_testkit::settle::wait_gone;
use huddle_testkit::temp_home::TempHome;

/// Spawns the stand-in helper. The kit's PATH puts the system shell first, and macOS hides a
/// platform binary's environment from ps, so the home rides on the command line instead — the
/// same way the guard sees a helper of a run directory whose env is hidden.
fn spawn_helper(home: &TempHome, script: &str) -> Child {
    home.command("sh")
        .args([
            "-c",
            &format!("export HOME={} ; {script}", home.root().display()),
        ])
        .spawn()
        .expect("spawn")
}

#[test]
fn t8_wait_gone_returns_once_the_helper_of_this_home_ends() {
    let home = TempHome::new();
    let mut helper = spawn_helper(&home, "sleep 0.4; : router start --by t8");
    let began = Instant::now();
    assert!(wait_gone(
        home.root(),
        "router start --by",
        Duration::from_secs(10)
    ));
    let waited = began.elapsed();
    assert!(
        waited >= Duration::from_millis(300),
        "it waited for the helper: {waited:?}"
    );
    helper.wait().expect("reap");
}

#[test]
fn t8_wait_gone_answers_false_while_the_helper_still_runs() {
    let home = TempHome::new();
    let mut helper = spawn_helper(&home, "sleep 30; : router start --by t8");
    assert!(!wait_gone(
        home.root(),
        "router start --by",
        Duration::from_millis(200)
    ));
    helper.kill().expect("kill");
    helper.wait().expect("reap");
}

#[test]
fn t8_wait_gone_ignores_a_helper_of_another_home() {
    let home = TempHome::new();
    let other = TempHome::new();
    let mut helper = spawn_helper(&other, "sleep 30; : router start --by t8");
    // The other home's helper outlives the deadline, so only ignoring it answers true.
    assert!(wait_gone(
        home.root(),
        "router start --by",
        Duration::from_secs(5)
    ));
    helper.kill().expect("kill");
    helper.wait().expect("reap");
}
