mod support;

use huddle_jobs::cancel::{CANCEL_OPTIONS, CancelError, CancelOptions, cancel};
use huddle_jobs::liveness::{ProcessRecord, driver_record_path, harness_record_path, write_record};
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::os::unix::process::CommandExt;
use std::process::{Child, Command};
use std::time::Duration;
use support::{Rig, rig, sample_job, write};

const FAST: CancelOptions = CancelOptions {
    stop_wait: Duration::from_millis(300),
    grace: Duration::from_millis(100),
};

fn store_for(r: &Rig) -> JobStore {
    JobStore::for_repo(&r.paths, &r.repo)
}

fn group_leader_sleep() -> Child {
    Command::new("/bin/sleep")
        .arg("60")
        .process_group(0)
        .spawn()
        .expect("spawn")
}

fn record_of(child: &Child) -> ProcessRecord {
    let pid = child.id();
    ProcessRecord {
        pid,
        start_token: huddle_procs::live::start_token(pid).expect("token"),
    }
}

#[test]
fn t3_cancel_of_a_queued_job_with_no_driver_cancels_it_and_leaves_the_folder() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "c1", Access::Write, JobState::Queued))
        .unwrap();
    write(&r.repo, "a.txt", "edited by the job\n");
    write(&r.repo, "new.txt", "new\n");
    let outcome = cancel(&store, "c1", &FAST).unwrap();
    assert_eq!(outcome.job.state, JobState::Cancelled);
    assert_eq!(
        outcome.job.decision.as_ref().unwrap().kind,
        JobState::Cancelled
    );
    assert_eq!(support::read(&r.repo, "a.txt"), "edited by the job\n");
    assert_eq!(support::read(&r.repo, "new.txt"), "new\n");
    assert!(!outcome.forced_driver);
}

#[test]
fn cancel_of_a_job_awaiting_review_keeps_the_folders_changes() {
    let r = rig();
    let store = store_for(&r);
    let mut job = sample_job(&r, "c2", Access::Write, JobState::AwaitingReview);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "t".into(),
        ended_at: Some("t".into()),
        outcome: Some(AttemptOutcome::Completed),
        report: None,
        report_text: None,
    });
    store.create(&job).unwrap();
    write(&r.repo, "a.txt", "kept\n");
    let outcome = cancel(&store, "c2", &FAST).unwrap();
    assert_eq!(outcome.job.state, JobState::Cancelled);
    assert_eq!(support::read(&r.repo, "a.txt"), "kept\n");
}

#[test]
fn cancel_refuses_a_job_already_decided() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "c3", Access::Write, JobState::Accepted))
        .unwrap();
    let err = cancel(&store, "c3", &FAST).unwrap_err();
    assert_eq!(
        err,
        CancelError::WrongState {
            id: "c3".into(),
            state: JobState::Accepted
        }
    );
}

#[test]
fn cancel_closes_the_open_attempt() {
    let r = rig();
    let store = store_for(&r);
    let mut job = sample_job(&r, "c4", Access::Write, JobState::Running);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "t".into(),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    });
    store.create(&job).unwrap();
    let outcome = cancel(&store, "c4", &FAST).unwrap();
    assert_eq!(
        outcome.job.last_attempt().unwrap().outcome,
        Some(AttemptOutcome::Stopped)
    );
    assert!(outcome.job.last_attempt().unwrap().ended_at.is_some());
}

#[test]
fn a_live_driver_gets_the_stop_file_and_is_terminated_when_it_will_not_stop() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "c5", Access::Write, JobState::Running))
        .unwrap();
    let dir = store.job_dir("c5");
    let mut driver = group_leader_sleep();
    write_record(&driver_record_path(&dir), &record_of(&driver)).unwrap();
    let outcome = cancel(&store, "c5", &FAST).unwrap();
    assert!(outcome.forced_driver);
    assert_eq!(outcome.job.state, JobState::Cancelled);
    assert!(
        !driver.wait().unwrap().success(),
        "the stubborn driver was terminated"
    );
    assert!(!dir.join("stop").exists());
}

#[test]
fn a_driver_record_with_a_reused_pid_is_never_signalled() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "c6", Access::Write, JobState::Running))
        .unwrap();
    let dir = store.job_dir("c6");
    let mut stranger = group_leader_sleep();
    write_record(
        &driver_record_path(&dir),
        &ProcessRecord {
            pid: stranger.id(),
            start_token: "not-this-process".into(),
        },
    )
    .unwrap();
    let outcome = cancel(&store, "c6", &FAST).unwrap();
    assert!(!outcome.forced_driver);
    assert!(
        stranger.try_wait().unwrap().is_none(),
        "a process with a reused pid must not be signalled"
    );
    assert_eq!(outcome.job.state, JobState::Cancelled);
    stranger.kill().unwrap();
    stranger.wait().unwrap();
}

#[test]
fn cancel_terminates_a_harness_left_by_a_dead_driver() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "c7", Access::Write, JobState::Running))
        .unwrap();
    let dir = store.job_dir("c7");
    let mut harness = group_leader_sleep();
    write_record(&harness_record_path(&dir), &record_of(&harness)).unwrap();
    cancel(&store, "c7", &FAST).unwrap();
    assert!(
        !harness.wait().unwrap().success(),
        "the harness group was terminated"
    );
    assert!(!harness_record_path(&dir).exists());
}

#[test]
fn the_production_timings_are_the_decided_ones() {
    assert_eq!(CANCEL_OPTIONS.stop_wait, Duration::from_secs(25));
    assert_eq!(CANCEL_OPTIONS.grace, Duration::from_secs(10));
}
