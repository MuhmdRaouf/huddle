#[path = "../../huddle-jobs/tests/support/mod.rs"]
mod support;

mod e2e_support;
use e2e_support::wait_drivers_done;

use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use huddle_testkit::fake_harness::{FakeHarness, Mode};
use std::process::{Output, Stdio};
use std::time::{Duration, Instant};
use support::{Rig, rig};

const REPORT: &str = r#"{"summary":"Renamed foo","files":[],"open_items":[]}"#;

fn fake(r: &Rig, text: &str, exit: i32) -> String {
    FakeHarness::install(
        &r.home,
        "opencode",
        Mode::Jsonl {
            session: "ses_1".into(),
            text: text.into(),
        },
        exit,
        "boom\n",
    )
    .unwrap()
    .path()
    .to_string_lossy()
    .into_owned()
}

fn huddle(r: &Rig, bin: &str, args: &[&str]) -> Output {
    r.home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(&r.repo)
        .env("HUDDLE_OPENCODE_BIN", bin)
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
fn run_waits_and_prints_the_report_for_review() {
    let r = rig();
    let bin = fake(&r, REPORT, 0);
    let out = huddle(&r, &bin, &["run", "opencode", "Rename", "foo"]);
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(
        out.status.success(),
        "{text}\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let id = job_id(&text);
    assert!(
        text.starts_with(&format!(
            "huddle job {id} started: Rename foo (opencode, write)"
        )),
        "{text}"
    );
    assert!(text.contains("Report\n  Renamed foo"), "{text}");
    let store = JobStore::for_repo(&r.paths, &r.repo);
    assert_eq!(store.get(&id).unwrap().state, JobState::AwaitingReview);
    wait_drivers_done(r.home.root());
}

#[test]
fn background_returns_at_once_and_the_job_still_lands() {
    let r = rig();
    let bin = fake(&r, REPORT, 0);
    let out = huddle(&r, &bin, &["run", "opencode", "--background", "Do it"]);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let text = String::from_utf8_lossy(&out.stdout);
    let id = job_id(&text);
    assert!(
        text.contains(&format!(
            "Running detached: /huddle:status shows it, /huddle:result {id} once it awaits review."
        )),
        "{text}"
    );
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let deadline = Instant::now() + Duration::from_secs(20);
    while store.get(&id).unwrap().state.is_active() {
        assert!(Instant::now() < deadline, "the driver never finished");
        std::thread::sleep(Duration::from_millis(100));
    }
    assert_eq!(store.get(&id).unwrap().state, JobState::AwaitingReview);
    wait_drivers_done(r.home.root());
}

#[test]
fn a_readonly_run_completes() {
    let r = rig();
    let bin = fake(&r, REPORT, 0);
    let out = huddle(&r, &bin, &["run", "opencode", "--readonly", "Audit", "it"]);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stdout)
    );
    let text = String::from_utf8_lossy(&out.stdout);
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = store.get(&job_id(&text)).unwrap();
    assert_eq!(job.state, JobState::Completed);
    assert_eq!(job.access, Access::Readonly);
    wait_drivers_done(r.home.root());
}

#[test]
fn a_failed_harness_exits_3() {
    let r = rig();
    let bin = fake(&r, "no report", 5);
    let out = huddle(&r, &bin, &["run", "opencode", "task"]);
    assert_eq!(
        out.status.code(),
        Some(3),
        "{}",
        String::from_utf8_lossy(&out.stdout)
    );
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("failed"), "{text}");
    wait_drivers_done(r.home.root());
}

#[test]
fn an_unknown_harness_exits_2() {
    let r = rig();
    let out = huddle(&r, "/no/such", &["run", "nonesuch", "task"]);
    assert_eq!(out.status.code(), Some(2));
    assert_eq!(
        String::from_utf8_lossy(&out.stderr).trim(),
        "huddle: no harness named \"nonesuch\": /huddle:setup lists them"
    );
}

#[test]
fn a_harness_that_is_not_ready_exits_6_and_says_what_to_do() {
    let r = rig();
    let out = huddle(&r, "/no/such/opencode", &["run", "opencode", "task"]);
    assert_eq!(out.status.code(), Some(6));
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(
        err.contains("opencode is not ready") && err.contains("/huddle:setup opencode"),
        "{err}"
    );
}

#[test]
fn run_outside_a_repository_exits_1() {
    let r = rig();
    let bin = fake(&r, REPORT, 0);
    let outside = r.home.root().join("elsewhere");
    std::fs::create_dir(&outside).unwrap();
    let out = r
        .home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["run", "opencode", "task"])
        .current_dir(&outside)
        .env("HUDDLE_OPENCODE_BIN", &bin)
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("not a git repository"));
}

#[test]
fn usage_errors_exit_2() {
    let r = rig();
    let bin = fake(&r, REPORT, 0);
    for args in [
        vec!["run"],
        vec!["run", "opencode"],
        vec!["run", "opencode", "--readonly", "--write", "x"],
        vec!["run", "opencode", "--bogus", "x"],
    ] {
        assert_eq!(huddle(&r, &bin, &args).status.code(), Some(2), "{args:?}");
    }
    let unknown = huddle(&r, &bin, &["run", "opencode", "--bogus", "x"]);
    assert!(String::from_utf8_lossy(&unknown.stderr).contains("Unknown option '--bogus'"));
    let missing = huddle(&r, &bin, &["run", "opencode", "--effort"]);
    assert!(
        String::from_utf8_lossy(&missing.stderr)
            .contains("Option '--effort <value>' argument missing")
    );
}

#[test]
fn a_closed_stdout_does_not_kill_the_run() {
    let r = rig();
    let bin = fake(&r, REPORT, 0);
    let mut child = r
        .home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["run", "opencode", "--background", "task"])
        .current_dir(&r.repo)
        .env("HUDDLE_OPENCODE_BIN", &bin)
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    drop(child.stdout.take());
    assert!(child.wait().unwrap().success());
    wait_drivers_done(r.home.root());
}

#[test]
fn t3_a_readonly_run_that_changes_a_file_fails_with_exit_3() {
    let r = rig();
    let env = support::fake_harness_env_writing(&r, REPORT, "touched.txt");
    let bin = env.get("HUDDLE_OPENCODE_BIN").expect("bin").to_string();
    let out = huddle(&r, &bin, &["run", "opencode", "--readonly", "Audit", "it"]);
    assert_eq!(
        out.status.code(),
        Some(3),
        "{}",
        String::from_utf8_lossy(&out.stdout)
    );
    let text = String::from_utf8_lossy(&out.stdout);
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = store.get(&job_id(&text)).unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert_eq!(
        job.failure.as_deref(),
        Some("a readonly job changed files in the folder")
    );
    assert!(
        r.repo.join("touched.txt").exists(),
        "the folder is left as it is"
    );
    wait_drivers_done(r.home.root());
}
