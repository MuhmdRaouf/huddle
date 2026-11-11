mod support;

use huddle_config::env::Env;
use huddle_harness::table::ExitInfo;
use huddle_jobs::driver::{DriveError, SubmitInput, drive, outcome_of, submit};
use huddle_jobs::git::{self, GitError};
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use huddle_testkit::script::write_executable;
use std::path::Path;
use std::time::{Duration, Instant};
use support::{Rig, env_with, fake_harness_env, rig, write};

const REPORT: &str =
    r#"{"summary":"Renamed foo","files":[{"path":"a.txt","why":"rename"}],"open_items":[]}"#;

fn input(id: &str, access: Access, r: &Rig) -> SubmitInput {
    SubmitInput {
        id: id.into(),
        harness: "opencode".into(),
        task: "Rename foo".into(),
        access,
        effort: None,
        dir: r.repo.clone(),
    }
}

fn hanging_harness_env(r: &Rig) -> Env {
    let path = r.home.root().join("hang-harness");
    write_executable(&path, "#!/bin/sh\nsleep 30\n").unwrap();
    env_with(r, "HUDDLE_OPENCODE_BIN", path.to_str().unwrap())
}

#[test]
fn a_write_job_runs_in_place_and_lands_for_review_with_its_report() {
    let r = rig();
    let head = git::head_sha(&r.repo).unwrap();
    let (store, job) = submit(&r.paths, &input("261109-drv001", Access::Write, &r)).unwrap();
    assert_eq!(job.state, JobState::Queued);
    assert_eq!(job.base_branch.as_deref(), Some("main"));
    assert_eq!(job.base_sha, head);
    assert_eq!(Path::new(&job.dir), r.repo, "the folder is the caller's");
    assert!(!job.dirty_at_start);
    write(&r.repo, "a.txt", "edited\n");

    let env = fake_harness_env(&r, REPORT, 0);
    let job = drive(&store, "261109-drv001", &env).unwrap();
    assert_eq!(job.state, JobState::AwaitingReview);
    let attempt = job.last_attempt().unwrap();
    assert_eq!(attempt.outcome, Some(AttemptOutcome::Completed));
    assert_eq!(attempt.session_id.as_deref(), Some("ses_1"));
    assert_eq!(attempt.report.as_ref().unwrap().summary, "Renamed foo");
    assert_eq!(
        git::head_sha(&r.repo).unwrap(),
        head,
        "nothing is committed"
    );
    assert_eq!(support::read(&r.repo, "a.txt"), "edited\n");
    assert_eq!(
        support::git(&r.repo, &["worktree", "list"]).lines().count(),
        1,
        "no worktree is made"
    );
    let dir = store.job_dir("261109-drv001");
    assert!(!dir.join("driver.toml").exists());
    assert!(!dir.join("harness.toml").exists());
    assert!(!dir.join("stop").exists());
}

#[test]
fn t3_a_job_notes_a_folder_that_had_uncommitted_changes_at_start() {
    let r = rig();
    write(&r.repo, "a.txt", "dirty\n");
    let (_, job) = submit(&r.paths, &input("261109-drv011", Access::Write, &r)).unwrap();
    assert!(job.dirty_at_start);
}

#[test]
fn t3_a_job_in_a_linked_worktree_belongs_to_the_main_repository() {
    let r = rig();
    let wt = r.home.root().join("session-wt");
    support::git(
        &r.repo,
        &["worktree", "add", "-q", "-b", "s", wt.to_str().unwrap()],
    );
    let mut wt_input = input("261109-drv012", Access::Write, &r);
    wt_input.dir = wt.clone();
    let (store, job) = submit(&r.paths, &wt_input).unwrap();
    assert_eq!(Path::new(&job.dir), wt.canonicalize().unwrap());
    assert_eq!(job.base_branch.as_deref(), Some("s"));
    assert_eq!(
        store.root(),
        JobStore::for_repo(&r.paths, &r.repo).root(),
        "the worktree's jobs sit with the main repository's"
    );
}

#[test]
fn t3_a_second_write_job_in_the_same_folder_is_refused_while_one_runs() {
    let r = rig();
    let (store, first) = submit(&r.paths, &input("261109-drv013", Access::Write, &r)).unwrap();
    let err = submit(&r.paths, &input("261109-drv014", Access::Write, &r)).unwrap_err();
    assert_eq!(
        err,
        DriveError::Busy {
            id: first.id.clone(),
            dir: first.dir.clone()
        }
    );
    assert!(store.get("261109-drv014").is_err(), "no record is left");
    submit(&r.paths, &input("261109-drv015", Access::Readonly, &r))
        .expect("a readonly job may share the folder");
    drive(&store, &first.id, &fake_harness_env(&r, REPORT, 0)).unwrap();
    submit(&r.paths, &input("261109-drv016", Access::Write, &r))
        .expect("the folder is free once the first job has landed");
}

#[test]
fn t3_a_write_job_in_another_folder_is_not_refused() {
    let r = rig();
    submit(&r.paths, &input("261109-drv017", Access::Write, &r)).unwrap();
    let wt = r.home.root().join("other-wt");
    support::git(
        &r.repo,
        &["worktree", "add", "-q", "-b", "o", wt.to_str().unwrap()],
    );
    let mut other = input("261109-drv018", Access::Write, &r);
    other.dir = wt;
    submit(&r.paths, &other).unwrap();
}

#[test]
fn a_readonly_job_ends_completed() {
    let r = rig();
    let (store, job) = submit(&r.paths, &input("261109-drv002", Access::Readonly, &r)).unwrap();
    assert!(job.start_tree.is_some());
    let env = fake_harness_env(&r, REPORT, 0);
    let job = drive(&store, "261109-drv002", &env).unwrap();
    assert_eq!(job.state, JobState::Completed);
    assert_eq!(
        job.last_attempt().unwrap().report.as_ref().unwrap().summary,
        "Renamed foo"
    );
}

#[test]
fn t3_a_readonly_job_that_changes_a_file_fails() {
    let r = rig();
    let (store, _) = submit(&r.paths, &input("261109-drv019", Access::Readonly, &r)).unwrap();
    let env = support::fake_harness_env_writing(&r, REPORT, "touched.txt");
    let job = drive(&store, "261109-drv019", &env).unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert_eq!(
        job.failure.as_deref(),
        Some("a readonly job changed files in the folder")
    );
    assert!(
        r.repo.join("touched.txt").exists(),
        "the folder is left as it is"
    );
}

#[test]
fn t3_a_readonly_job_in_a_dirty_folder_passes_when_it_changes_nothing() {
    let r = rig();
    write(&r.repo, "a.txt", "dirty before\n");
    let (store, _) = submit(&r.paths, &input("261109-drv020", Access::Readonly, &r)).unwrap();
    let job = drive(&store, "261109-drv020", &fake_harness_env(&r, REPORT, 0)).unwrap();
    assert_eq!(job.state, JobState::Completed);
}

#[test]
fn a_harness_that_fails_lands_failed_with_its_reason() {
    let r = rig();
    let (store, _job) = submit(&r.paths, &input("261109-drv003", Access::Write, &r)).unwrap();
    let env = fake_harness_env(&r, "no report here", 7);
    let job = drive(&store, "261109-drv003", &env).unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert!(job.failure.is_some());
}

#[test]
fn a_missing_harness_binary_fails_the_job() {
    let r = rig();
    let (store, _job) = submit(&r.paths, &input("261109-drv004", Access::Write, &r)).unwrap();
    let env = env_with(&r, "HUDDLE_OPENCODE_BIN", "/no/such/harness");
    let job = drive(&store, "261109-drv004", &env).unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert!(matches!(
        job.last_attempt().unwrap().outcome,
        Some(AttemptOutcome::Error { .. })
    ));
}

#[test]
fn a_stop_file_left_behind_never_stops_the_next_run() {
    let r = rig();
    let (store, _job) = submit(&r.paths, &input("261109-drv005", Access::Write, &r)).unwrap();
    std::fs::write(store.job_dir("261109-drv005").join("stop"), "").unwrap();
    let env = fake_harness_env(&r, REPORT, 0);
    let job = drive(&store, "261109-drv005", &env).unwrap();
    assert_eq!(job.state, JobState::AwaitingReview);
    assert!(!store.job_dir("261109-drv005").join("stop").exists());
}

#[test]
fn a_stop_during_the_run_cancels_the_job() {
    let r = rig();
    let (store, _job) = submit(&r.paths, &input("261109-drv006", Access::Write, &r)).unwrap();
    let env = hanging_harness_env(&r);
    let paths = r.paths.clone();
    let repo = r.repo.clone();
    let handle = std::thread::spawn(move || {
        let store = JobStore::for_repo(&paths, &repo);
        drive(&store, "261109-drv006", &env)
    });
    let deadline = Instant::now() + Duration::from_secs(10);
    while store.get("261109-drv006").unwrap().state != JobState::Running {
        assert!(Instant::now() < deadline, "the job never started");
        std::thread::sleep(Duration::from_millis(20));
    }
    std::thread::sleep(Duration::from_millis(300));
    std::fs::write(store.job_dir("261109-drv006").join("stop"), "").unwrap();
    let job = handle.join().unwrap().unwrap();
    assert_eq!(job.state, JobState::Cancelled);
    assert_eq!(
        job.last_attempt().unwrap().outcome,
        Some(AttemptOutcome::Stopped)
    );
}

#[test]
fn a_returned_attempt_resumes_its_session() {
    let r = rig();
    let (store, job) = submit(&r.paths, &input("261109-drv007", Access::Write, &r)).unwrap();
    let mut job = job;
    job.state = JobState::Queued;
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::ReviewFix,
        prompt: "fix it".into(),
        feedback: Some("fix it".into()),
        session_id: Some("ses_prior".into()),
        started_at: "t".into(),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    });
    store.update(&job).unwrap();
    let env = fake_harness_env(&r, REPORT, 0);
    let done = drive(&store, "261109-drv007", &env).unwrap();
    assert_eq!(done.state, JobState::AwaitingReview);
    assert_eq!(
        done.attempts.len(),
        1,
        "the pending attempt ran, none was added"
    );
    assert_eq!(done.attempts[0].outcome, Some(AttemptOutcome::Completed));
}

#[test]
fn a_job_not_queued_is_refused() {
    let r = rig();
    let (store, job) = submit(&r.paths, &input("261109-drv008", Access::Write, &r)).unwrap();
    let mut job = job;
    job.state = JobState::Running;
    store.update(&job).unwrap();
    let env = fake_harness_env(&r, REPORT, 0);
    assert_eq!(
        drive(&store, "261109-drv008", &env),
        Err(DriveError::WrongState {
            id: "261109-drv008".into(),
            state: JobState::Running
        })
    );
}

#[test]
fn submit_outside_a_repository_is_refused() {
    let r = rig();
    let outside = r.home.root().join("elsewhere");
    std::fs::create_dir(&outside).unwrap();
    let mut bad = input("261109-drv010", Access::Write, &r);
    bad.dir = outside;
    assert!(matches!(
        submit(&r.paths, &bad),
        Err(DriveError::Git(GitError::NotARepo { .. }))
    ));
}

#[test]
fn the_outcome_comes_from_the_exit_and_not_from_a_late_stop_file() {
    assert_eq!(
        outcome_of(&ExitInfo::exited(0), None),
        AttemptOutcome::Completed
    );
    let mut stopped = ExitInfo::exited(0);
    stopped.forced = Some("stopped".into());
    assert_eq!(outcome_of(&stopped, None), AttemptOutcome::Stopped);
    let mut timed_out = ExitInfo::exited(1);
    timed_out.forced = Some("timeout".into());
    assert_eq!(outcome_of(&timed_out, None), AttemptOutcome::Timeout);
    assert_eq!(
        outcome_of(&ExitInfo::exited(9), Some("the harness exited 9: nope")),
        AttemptOutcome::Error {
            message: "the harness exited 9: nope".into()
        }
    );
    let bare = ExitInfo::exited(9);
    assert!(matches!(
        outcome_of(&bare, None),
        AttemptOutcome::Crashed {
            exit_code: Some(9),
            ..
        }
    ));
}
