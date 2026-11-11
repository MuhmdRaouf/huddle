mod support;

use huddle_jobs::accept::accept;
use huddle_jobs::checks::{self, Checks};
use huddle_jobs::git;
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use support::{Rig, rig, sample_job, write};

fn ready_job(r: &Rig, id: &str, checks: Checks) -> (JobStore, Job) {
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
    checks::write_checks(&store.job_dir(id), &checks).unwrap();
    (store, job)
}

fn gates(gates: &[&str]) -> Checks {
    Checks {
        gates: gates.iter().map(|s| s.to_string()).collect(),
        ..Default::default()
    }
}

#[test]
fn a_gate_that_passes_in_the_folder_commits_the_change() {
    let r = rig();
    let (store, job) = ready_job(&r, "261109-acv001", gates(&["test -f job.txt"]));
    write(&r.repo, "job.txt", "x\n");
    let out = accept(&store, &job.id).unwrap();
    assert_eq!(out.commit, Some(git::head_sha(&r.repo).unwrap()));
}

#[test]
fn a_gate_that_fails_commits_nothing_and_leaves_the_folder() {
    let r = rig();
    let (store, job) = ready_job(&r, "261109-acv002", gates(&["echo broken; exit 1"]));
    write(&r.repo, "job.txt", "x\n");
    let before = git::head_sha(&r.repo).unwrap();
    let err = accept(&store, &job.id).unwrap_err().to_string();
    assert!(
        err.contains("nothing was accepted") && err.contains("broken"),
        "{err}"
    );
    assert_eq!(git::head_sha(&r.repo).unwrap(), before);
    assert_eq!(store.get(&job.id).unwrap().state, JobState::AwaitingReview);
    assert!(
        r.repo.join("job.txt").exists(),
        "the change stays for a return"
    );
}

#[test]
fn a_scope_or_forbid_finding_does_not_block_the_accept() {
    let r = rig();
    let checks = Checks {
        scope: vec!["src/**".into()],
        forbid: vec!["*.lock".into()],
        ..Default::default()
    };
    let (store, job) = ready_job(&r, "261109-acv003", checks);
    write(&r.repo, "other.txt", "x\n");
    write(&r.repo, "deps.lock", "x\n");
    let out = accept(&store, &job.id).unwrap();
    assert!(out.commit.is_some());
}
