mod support;

use huddle_jobs::driver;
use huddle_jobs::model::*;
use huddle_jobs::return_job::{ReturnError, return_job};
use huddle_jobs::store::JobStore;
use support::{Rig, rig, sample_job, write};

fn store_for(r: &Rig) -> JobStore {
    JobStore::for_repo(&r.paths, &r.repo)
}

fn reviewable(r: &Rig, id: &str, session: Option<&str>) -> (JobStore, Job) {
    let store = store_for(r);
    let mut job = sample_job(r, id, Access::Write, JobState::AwaitingReview);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: session.map(String::from),
        started_at: "t".into(),
        ended_at: Some("t".into()),
        outcome: Some(AttemptOutcome::Completed),
        report: None,
        report_text: None,
    });
    let job = store.create(&job).unwrap();
    (store, job)
}

#[test]
fn return_queues_a_review_fix_attempt_on_the_same_session() {
    let r = rig();
    let (store, _job) = reviewable(&r, "r1", Some("sess-1"));
    let job = return_job(&store, "r1", "  Also rename the tests.  ").unwrap();
    assert_eq!(job.state, JobState::Queued);
    assert_eq!(job.attempts.len(), 2);
    let attempt = &job.attempts[1];
    assert_eq!(attempt.n, 2);
    assert_eq!(attempt.kind, AttemptKind::ReviewFix);
    assert_eq!(attempt.feedback.as_deref(), Some("Also rename the tests."));
    assert_eq!(attempt.session_id.as_deref(), Some("sess-1"));
    assert!(
        attempt.prompt.contains("Also rename the tests."),
        "{}",
        attempt.prompt
    );
    assert_eq!(attempt.outcome, None);
    assert_eq!(job.pending_attempt(), Some(1));
}

#[test]
fn return_refuses_blank_feedback() {
    let r = rig();
    let (store, _job) = reviewable(&r, "r2", None);
    assert_eq!(
        return_job(&store, "r2", "   \n"),
        Err(ReturnError::EmptyFeedback)
    );
    assert_eq!(
        ReturnError::EmptyFeedback.to_string(),
        "return needs feedback for the harness"
    );
    assert_eq!(store.get("r2").unwrap().attempts.len(), 1);
}

#[test]
fn return_only_works_on_a_job_awaiting_review_or_failed() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "r3", Access::Write, JobState::Running))
        .unwrap();
    let err = return_job(&store, "r3", "feedback").unwrap_err();
    assert_eq!(
        err,
        ReturnError::WrongState {
            id: "r3".into(),
            state: JobState::Running
        }
    );
    assert_eq!(
        err.to_string(),
        "job r3 is running; this only works on a job that awaits review or has failed"
    );
}

#[test]
fn a_failed_write_job_can_be_returned() {
    let r = rig();
    let store = store_for(&r);
    let mut job = sample_job(&r, "r4", Access::Write, JobState::Failed);
    job.failure = Some("worker died".into());
    store.create(&job).unwrap();
    let job = return_job(&store, "r4", "try again").unwrap();
    assert_eq!(job.state, JobState::Queued);
    assert_eq!(job.failure, None);
}

#[test]
fn a_finished_readonly_job_cannot_be_returned() {
    let r = rig();
    let store = store_for(&r);
    let job = sample_job(&r, "r5", Access::Readonly, JobState::Failed);
    store.create(&job).unwrap();
    let err = return_job(&store, "r5", "again").unwrap_err();
    assert_eq!(err, ReturnError::NotWrite { id: "r5".into() });
}

#[test]
fn return_clears_a_stale_stop_file_before_queuing() {
    let r = rig();
    let (store, _job) = reviewable(&r, "r6", None);
    let stop = store.job_dir("r6").join("stop");
    std::fs::write(&stop, "").unwrap();
    return_job(&store, "r6", "go on").unwrap();
    assert!(!stop.exists());
}

#[test]
fn a_returned_job_reruns_in_the_same_folder_with_its_files() {
    let r = rig();
    let (store, _job) = reviewable(&r, "r7", None);
    write(&r.repo, "kept.txt", "from attempt one\n");
    return_job(&store, "r7", "continue").unwrap();
    // drive runs the pending review_fix attempt against the fake harness set up in support.
    let harness =
        support::fake_harness_env(&r, r#"{"summary":"more","files":[],"open_items":[]}"#, 0);
    let job = driver::drive(&store, "r7", &harness).unwrap();
    assert_eq!(job.state, JobState::AwaitingReview);
    assert_eq!(job.attempts.len(), 2);
    assert_eq!(job.attempts[1].kind, AttemptKind::ReviewFix);
    assert_eq!(job.attempts[1].outcome, Some(AttemptOutcome::Completed));
    assert_eq!(job.attempts[1].report.as_ref().unwrap().summary, "more");
    assert_eq!(support::read(&r.repo, "kept.txt"), "from attempt one\n");
}
