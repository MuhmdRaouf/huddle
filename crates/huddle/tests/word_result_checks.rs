#[path = "../../huddle-jobs/tests/support/mod.rs"]
mod support;

mod e2e_support;
use e2e_support::wait_drivers_done;

use huddle_jobs::checks;
use huddle_jobs::store::JobStore;
use huddle_testkit::fake_harness::{FakeHarness, Mode};
use std::process::Output;
use support::{Rig, rig};

const REPORT: &str = r#"{"summary":"Done","files":[],"open_items":[]}"#;

fn huddle(r: &Rig, args: &[&str]) -> Output {
    let fake = FakeHarness::install(
        &r.home,
        "opencode",
        Mode::Jsonl {
            session: "ses_1".into(),
            text: REPORT.into(),
        },
        0,
        "",
    )
    .unwrap();
    r.home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(&r.repo)
        .env("HUDDLE_OPENCODE_BIN", fake.path())
        .output()
        .expect("run huddle")
}

fn job_id(out: &Output) -> String {
    let text = String::from_utf8_lossy(&out.stdout);
    let first = text.lines().next().unwrap_or_default();
    first
        .strip_prefix("huddle job ")
        .and_then(|rest| rest.split(' ').next())
        .unwrap_or_else(|| panic!("no id in {first}"))
        .to_string()
}

#[test]
fn result_shows_the_verdict_of_the_last_attempt() {
    let r = rig();
    let id = job_id(&huddle(
        &r,
        &["run", "opencode", "--gate", "exit 5", "Do", "it"],
    ));
    let out = huddle(&r, &["result", &id]);
    let text = String::from_utf8_lossy(&out.stdout);
    assert_eq!(out.status.code(), Some(0), "{text}");
    assert!(text.contains("Verdict: gate_fail"), "{text}");
    wait_drivers_done(r.home.root());
}

#[test]
fn a_job_with_no_checks_shows_no_verdict() {
    let r = rig();
    let id = job_id(&huddle(&r, &["run", "opencode", "Do", "it"]));
    assert!(!String::from_utf8_lossy(&huddle(&r, &["result", &id]).stdout).contains("Verdict:"));
    wait_drivers_done(r.home.root());
}

#[test]
fn a_corrupt_verification_is_an_error_not_a_clean_result() {
    let r = rig();
    let id = job_id(&huddle(
        &r,
        &["run", "opencode", "--gate", "exit 0", "Do", "it"],
    ));
    let store = JobStore::for_repo(&r.paths, &r.repo);
    std::fs::write(checks::verification_path(&store.job_dir(&id), 1), "{").unwrap();
    let out = huddle(&r, &["result", &id]);
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("the checks could not be read"));
    wait_drivers_done(r.home.root());
}
