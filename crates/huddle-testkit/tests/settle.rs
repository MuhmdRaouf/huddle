use std::time::{Duration, Instant};

use huddle_testkit::settle::wait_gone;
use huddle_testkit::temp_home::TempHome;

#[test]
fn t8_wait_gone_returns_once_the_helper_of_this_home_ends() {
    let home = TempHome::new();
    let mut helper = home
        .command("sh")
        .args(["-c", "sleep 0.4; : router start --by t8"])
        .spawn()
        .expect("spawn");
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
    let mut helper = home
        .command("sh")
        .args(["-c", "sleep 30; : router start --by t8"])
        .spawn()
        .expect("spawn");
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
    let mut helper = other
        .command("sh")
        .args(["-c", "sleep 30; : router start --by t8"])
        .spawn()
        .expect("spawn");
    // The other home's helper outlives the deadline, so only ignoring it answers true.
    assert!(wait_gone(
        home.root(),
        "router start --by",
        Duration::from_secs(5)
    ));
    helper.kill().expect("kill");
    helper.wait().expect("reap");
}
