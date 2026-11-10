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

fn job_id(stdout: &str) -> String {
    let first = stdout.lines().next().unwrap_or_default();
    first
        .strip_prefix("huddle job ")
        .and_then(|rest| rest.split(' ').next())
        .unwrap_or_else(|| panic!("no id in {first}"))
        .to_string()
}

#[test]
fn a_waiting_run_prints_the_checks_after_the_report() {
    let r = rig();
    let out = huddle(
        &r,
        &[
            "run", "opencode", "--gate", "exit 0", "--gate", "exit 5", "Do", "it",
        ],
    );
    let text = String::from_utf8_lossy(&out.stdout);
    assert_eq!(out.status.code(), Some(0), "{text}");
    assert!(text.contains("Verdict: gate_fail"), "{text}");
    assert!(text.contains("Gates (1/2 passed)"), "{text}");
    wait_drivers_done(r.home.root());
}

#[test]
fn the_flags_reach_the_checks_file_in_order() {
    let r = rig();
    let out = huddle(
        &r,
        &[
            "run",
            "opencode",
            "--setup",
            "exit 0",
            "--scope",
            "src/**",
            "--scope",
            "docs/**",
            "--forbid",
            ".github/**",
            "Do",
            "it",
        ],
    );
    let text = String::from_utf8_lossy(&out.stdout);
    let id = job_id(&text);
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let c = checks::read_checks(&store.job_dir(&id)).unwrap();
    assert_eq!(c.setup, vec!["exit 0"]);
    assert_eq!(c.scope, vec!["src/**", "docs/**"]);
    assert_eq!(c.forbid, vec![".github/**"]);
    assert!(c.gates.is_empty());
    wait_drivers_done(r.home.root());
}

#[test]
fn a_run_with_no_checks_prints_no_verdict() {
    let r = rig();
    let out = huddle(&r, &["run", "opencode", "Do", "it"]);
    assert!(!String::from_utf8_lossy(&out.stdout).contains("Verdict:"));
    wait_drivers_done(r.home.root());
}
