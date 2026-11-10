mod support;

use huddle_jobs::accept::{AcceptError, accept_no_commit};
use huddle_jobs::git::{self, GitError};
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::path::Path;
use support::{Rig, git as run, ready_worktree, rig, sample_job, write};

fn ready_job(r: &Rig, id: &str) -> (JobStore, Job) {
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = sample_job(r, id, Access::Write, JobState::AwaitingReview);
    let job = store.create(&job).unwrap();
    ready_worktree(r, &job);
    (store, job)
}

fn wt(job: &Job) -> &Path {
    Path::new(&job.worktree)
}

#[test]
fn no_commit_writes_files_into_the_working_tree_only() {
    let r = rig();
    let (store, job) = ready_job(&r, "n1");
    let head = git::head_sha(&r.repo).unwrap();
    write(wt(&job), "a.txt", "changed\n");
    write(wt(&job), "dir/new.txt", "new\n");
    let outcome = accept_no_commit(&store, "n1").unwrap();
    assert_eq!(outcome.commit, None);
    assert_eq!(outcome.job.state, JobState::Accepted);
    assert_eq!(support::read(&r.repo, "a.txt"), "changed\n");
    assert_eq!(support::read(&r.repo, "dir/new.txt"), "new\n");
    assert_eq!(git::head_sha(&r.repo).unwrap(), head, "HEAD does not move");
    let status = run(&r.repo, &["status", "--porcelain"]);
    assert!(
        status.contains("M a.txt") && status.contains("?? dir/"),
        "the change is uncommitted and unstaged: {status}"
    );
    assert!(!wt(&job).exists());
}

#[test]
fn no_commit_removes_a_deleted_file_and_its_empty_directory() {
    let r = rig();
    write(&r.repo, "gone/old.txt", "old\n");
    run(&r.repo, &["add", "-A"]);
    run(&r.repo, &["commit", "-q", "-m", "add old"]);
    let (store, job) = ready_job(&r, "n2");
    std::fs::remove_file(wt(&job).join("gone/old.txt")).unwrap();
    accept_no_commit(&store, "n2").unwrap();
    assert!(!r.repo.join("gone/old.txt").exists());
    assert!(
        !r.repo.join("gone").exists(),
        "the emptied directory goes too"
    );
}

#[test]
fn no_commit_refuses_a_dirty_or_untracked_file_it_would_write() {
    let r = rig();
    let (store, job) = ready_job(&r, "n3");
    write(wt(&job), "a.txt", "from job\n");
    write(&r.repo, "a.txt", "dirty\n");
    let err = accept_no_commit(&store, "n3").unwrap_err();
    assert_eq!(
        err,
        AcceptError::Git(GitError::Dirty {
            paths: vec!["a.txt".into()]
        })
    );
    assert_eq!(support::read(&r.repo, "a.txt"), "dirty\n");
    assert_eq!(store.get("n3").unwrap().state, JobState::AwaitingReview);
}

#[test]
fn no_commit_merges_a_change_the_repo_also_made_elsewhere() {
    let r = rig();
    write(&r.repo, "a.txt", "one\ntwo\nthree\nfour\nfive\nsix\n");
    run(&r.repo, &["add", "-A"]);
    run(&r.repo, &["commit", "-q", "-m", "lines"]);
    let (store, job) = ready_job(&r, "n4");
    write(wt(&job), "a.txt", "ONE\ntwo\nthree\nfour\nfive\nsix\n");
    write(&r.repo, "a.txt", "one\ntwo\nthree\nfour\nfive\nSIX\n");
    run(&r.repo, &["add", "-A"]);
    run(&r.repo, &["commit", "-q", "-m", "main edits the end"]);
    accept_no_commit(&store, "n4").unwrap();
    assert_eq!(
        support::read(&r.repo, "a.txt"),
        "ONE\ntwo\nthree\nfour\nfive\nSIX\n"
    );
}

#[test]
fn no_commit_conflict_reports_the_paths_and_touches_nothing() {
    let r = rig();
    let (store, job) = ready_job(&r, "n5");
    write(wt(&job), "a.txt", "job\n");
    write(&r.repo, "a.txt", "main\n");
    run(&r.repo, &["add", "-A"]);
    run(&r.repo, &["commit", "-q", "-m", "main edits a"]);
    let err = accept_no_commit(&store, "n5").unwrap_err();
    assert_eq!(
        err,
        AcceptError::Git(GitError::Conflict {
            paths: vec!["a.txt".into()]
        })
    );
    assert_eq!(support::read(&r.repo, "a.txt"), "main\n");
    assert_eq!(store.get("n5").unwrap().state, JobState::AwaitingReview);
}

#[test]
fn no_commit_leaves_no_private_index_behind() {
    let r = rig();
    let (store, job) = ready_job(&r, "n6");
    write(wt(&job), "a.txt", "changed\n");
    accept_no_commit(&store, "n6").unwrap();
    let leftovers: Vec<_> = std::fs::read_dir(r.repo.join(".git"))
        .unwrap()
        .filter_map(|e| e.ok())
        .filter(|e| e.file_name().to_string_lossy().starts_with("index.huddle-"))
        .collect();
    assert!(leftovers.is_empty());
}
