#[path = "../../huddle-jobs/tests/support/mod.rs"]
mod support;

use huddle_jobs::liveness::{ProcessRecord, driver_record_path, write_record};
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::process::{Command, Output};
use support::{Rig, rig, sample_job};

fn huddle(r: &Rig, args: &[&str]) -> Output {
    r.home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(&r.repo)
        .output()
        .expect("run huddle")
}

fn stdout(out: &Output) -> String {
    String::from_utf8_lossy(&out.stdout).into_owned()
}

fn stderr(out: &Output) -> String {
    String::from_utf8_lossy(&out.stderr).into_owned()
}

#[test]
fn an_empty_repository_says_how_to_start_a_job() {
    let r = rig();
    let out = huddle(&r, &["status"]);
    assert!(out.status.success(), "{}", stderr(&out));
    assert_eq!(
        stdout(&out).trim(),
        "No huddle jobs in this repository yet. /huddle:run <harness> <task> starts one."
    );
}

#[test]
fn a_running_job_whose_driver_died_shows_failed_worker_died() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = sample_job(&r, "261109-dead11", Access::Write, JobState::Running);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "2026-11-09T10:00:00.000Z".into(),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    });
    store.create(&job).unwrap();
    let mut child = Command::new("/usr/bin/true").spawn().unwrap();
    let dead_pid = child.id();
    child.wait().unwrap();
    write_record(
        &driver_record_path(&store.job_dir("261109-dead11")),
        &ProcessRecord {
            pid: dead_pid,
            start_token: "gone".into(),
        },
    )
    .unwrap();

    let out = huddle(&r, &["status"]);
    assert!(out.status.success(), "{}", stderr(&out));
    let text = stdout(&out);
    assert!(text.contains("failed (worker died)"), "{text}");
    assert_eq!(store.get("261109-dead11").unwrap().state, JobState::Failed);
}

#[test]
fn status_of_one_job_by_prefix_prints_the_detail() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    store
        .create(&sample_job(
            &r,
            "261109-abc123",
            Access::Write,
            JobState::AwaitingReview,
        ))
        .unwrap();
    let out = huddle(&r, &["status", "261109-a"]);
    assert!(out.status.success(), "{}", stderr(&out));
    let text = stdout(&out);
    assert!(
        text.starts_with("huddle job 261109-abc123: Do the thing\n"),
        "{text}"
    );
    assert!(
        text.contains("Result: /huddle:result 261109-abc123"),
        "{text}"
    );
}

#[test]
fn status_of_a_job_shows_the_tail_of_its_attempt_log() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = sample_job(&r, "261109-log001", Access::Write, JobState::Running);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "2026-11-09T10:00:00.000Z".into(),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    });
    store.create(&job).unwrap();
    write_record(
        &driver_record_path(&store.job_dir("261109-log001")),
        &ProcessRecord {
            pid: std::process::id(),
            start_token: huddle_procs::live::start_token(std::process::id()).unwrap(),
        },
    )
    .unwrap();
    let log_dir = store.job_dir("261109-log001").join("attempt-1");
    std::fs::create_dir(&log_dir).unwrap();
    std::fs::write(log_dir.join("stdout.log"), "one\ntwo\nthree\nfour\nfive\n").unwrap();
    let text = stdout(&huddle(&r, &["status", "261109-log001"]));
    assert!(
        text.contains("Log (last 4 lines):\n  two\n  three\n  four\n  five\n"),
        "{text}"
    );
}

#[test]
fn status_json_lists_the_jobs() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    store
        .create(&sample_job(
            &r,
            "261109-json01",
            Access::Write,
            JobState::AwaitingReview,
        ))
        .unwrap();
    let out = huddle(&r, &["status", "--json"]);
    assert!(out.status.success());
    let parsed: serde_json::Value = serde_json::from_str(&stdout(&out)).unwrap();
    assert_eq!(parsed[0]["id"], "261109-json01");
    assert_eq!(parsed[0]["state"], "awaiting_review");
}

#[test]
fn an_unknown_job_exits_4_and_names_the_command() {
    let r = rig();
    let out = huddle(&r, &["status", "nope"]);
    assert_eq!(out.status.code(), Some(4));
    assert_eq!(
        stderr(&out).trim(),
        "huddle: no job matches \"nope\" (/huddle:status lists them)"
    );
}

#[test]
fn status_outside_a_repository_exits_1() {
    let r = rig();
    let outside = r.home.root().join("elsewhere");
    std::fs::create_dir(&outside).unwrap();
    let out = r
        .home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .arg("status")
        .current_dir(&outside)
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(
        stderr(&out).contains("not a git repository"),
        "{}",
        stderr(&out)
    );
}
