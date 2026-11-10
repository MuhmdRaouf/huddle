mod support;

use huddle_jobs::accept::{AcceptError, accept};
use huddle_jobs::git::{self, GitError};
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use huddle_testkit::script::write_executable;
use std::path::Path;
use support::{Rig, git as run, ready_worktree, rig, sample_job, write};

/// A write job awaiting review: stored, with its worktree on disk.
fn ready_job(r: &Rig, id: &str) -> (JobStore, Job) {
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
        report: Some(Report {
            summary: "Renamed foo".into(),
            files: vec![],
            open_items: vec![],
        }),
        report_text: None,
    });
    let job = store.create(&job).unwrap();
    ready_worktree(r, &job);
    (store, job)
}

fn wt(job: &Job) -> &Path {
    Path::new(&job.worktree)
}

#[test]
fn accept_commits_on_the_job_branch_and_fast_forwards_the_repository() {
    let r = rig();
    let (store, job) = ready_job(&r, "j1");
    write(wt(&job), "a.txt", "changed\n");
    let outcome = accept(&store, "j1").unwrap();
    let head = git::head_sha(&r.repo).unwrap();
    assert_eq!(outcome.commit.as_deref(), Some(head.as_str()));
    assert_eq!(support::read(&r.repo, "a.txt"), "changed\n");
    assert_eq!(
        run(&r.repo, &["log", "-1", "--format=%B"]),
        "Do the thing\n\nRenamed foo"
    );
    assert_eq!(outcome.job.state, JobState::Accepted);
    let decision = outcome.job.decision.as_ref().unwrap();
    assert_eq!(decision.kind, JobState::Accepted);
    assert_eq!(decision.commit.as_deref(), Some(head.as_str()));
    assert!(!wt(&job).exists(), "the worktree is removed");
    assert!(
        run(&r.repo, &["branch", "--list", "huddle/j1"]).is_empty(),
        "the branch is removed"
    );
    assert!(outcome.cleanup_failed.is_none());
}

#[test]
fn the_commit_message_carries_no_attribution_trailer() {
    let r = rig();
    let (store, job) = ready_job(&r, "j2");
    write(wt(&job), "a.txt", "changed\n");
    accept(&store, "j2").unwrap();
    let body = run(&r.repo, &["log", "-1", "--format=%B"]);
    assert!(!body.contains("Worked-by"), "{body}");
    assert!(!body.contains("Co-authored"), "{body}");
}

#[test]
fn accept_rebases_onto_a_moved_tip() {
    let r = rig();
    let (store, job) = ready_job(&r, "j3");
    write(wt(&job), "new-from-job.txt", "job\n");
    write(&r.repo, "new-from-main.txt", "main\n");
    run(&r.repo, &["add", "-A"]);
    run(&r.repo, &["commit", "-q", "-m", "main moved"]);
    let tip = git::head_sha(&r.repo).unwrap();
    accept(&store, "j3").unwrap();
    assert_eq!(support::read(&r.repo, "new-from-job.txt"), "job\n");
    assert_eq!(support::read(&r.repo, "new-from-main.txt"), "main\n");
    // Two commits the rig seeds ("init", "add a"), then "main moved", then the job's commit.
    assert_eq!(run(&r.repo, &["rev-list", "--count", "HEAD"]), "4");
    assert_eq!(
        run(&r.repo, &["rev-list", "--merges", "--count", "HEAD"]),
        "0",
        "the history stays linear"
    );
    assert_eq!(
        run(&r.repo, &["rev-parse", "HEAD~1"]),
        tip,
        "the job's commit sits on the moved tip"
    );
}

#[test]
fn accept_refuses_a_dirty_overlapping_file_and_changes_nothing() {
    let r = rig();
    let (store, job) = ready_job(&r, "j4");
    write(wt(&job), "a.txt", "from job\n");
    write(&r.repo, "a.txt", "dirty in the checkout\n");
    let err = accept(&store, "j4").unwrap_err();
    assert_eq!(
        err,
        AcceptError::Git(GitError::Dirty {
            paths: vec!["a.txt".into()]
        })
    );
    assert_eq!(support::read(&r.repo, "a.txt"), "dirty in the checkout\n");
    assert_eq!(store.get("j4").unwrap().state, JobState::AwaitingReview);
    assert!(wt(&job).exists(), "the worktree stays for a retry");
}

#[test]
fn a_dirty_file_the_job_did_not_touch_does_not_block_accept() {
    let r = rig();
    let (store, job) = ready_job(&r, "j5");
    write(wt(&job), "other.txt", "from job\n");
    write(&r.repo, "a.txt", "dirty but unrelated\n");
    accept(&store, "j5").unwrap();
    assert_eq!(support::read(&r.repo, "a.txt"), "dirty but unrelated\n");
    assert_eq!(support::read(&r.repo, "other.txt"), "from job\n");
}

#[test]
fn a_rebase_conflict_is_aborted_and_leaves_no_rebase_in_progress() {
    let r = rig();
    let (store, job) = ready_job(&r, "j6");
    write(wt(&job), "a.txt", "job change\n");
    write(&r.repo, "a.txt", "main change\n");
    run(&r.repo, &["add", "-A"]);
    run(&r.repo, &["commit", "-q", "-m", "main edits a.txt"]);
    let main_head = git::head_sha(&r.repo).unwrap();
    let err = accept(&store, "j6").unwrap_err();
    assert_eq!(
        err,
        AcceptError::Git(GitError::Conflict {
            paths: vec!["a.txt".into()]
        })
    );
    assert_eq!(
        git::head_sha(&r.repo).unwrap(),
        main_head,
        "the repository head is unchanged"
    );
    let wt_path = wt(&job);
    let rebase_merge = run(wt_path, &["rev-parse", "--git-path", "rebase-merge"]);
    assert!(
        !wt_path.join(&rebase_merge).exists() && !Path::new(&rebase_merge).exists(),
        "no rebase left in progress"
    );
    assert_eq!(store.get("j6").unwrap().state, JobState::AwaitingReview);
}

#[test]
fn accept_refuses_when_the_user_switched_branches() {
    let r = rig();
    let (store, job) = ready_job(&r, "j7");
    write(wt(&job), "a.txt", "from job\n");
    run(&r.repo, &["checkout", "-q", "-b", "other"]);
    let err = accept(&store, "j7").unwrap_err();
    assert_eq!(
        err,
        AcceptError::Git(GitError::WrongBranch {
            expected: "main".into(),
            actual: "other".into()
        })
    );
    assert_eq!(store.get("j7").unwrap().state, JobState::AwaitingReview);
}

#[test]
fn accept_of_a_job_that_changed_nothing_records_no_commit() {
    let r = rig();
    let (store, _job) = ready_job(&r, "j8");
    let head = git::head_sha(&r.repo).unwrap();
    let outcome = accept(&store, "j8").unwrap();
    assert_eq!(outcome.commit, None);
    assert_eq!(git::head_sha(&r.repo).unwrap(), head);
    assert_eq!(outcome.job.state, JobState::Accepted);
    assert_eq!(outcome.job.decision.unwrap().commit, None);
}

#[test]
fn accept_only_works_on_a_job_awaiting_review() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = sample_job(&r, "j9", Access::Write, JobState::Running);
    store.create(&job).unwrap();
    let err = accept(&store, "j9").unwrap_err();
    assert_eq!(
        err,
        AcceptError::WrongState {
            id: "j9".into(),
            state: JobState::Running
        }
    );
    assert_eq!(
        err.to_string(),
        "job j9 is running; this only works on a job that awaits review"
    );
}

#[test]
fn a_commit_hook_failure_leaves_the_job_awaiting_review() {
    let r = rig();
    let (store, job) = ready_job(&r, "j10");
    let hooks = r.home.root().join("hooks");
    std::fs::create_dir_all(&hooks).unwrap();
    write_executable(
        &hooks.join("pre-commit"),
        "#!/bin/sh\necho 'lint: 2 problems' >&2\nexit 1\n",
    )
    .unwrap();
    run(
        &r.repo,
        &["config", "core.hooksPath", hooks.to_str().unwrap()],
    );
    write(wt(&job), "a.txt", "changed\n");
    let err = accept(&store, "j10").unwrap_err();
    match err {
        AcceptError::Git(GitError::Failed { command, stderr }) => {
            assert!(command.starts_with("git commit"), "{command}");
            assert!(stderr.contains("lint: 2 problems"), "{stderr}");
        }
        other => panic!("{other:?}"),
    }
    assert_eq!(store.get("j10").unwrap().state, JobState::AwaitingReview);
    assert!(wt(&job).exists());
}

#[test]
fn accept_settles_when_an_earlier_accept_already_landed_the_commit() {
    let r = rig();
    let (store, job) = ready_job(&r, "j11");
    write(wt(&job), "a.txt", "changed\n");
    let sha = git::commit_all(wt(&job), "Do the thing\n").unwrap();
    run(&r.repo, &["merge", "--quiet", "--ff-only", &sha]);
    let outcome = accept(&store, "j11").unwrap();
    assert_eq!(outcome.commit.as_deref(), Some(sha.as_str()));
    assert_eq!(outcome.job.state, JobState::Accepted);
}

#[test]
fn accept_takes_a_unique_id_prefix() {
    let r = rig();
    let (store, job) = ready_job(&r, "261109-abcdef");
    write(wt(&job), "a.txt", "changed\n");
    let outcome = accept(&store, "261109-a").unwrap();
    assert_eq!(outcome.job.id, "261109-abcdef");
}
