//! huddle-jobs: liveness.

use crate::files;
use crate::id::{iso_ms, parse_iso_ms};
use crate::model::{AttemptOutcome, Job, JobState};
use crate::store::{JobStore, KEEP_TERMINAL_JOBS, StoreError};
use huddle_procs::group::terminate_group;
use huddle_procs::live::process_alive;
use std::path::{Path, PathBuf};
use std::time::Duration;

/// A process identity strong enough to tell the recorded process from a later
/// one the OS gave the same pid to.
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProcessRecord {
    pub pid: u32,
    pub start_token: String,
}

/// `<job dir>/driver.toml`: the driver's own process.
pub fn driver_record_path(job_dir: &Path) -> PathBuf {
    job_dir.join("driver.toml")
}

/// `<job dir>/harness.toml`: the harness process group leader, while it runs.
pub fn harness_record_path(job_dir: &Path) -> PathBuf {
    job_dir.join("harness.toml")
}

/// Writes the record with mode 0600, atomically.
pub fn write_record(path: &Path, record: &ProcessRecord) -> std::io::Result<()> {
    files::write(path, record)
}

/// None when the file is missing or unreadable.
pub fn read_record(path: &Path) -> Option<ProcessRecord> {
    files::read(path).ok().flatten()
}

/// A queued job with no driver keeps this long after its last update before the sweep calls it
/// dead: a driver may take a moment to write its record.
pub const QUEUED_GRACE_MS: u64 = 30_000;

/// The failure text a job carries when the sweep finds its driver gone.
pub const WORKER_DIED: &str = "worker died";

/// The harness grace the sweep gives a terminated group.
const STOP_GRACE: Duration = Duration::from_secs(10);

/// Pure. Queued: a record alive is alive; else alive while now_ms - updated_at_ms < QUEUED_GRACE_MS.
/// Running: dead when the record is missing or not alive. Any other state: not asked, returns true.
pub fn job_alive(
    state: JobState,
    record_alive: bool,
    record_present: bool,
    updated_at_ms: u64,
    now_ms: u64,
) -> bool {
    match state {
        JobState::Queued => {
            (record_present && record_alive)
                || now_ms.saturating_sub(updated_at_ms) < QUEUED_GRACE_MS
        }
        JobState::Running => record_present && record_alive,
        _ => true,
    }
}

/// Marks one job dead: state Failed, failure "worker died", the open attempt closed with
/// outcome Error { message: "worker died" }, ended_at and updated_at now. A VersionConflict is not an error: Ok(false).
pub fn mark_worker_died(store: &JobStore, job: &Job, now_ms: u64) -> Result<bool, StoreError> {
    let mut dead = job.clone();
    dead.state = JobState::Failed;
    dead.failure = Some(WORKER_DIED.to_string());
    if let Some(attempt) = dead.attempts.last_mut()
        && attempt.ended_at.is_none()
    {
        attempt.ended_at = Some(iso_ms(now_ms));
        attempt.outcome = Some(AttemptOutcome::Error {
            message: WORKER_DIED.to_string(),
        });
    }
    dead.updated_at = iso_ms(now_ms);
    match store.update(&dead) {
        Ok(_) => Ok(true),
        Err(StoreError::VersionConflict { .. }) => Ok(false),
        Err(error) => Err(error),
    }
}

/// Ends a dead driver's harness: terminates its process group only when the record is
/// `process_alive` with its token. Removes harness.toml either way.
pub fn reap_harness(job_dir: &Path, grace: Duration) {
    let path = harness_record_path(job_dir);
    if let Some(record) = read_record(&path)
        && process_alive(record.pid, &record.start_token)
    {
        terminate_group(record.pid, grace);
    }
    let _ = std::fs::remove_file(&path);
}

/// What one sweep pass did, ids in the order the store listed them.
pub struct SweepReport {
    pub died: Vec<String>,
    pub pruned: Vec<String>,
}

/// For every active job with a dead driver: the harness is reaped and the job marked dead. Then
/// store.prune(KEEP_TERMINAL_JOBS). The job's folder is the caller's and stays as it is.
pub fn sweep(store: &JobStore, now_ms: u64) -> Result<SweepReport, StoreError> {
    let mut report = SweepReport {
        died: Vec::new(),
        pruned: Vec::new(),
    };
    for job in store
        .list()?
        .into_iter()
        .filter(|job| job.state.is_active())
    {
        let dir = store.job_dir(&job.id);
        let record = read_record(&driver_record_path(&dir));
        let record_alive = record
            .as_ref()
            .is_some_and(|r| process_alive(r.pid, &r.start_token));
        let updated_at_ms = parse_iso_ms(&job.updated_at).unwrap_or(0);
        if !job_alive(
            job.state,
            record_alive,
            record.is_some(),
            updated_at_ms,
            now_ms,
        ) {
            if mark_worker_died(store, &job, now_ms)? {
                report.died.push(job.id.clone());
            }
            reap_harness(&dir, STOP_GRACE);
        }
    }
    report.pruned = store.prune(KEEP_TERMINAL_JOBS)?;
    Ok(report)
}
