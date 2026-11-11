#[path = "../../huddle-jobs/tests/support/mod.rs"]
mod support;

mod e2e_support;
use e2e_support::wait_drivers_done;

use huddle_jobs::git;
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::process::Output;
use support::{Rig, git as run_git, rig, sample_job, write};

fn huddle(r: &Rig, args: &[&str]) -> Output {
    r.home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(&r.repo)
        .env("HUDDLE_OPENCODE_BIN", r.home.root().join("no-such-harness"))
        .output()
        .expect("run huddle")
}

fn out_text(o: &Output) -> String {
    String::from_utf8_lossy(&o.stdout).into_owned()
}

fn err_text(o: &Output) -> String {
    String::from_utf8_lossy(&o.stderr).into_owned()
}

fn ready_job(r: &Rig, id: &str, report: Option<Report>) -> (JobStore, Job) {
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = sample_job(r, id, Access::Write, JobState::AwaitingReview);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: Some("s1".into()),
        started_at: "t".into(),
        ended_at: Some("t".into()),
        outcome: Some(AttemptOutcome::Completed),
        report,
        report_text: None,
    });
    let job = store.create(&job).unwrap();
    (store, job)
}

#[test]
fn result_prints_the_report_changes_and_next_step() {
    let r = rig();
    let (_store, _job) = ready_job(
        &r,
        "261109-res001",
        Some(Report {
            summary: "Renamed foo".into(),
            files: vec![],
            open_items: vec![],
        }),
    );
    write(&r.repo, "a.txt", "edited\n");
    let out = huddle(&r, &["result", "261109-res001"]);
    assert!(out.status.success(), "{}", err_text(&out));
    let text = out_text(&out);
    assert!(
        text.contains("huddle job 261109-res001: Do the thing"),
        "{text}"
    );
    assert!(text.contains("Report\n  Renamed foo"), "{text}");
    assert!(text.contains("Changes (1):\n  M a.txt"), "{text}");
    assert!(text.contains("--accept lands it"), "{text}");
    assert!(
        !text.contains("Diff\n```diff"),
        "no diff without --diff: {text}"
    );
}

#[test]
fn diff_appends_the_fenced_patch() {
    let r = rig();
    let (_store, _job) = ready_job(&r, "261109-res002", None);
    write(&r.repo, "a.txt", "edited\n");
    let text = out_text(&huddle(&r, &["result", "261109-res002", "--diff"]));
    assert!(text.contains("Diff\n```diff"), "{text}");
    assert!(text.contains("+edited"), "{text}");
}

#[test]
fn t3_accept_commits_in_the_folder_and_prints_its_sha() {
    let r = rig();
    let (_store, _job) = ready_job(&r, "261109-res003", None);
    write(&r.repo, "a.txt", "edited\n");
    let out = huddle(&r, &["result", "261109-res003", "--accept"]);
    assert!(out.status.success(), "{}", err_text(&out));
    let head = git::head_sha(&r.repo).unwrap();
    let text = out_text(&out);
    assert_eq!(
        text.trim(),
        format!(
            "huddle job 261109-res003 accepted: commit {} on the current branch of {}",
            &head[..12],
            r.repo.display()
        )
    );
    assert_eq!(support::read(&r.repo, "a.txt"), "edited\n");
    assert_eq!(run_git(&r.repo, &["worktree", "list"]).lines().count(), 1);
}

#[test]
fn accept_of_a_job_with_no_changes_says_so() {
    let r = rig();
    ready_job(&r, "261109-res004", None);
    let out = huddle(&r, &["result", "261109-res004", "--accept"]);
    assert!(out.status.success());
    assert_eq!(
        out_text(&out).trim(),
        "huddle job 261109-res004 accepted: no changes to commit"
    );
}

#[test]
fn accept_no_commit_leaves_the_change_uncommitted() {
    let r = rig();
    let (_store, _job) = ready_job(&r, "261109-res005", None);
    let head = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "a.txt", "edited\n");
    let out = huddle(&r, &["result", "261109-res005", "--accept", "--no-commit"]);
    assert!(out.status.success(), "{}", err_text(&out));
    assert_eq!(
        out_text(&out).trim(),
        format!(
            "huddle job 261109-res005 accepted: the change stays uncommitted in {}",
            r.repo.display()
        )
    );
    assert_eq!(git::head_sha(&r.repo).unwrap(), head);
}

#[test]
fn a_moved_branch_exits_5() {
    let r = rig();
    ready_job(&r, "261109-res006", None);
    run_git(&r.repo, &["checkout", "-q", "-b", "elsewhere"]);
    let out = huddle(&r, &["result", "261109-res006", "--accept"]);
    assert_eq!(out.status.code(), Some(5));
    assert!(err_text(&out).contains("elsewhere"), "{}", err_text(&out));
}

#[test]
fn accept_of_a_failed_job_exits_2() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    store
        .create(&sample_job(
            &r,
            "261109-res008",
            Access::Write,
            JobState::Failed,
        ))
        .unwrap();
    let out = huddle(&r, &["result", "261109-res008", "--accept"]);
    assert_eq!(out.status.code(), Some(2));
    assert!(
        err_text(&out).contains("this only works on a job that awaits review"),
        "{}",
        err_text(&out)
    );
}

#[test]
fn return_starts_a_driver_and_records_the_feedback() {
    let r = rig();
    let (store, _job) = ready_job(&r, "261109-res009", None);
    let out = huddle(
        &r,
        &[
            "result",
            "261109-res009",
            "--return",
            "Also rename the tests.",
        ],
    );
    assert!(out.status.success(), "{}", err_text(&out));
    assert_eq!(
        out_text(&out).trim(),
        "huddle job 261109-res009 returned with feedback: attempt 2 runs detached. /huddle:result 261109-res009 once it awaits review again."
    );
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
    let job = loop {
        let job = store.get("261109-res009").unwrap();
        if !job.state.is_active() {
            break job;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "the driver never ended the job"
        );
        std::thread::sleep(std::time::Duration::from_millis(100));
    };
    assert_eq!(job.attempts.len(), 2);
    assert_eq!(job.attempts[1].kind, AttemptKind::ReviewFix);
    assert_eq!(
        job.attempts[1].feedback.as_deref(),
        Some("Also rename the tests.")
    );
    assert_eq!(
        job.state,
        JobState::Failed,
        "the driver ran and failed because the test harness does not exist"
    );
    wait_drivers_done(r.home.root());
}

#[test]
fn return_without_feedback_exits_2() {
    let r = rig();
    ready_job(&r, "261109-res010", None);
    let out = huddle(&r, &["result", "261109-res010", "--return", "   "]);
    assert_eq!(out.status.code(), Some(2));
    assert_eq!(
        err_text(&out).trim(),
        "huddle: return needs feedback for the harness"
    );
}

#[test]
fn usage_errors_exit_2() {
    let r = rig();
    assert_eq!(huddle(&r, &["result"]).status.code(), Some(2));
    assert_eq!(
        huddle(&r, &["result", "x", "--accept", "--return", "y"])
            .status
            .code(),
        Some(2)
    );
    assert_eq!(
        huddle(&r, &["result", "x", "--no-commit"]).status.code(),
        Some(2)
    );
}

#[test]
fn an_unknown_job_exits_4() {
    let r = rig();
    let out = huddle(&r, &["result", "nope", "--accept"]);
    assert_eq!(out.status.code(), Some(4));
}

#[test]
fn json_prints_the_job() {
    let r = rig();
    ready_job(&r, "261109-res011", None);
    let out = huddle(&r, &["result", "261109-res011", "--json"]);
    let parsed: serde_json::Value = serde_json::from_str(&out_text(&out)).unwrap();
    assert_eq!(parsed["id"], "261109-res011");
}
