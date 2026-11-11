mod support;

use huddle_jobs::accept::accept_no_commit;
use huddle_jobs::git;
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use support::{Rig, git as run, rig, sample_job, write};

fn ready_job(r: &Rig, id: &str) -> (JobStore, Job) {
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = sample_job(r, id, Access::Write, JobState::AwaitingReview);
    let job = store.create(&job).unwrap();
    (store, job)
}

#[test]
fn no_commit_leaves_the_change_in_the_folder_uncommitted() {
    let r = rig();
    let (store, _) = ready_job(&r, "n1");
    let head = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "a.txt", "changed\n");
    write(&r.repo, "dir/new.txt", "new\n");
    let outcome = accept_no_commit(&store, "n1").unwrap();
    assert_eq!(outcome.commit, None);
    assert_eq!(outcome.job.state, JobState::Accepted);
    assert_eq!(support::read(&r.repo, "a.txt"), "changed\n");
    assert_eq!(git::head_sha(&r.repo).unwrap(), head, "HEAD does not move");
    let status = run(&r.repo, &["status", "--porcelain"]);
    assert!(
        status.contains("M a.txt") && status.contains("?? dir/"),
        "the change is uncommitted and unstaged: {status}"
    );
}

#[test]
fn no_commit_refuses_when_another_branch_is_checked_out() {
    let r = rig();
    let (store, _) = ready_job(&r, "n2");
    run(&r.repo, &["checkout", "-q", "-b", "elsewhere"]);
    let err = accept_no_commit(&store, "n2").unwrap_err().to_string();
    assert!(err.contains("elsewhere"), "{err}");
    assert_eq!(store.get("n2").unwrap().state, JobState::AwaitingReview);
}
