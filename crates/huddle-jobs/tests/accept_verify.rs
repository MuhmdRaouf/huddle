mod support;

use huddle_jobs::accept::accept;
use huddle_jobs::checks::{self, Checks};
use huddle_jobs::git;
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::path::Path;
use support::{Rig, git as run, ready_worktree, rig, sample_job, write};

fn ready_job(r: &Rig, id: &str, gates: &[&str]) -> (JobStore, Job) {
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = sample_job(r, id, Access::Write, JobState::AwaitingReview);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: Some("s".into()),
        started_at: "t".into(),
        ended_at: Some("t".into()),
        outcome: Some(AttemptOutcome::Completed),
        report: None,
        report_text: None,
    });
    let job = store.create(&job).unwrap();
    ready_worktree(r, &job);
    let c = Checks {
        gates: gates.iter().map(|s| s.to_string()).collect(),
        ..Default::default()
    };
    checks::write_checks(&store.job_dir(id), &c).unwrap();
    (store, job)
}

#[test]
fn a_gate_that_passes_on_the_commit_lands_it() {
    let r = rig();
    let (store, job) = ready_job(&r, "261109-acv001", &["test -f job.txt"]);
    write(Path::new(&job.worktree), "job.txt", "x\n");
    let out = accept(&store, &job.id).unwrap();
    assert!(out.commit.is_some());
    assert!(
        !store.job_dir(&job.id).join("verify").exists(),
        "the checkout is removed"
    );
}

#[test]
fn a_gate_that_fails_on_the_commit_lands_nothing() {
    let r = rig();
    let (store, job) = ready_job(&r, "261109-acv002", &["echo broken; exit 1"]);
    write(Path::new(&job.worktree), "job.txt", "x\n");
    let before = git::head_sha(&r.repo).unwrap();
    let err = accept(&store, &job.id).unwrap_err().to_string();
    assert!(
        err.contains("nothing was applied") && err.contains("broken"),
        "{err}"
    );
    assert_eq!(git::head_sha(&r.repo).unwrap(), before);
    assert_eq!(store.get(&job.id).unwrap().state, JobState::AwaitingReview);
    assert!(
        Path::new(&job.worktree).exists(),
        "the worktree stays so the job can be returned"
    );
    assert!(!store.job_dir(&job.id).join("verify").exists());
}

#[test]
fn the_gate_runs_on_the_commit_rebased_onto_a_moved_tip() {
    let r = rig();
    let (store, job) = ready_job(&r, "261109-acv003", &["test ! -f landed.txt"]);
    write(Path::new(&job.worktree), "job.txt", "x\n");
    write(&r.repo, "landed.txt", "moved\n");
    run(&r.repo, &["add", "landed.txt"]);
    run(&r.repo, &["commit", "-m", "tip moved"]);
    let err = accept(&store, &job.id).unwrap_err().to_string();
    assert!(err.contains("nothing was applied"), "{err}");
    assert!(!r.repo.join("job.txt").exists());
}
