mod support;

use huddle_jobs::accept::{AcceptError, accept};
use huddle_jobs::git::{self, GitError};
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use huddle_testkit::script::write_executable;
use support::{Rig, git as run, rig, sample_job, write};

/// A write job awaiting review: stored, its folder the rig's repository.
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
    (store, job)
}

#[test]
fn accept_commits_on_the_current_branch_of_the_folder() {
    let r = rig();
    let (store, _) = ready_job(&r, "j1");
    write(&r.repo, "a.txt", "changed\n");
    let outcome = accept(&store, "j1").unwrap();
    let head = git::head_sha(&r.repo).unwrap();
    assert_eq!(outcome.commit.as_deref(), Some(head.as_str()));
    assert_eq!(
        run(&r.repo, &["log", "-1", "--format=%B"]),
        "Do the thing\n\nRenamed foo"
    );
    assert_eq!(run(&r.repo, &["branch", "--show-current"]), "main");
    assert_eq!(outcome.job.state, JobState::Accepted);
    let decision = outcome.job.decision.as_ref().unwrap();
    assert_eq!(decision.kind, JobState::Accepted);
    assert_eq!(decision.commit.as_deref(), Some(head.as_str()));
    assert_eq!(
        run(&r.repo, &["worktree", "list"]).lines().count(),
        1,
        "no worktree is made or kept"
    );
}

#[test]
fn the_commit_message_carries_no_attribution_trailer() {
    let r = rig();
    let (store, _) = ready_job(&r, "j2");
    write(&r.repo, "a.txt", "changed\n");
    accept(&store, "j2").unwrap();
    let body = run(&r.repo, &["log", "-1", "--format=%B"]);
    assert!(!body.contains("Worked-by"), "{body}");
    assert!(!body.contains("Co-authored"), "{body}");
}

#[test]
fn t3_accept_commits_in_a_linked_worktree_the_session_made() {
    let r = rig();
    let wt = r.home.root().join("session-wt");
    run(
        &r.repo,
        &[
            "worktree",
            "add",
            "-q",
            "-b",
            "session",
            wt.to_str().unwrap(),
        ],
    );
    let wt = wt.canonicalize().unwrap();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = sample_job(&r, "j3", Access::Write, JobState::AwaitingReview);
    job.dir = wt.to_string_lossy().into_owned();
    job.base_branch = Some("session".into());
    store.create(&job).unwrap();
    write(&wt, "from-job.txt", "job\n");
    let outcome = accept(&store, "j3").unwrap();
    assert_eq!(outcome.commit, Some(git::head_sha(&wt).unwrap()));
    assert_eq!(run(&wt, &["log", "-1", "--format=%s"]), "Do the thing");
    assert!(!r.repo.join("from-job.txt").exists());
    assert_eq!(run(&r.repo, &["branch", "--show-current"]), "main");
}

#[test]
fn a_dirty_file_the_job_did_not_name_is_committed_with_the_rest() {
    let r = rig();
    let (store, _) = ready_job(&r, "j5");
    write(&r.repo, "other.txt", "from job\n");
    write(&r.repo, "a.txt", "dirty before the job\n");
    accept(&store, "j5").unwrap();
    assert_eq!(run(&r.repo, &["status", "--porcelain"]), "");
}

#[test]
fn accept_refuses_when_the_user_switched_branches() {
    let r = rig();
    let (store, _) = ready_job(&r, "j7");
    write(&r.repo, "a.txt", "from job\n");
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
    let (store, _) = ready_job(&r, "j10");
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
    write(&r.repo, "a.txt", "changed\n");
    let err = accept(&store, "j10").unwrap_err();
    match err {
        AcceptError::Git(GitError::Failed { command, stderr }) => {
            assert!(command.starts_with("git commit"), "{command}");
            assert!(stderr.contains("lint: 2 problems"), "{stderr}");
        }
        other => panic!("{other:?}"),
    }
    assert_eq!(store.get("j10").unwrap().state, JobState::AwaitingReview);
}

#[test]
fn accept_takes_a_unique_id_prefix() {
    let r = rig();
    let (store, _) = ready_job(&r, "261109-abcdef");
    write(&r.repo, "a.txt", "changed\n");
    let outcome = accept(&store, "261109-a").unwrap();
    assert_eq!(outcome.job.id, "261109-abcdef");
}
