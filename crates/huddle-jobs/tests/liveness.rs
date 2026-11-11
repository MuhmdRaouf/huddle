mod support;

use huddle_jobs::id::iso_ms;
use huddle_jobs::liveness::*;
use huddle_jobs::model::*;
use huddle_jobs::store::JobStore;
use std::process::Command;
use support::{Rig, git, rig, sample_job, write};

const NOW: u64 = 1_800_000_000_000;

fn store_for(r: &Rig) -> JobStore {
    JobStore::for_repo(&r.paths, &r.repo)
}

fn own_record() -> ProcessRecord {
    let pid = std::process::id();
    ProcessRecord {
        pid,
        start_token: huddle_procs::live::start_token(pid).expect("token"),
    }
}

fn dead_record() -> ProcessRecord {
    let mut child = Command::new("/usr/bin/true").spawn().expect("spawn");
    let pid = child.id();
    child.wait().expect("wait");
    ProcessRecord {
        pid,
        start_token: "gone".into(),
    }
}

#[test]
fn a_record_round_trips_with_a_private_mode() {
    let r = rig();
    let path = r.home.root().join("driver.toml");
    write_record(
        &path,
        &ProcessRecord {
            pid: 42,
            start_token: "tok".into(),
        },
    )
    .unwrap();
    assert_eq!(
        read_record(&path),
        Some(ProcessRecord {
            pid: 42,
            start_token: "tok".into()
        })
    );
    use std::os::unix::fs::PermissionsExt;
    assert_eq!(
        std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
        0o644
    );
    assert_eq!(read_record(&r.home.root().join("missing.json")), None);
}

#[test]
fn a_queued_job_with_no_driver_is_alive_for_the_grace_then_dead() {
    assert!(job_alive(JobState::Queued, false, false, NOW - 1_000, NOW));
    assert!(job_alive(JobState::Queued, false, false, NOW - 29_999, NOW));
    assert!(!job_alive(
        JobState::Queued,
        false,
        false,
        NOW - 30_000,
        NOW
    ));
    assert!(
        job_alive(JobState::Queued, true, true, NOW - 90_000, NOW),
        "a driver that started is alive"
    );
}

#[test]
fn a_running_job_with_no_live_driver_is_dead_at_once() {
    assert!(!job_alive(JobState::Running, false, true, NOW, NOW));
    assert!(!job_alive(JobState::Running, false, false, NOW, NOW));
    assert!(job_alive(JobState::Running, true, true, NOW - 90_000, NOW));
}

#[test]
fn a_running_job_whose_driver_died_fails_with_worker_died() {
    let r = rig();
    let store = store_for(&r);
    let mut job = sample_job(&r, "d1", Access::Write, JobState::Running);
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
    write_record(&driver_record_path(&store.job_dir("d1")), &dead_record()).unwrap();
    let report = sweep(&store, NOW).unwrap();
    assert_eq!(report.died, vec!["d1".to_string()]);
    let job = store.get("d1").unwrap();
    assert_eq!(job.state, JobState::Failed);
    assert_eq!(job.failure.as_deref(), Some("worker died"));
    let attempt = job.last_attempt().unwrap();
    assert_eq!(
        attempt.outcome,
        Some(AttemptOutcome::Error {
            message: "worker died".into()
        })
    );
    assert_eq!(attempt.ended_at.as_deref(), Some(iso_ms(NOW).as_str()));
}

#[test]
fn a_live_driver_keeps_its_job_running() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "d2", Access::Write, JobState::Running))
        .unwrap();
    write_record(&driver_record_path(&store.job_dir("d2")), &own_record()).unwrap();
    assert!(sweep(&store, NOW).unwrap().died.is_empty());
    assert_eq!(store.get("d2").unwrap().state, JobState::Running);
}

#[test]
fn a_reused_pid_counts_as_dead() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "d3", Access::Write, JobState::Running))
        .unwrap();
    let mut record = own_record();
    record.start_token = "not-the-real-start-token".into();
    write_record(&driver_record_path(&store.job_dir("d3")), &record).unwrap();
    assert_eq!(sweep(&store, NOW).unwrap().died, vec!["d3".to_string()]);
}

#[test]
fn a_harness_record_with_a_wrong_token_is_never_signalled() {
    let r = rig();
    let store = store_for(&r);
    let mut child = Command::new("/bin/sleep").arg("30").spawn().expect("spawn");
    store
        .create(&sample_job(&r, "d4", Access::Write, JobState::Queued))
        .unwrap();
    let dir = store.job_dir("d4");
    write_record(
        &harness_record_path(&dir),
        &ProcessRecord {
            pid: child.id(),
            start_token: "wrong".into(),
        },
    )
    .unwrap();
    reap_harness(&dir, std::time::Duration::from_millis(100));
    assert!(
        child.try_wait().unwrap().is_none(),
        "the process with the same pid must not be signalled"
    );
    assert!(!harness_record_path(&dir).exists());
    child.kill().unwrap();
    child.wait().unwrap();
}

#[test]
fn a_dead_jobs_harness_group_is_terminated() {
    let r = rig();
    let store = store_for(&r);
    use std::os::unix::process::CommandExt;
    let mut child = Command::new("/bin/sleep")
        .arg("30")
        .process_group(0)
        .spawn()
        .expect("spawn");
    let pid = child.id();
    store
        .create(&sample_job(&r, "d5", Access::Write, JobState::Queued))
        .unwrap();
    let dir = store.job_dir("d5");
    write_record(
        &harness_record_path(&dir),
        &ProcessRecord {
            pid,
            start_token: huddle_procs::live::start_token(pid).unwrap(),
        },
    )
    .unwrap();
    reap_harness(&dir, std::time::Duration::from_millis(100));
    let status = child.wait().unwrap();
    assert!(!status.success(), "the harness was terminated");
}

#[test]
fn a_version_conflict_is_not_an_error_and_writes_nothing() {
    let r = rig();
    let store = store_for(&r);
    let job = store
        .create(&sample_job(&r, "d6", Access::Write, JobState::Running))
        .unwrap();
    let mut moved = job.clone();
    moved.state = JobState::AwaitingReview;
    store.update(&moved).unwrap();
    assert_eq!(mark_worker_died(&store, &job, NOW), Ok(false));
    assert_eq!(store.get("d6").unwrap().state, JobState::AwaitingReview);
}

#[test]
fn t3_sweep_leaves_every_jobs_folder_alone() {
    let r = rig();
    let store = store_for(&r);
    store
        .create(&sample_job(&r, "w1", Access::Write, JobState::Accepted))
        .unwrap();
    store
        .create(&sample_job(&r, "w3", Access::Readonly, JobState::Running))
        .unwrap();
    write_record(&driver_record_path(&store.job_dir("w3")), &dead_record()).unwrap();
    write(&r.repo, "a.txt", "edited\n");
    sweep(&store, NOW).unwrap();
    assert_eq!(store.get("w3").unwrap().state, JobState::Failed);
    assert_eq!(support::read(&r.repo, "a.txt"), "edited\n");
    assert_eq!(git(&r.repo, &["worktree", "list"]).lines().count(), 1);
}
