mod support;

use huddle_jobs::checks::{self, Checks, Verdict};
use huddle_jobs::driver::{SubmitInput, drive, submit};
use huddle_jobs::model::*;
use std::path::Path;
use support::{Rig, fake_harness_env, rig, write};

const REPORT: &str = r#"{"summary":"Done","files":[],"open_items":[]}"#;

fn start(r: &Rig, id: &str, access: Access, c: &Checks) -> (huddle_jobs::store::JobStore, Job) {
    let input = SubmitInput {
        id: id.into(),
        harness: "opencode".into(),
        task: "Do it".into(),
        access,
        effort: None,
        cwd: r.repo.clone(),
    };
    let (store, job) = submit(&r.paths, &input).unwrap();
    checks::write_checks(&store.job_dir(id), c).unwrap();
    (store, job)
}

fn gates(items: &[&str]) -> Checks {
    Checks {
        gates: items.iter().map(|s| s.to_string()).collect(),
        ..Default::default()
    }
}

#[test]
fn a_failing_gate_is_recorded_and_the_job_still_lands_for_review() {
    let r = rig();
    let (store, job) = start(&r, "261109-chk001", Access::Write, &gates(&["exit 4"]));
    write(Path::new(&job.worktree), "a.txt", "edited\n");
    let job = drive(&store, "261109-chk001", &fake_harness_env(&r, REPORT, 0)).unwrap();
    assert_eq!(job.state, JobState::AwaitingReview);
    let v = checks::read_verification(&store.job_dir(&job.id), 1)
        .unwrap()
        .unwrap();
    assert_eq!(v.verdict, Verdict::GateFail);
    assert_eq!(v.gates[0].exit_code, Some(4));
}

#[test]
fn a_job_with_no_checks_writes_no_verification() {
    let r = rig();
    let (store, job) = start(&r, "261109-chk002", Access::Write, &Checks::default());
    let job = drive(&store, &job.id, &fake_harness_env(&r, REPORT, 0)).unwrap();
    assert_eq!(job.state, JobState::AwaitingReview);
    assert_eq!(
        checks::read_verification(&store.job_dir(&job.id), 1).unwrap(),
        None
    );
}

#[test]
fn a_harness_that_failed_is_not_verified() {
    let r = rig();
    let (store, job) = start(&r, "261109-chk003", Access::Write, &gates(&["exit 0"]));
    let job = drive(&store, &job.id, &fake_harness_env(&r, "no report", 7)).unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert_eq!(
        checks::read_verification(&store.job_dir(&job.id), 1).unwrap(),
        None
    );
}

#[test]
fn a_corrupt_checks_file_fails_the_job() {
    let r = rig();
    let (store, job) = start(&r, "261109-chk004", Access::Write, &Checks::default());
    std::fs::write(checks::checks_path(&store.job_dir(&job.id)), "{").unwrap();
    let job = drive(&store, &job.id, &fake_harness_env(&r, REPORT, 0)).unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert!(job.failure.unwrap().contains("the checks could not run"));
}
