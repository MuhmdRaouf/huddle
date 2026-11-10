#[path = "../../huddle-jobs/tests/support/mod.rs"]
mod support;

use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::path::Path;
use std::process::Output;
use support::{Rig, git, ready_worktree, rig, sample_job};

fn huddle(r: &Rig, args: &[&str]) -> Output {
    r.home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(&r.repo)
        .output()
        .expect("run huddle")
}

#[test]
fn cancel_removes_the_worktree_and_branch_and_says_so() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = store
        .create(&sample_job(
            &r,
            "261109-can001",
            Access::Write,
            JobState::AwaitingReview,
        ))
        .unwrap();
    ready_worktree(&r, &job);
    let out = huddle(&r, &["cancel", "261109-can001"]);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(
        String::from_utf8_lossy(&out.stdout).trim(),
        "huddle job 261109-can001 cancelled: worktree and branch removed"
    );
    assert!(!Path::new(&job.worktree).exists());
    assert!(git(&r.repo, &["branch", "--list", "huddle/261109-can001"]).is_empty());
    assert_eq!(
        store.get("261109-can001").unwrap().state,
        JobState::Cancelled
    );
}

#[test]
fn cancel_of_a_decided_job_exits_2() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    store
        .create(&sample_job(
            &r,
            "261109-can002",
            Access::Write,
            JobState::Accepted,
        ))
        .unwrap();
    let out = huddle(&r, &["cancel", "261109-can002"]);
    assert_eq!(out.status.code(), Some(2));
}

#[test]
fn cancel_of_an_unknown_job_exits_4() {
    let r = rig();
    let out = huddle(&r, &["cancel", "nope"]);
    assert_eq!(out.status.code(), Some(4));
    assert!(String::from_utf8_lossy(&out.stderr).contains("no job matches"));
}

#[test]
fn cancel_without_an_id_exits_2() {
    let r = rig();
    let out = huddle(&r, &["cancel"]);
    assert_eq!(out.status.code(), Some(2));
}

#[test]
fn cancel_json_prints_the_job() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = store
        .create(&sample_job(
            &r,
            "261109-can003",
            Access::Write,
            JobState::Failed,
        ))
        .unwrap();
    ready_worktree(&r, &job);
    let out = huddle(&r, &["cancel", "261109-can003", "--json"]);
    assert!(out.status.success());
    let parsed: serde_json::Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(parsed["state"], "cancelled");
}
