//! huddle-jobs: cancel.

use crate::id::{iso_ms, now_ms};
use crate::liveness::{driver_record_path, read_record, reap_harness};
use crate::model::{
    AttemptOutcome, Decision, Job, JobEvent, JobState, next_state, wrong_state_text,
};
use crate::store::{JobStore, StoreError};
use crate::worktree;
use huddle_config::atomic::write_atomic;
use huddle_procs::group::terminate_group;
use huddle_procs::live::process_alive;
use std::fmt;
use std::fs;
use std::path::Path;
use std::thread;
use std::time::{Duration, Instant};

/// The states a cancel accepts; a decided job is refused.
const CANCELABLE: &[JobState] = &[
    JobState::Queued,
    JobState::Running,
    JobState::AwaitingReview,
    JobState::Failed,
];

/// How often a waiting cancel re-checks the driver and the store.
const POLL: Duration = Duration::from_millis(250);

/// The one way a cancel can fail.
#[derive(Debug, PartialEq, Eq)]
pub enum CancelError {
    WrongState { id: String, state: JobState },
    Io(String),
    Store(StoreError),
}

impl fmt::Display for CancelError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            CancelError::WrongState { id, state } => {
                write!(f, "{}", wrong_state_text(id, *state, CANCELABLE))
            }
            CancelError::Io(message) => write!(f, "{message}"),
            CancelError::Store(error) => write!(f, "{error}"),
        }
    }
}

impl std::error::Error for CancelError {}

impl From<StoreError> for CancelError {
    fn from(error: StoreError) -> Self {
        CancelError::Store(error)
    }
}

/// The knobs a cancel runs with.
pub struct CancelOptions {
    /// Time the driver gets to see the stop file and end the job.
    pub stop_wait: Duration,
    /// SIGTERM to SIGKILL grace when the driver must be terminated.
    pub grace: Duration,
}

/// The production timings.
pub const CANCEL_OPTIONS: CancelOptions = CancelOptions {
    stop_wait: Duration::from_secs(25),
    grace: Duration::from_secs(10),
};

/// How a cancel ended.
#[derive(Debug)]
pub struct CancelOutcome {
    pub job: Job,
    /// A driver had to be terminated because it did not stop on its own.
    pub forced_driver: bool,
    /// "huddle job <id> is cancelled, but its worktree could not be removed: <git error>", when
    /// that happened.
    pub cleanup_failed: Option<String>,
}

/// Cancels a queued, running, awaiting-review or failed job: a live driver is asked to stop
/// through its stop file and terminated only when it will not, a harness left behind is reaped,
/// the open attempt is closed and the job marked cancelled, then the worktree and its branch are
/// removed. `id` is a full id or a unique prefix.
pub fn cancel(
    store: &JobStore,
    id: &str,
    options: &CancelOptions,
) -> Result<CancelOutcome, CancelError> {
    let found = store.find(id)?;
    if !CANCELABLE.contains(&found.state) {
        return Err(CancelError::WrongState {
            id: found.id.clone(),
            state: found.state,
        });
    }
    let dir = store.job_dir(&found.id);
    let forced_driver = stop_driver(store, &found.id, &dir, options)?;
    reap_harness(&dir, options.grace);
    let mut job = store.get(&found.id)?;
    if job.state != JobState::Cancelled {
        job = record_cancelled(store, job)?;
    }
    let cleanup_failed = cleanup(&job, &dir);
    Ok(CancelOutcome {
        job,
        forced_driver,
        cleanup_failed,
    })
}

/// Asks a live driver to stop through its stop file, waits for it to end the job, and terminates
/// its process group when it will not; true when a termination was needed.
fn stop_driver(
    store: &JobStore,
    id: &str,
    dir: &Path,
    options: &CancelOptions,
) -> Result<bool, CancelError> {
    let Some(record) = read_record(&driver_record_path(dir)) else {
        return Ok(false);
    };
    if !process_alive(record.pid, &record.start_token) {
        return Ok(false);
    }
    let stop = dir.join("stop");
    write_atomic(&stop, b"", 0o600)
        .map_err(|error| CancelError::Io(format!("{}: {error}", stop.display())))?;
    let deadline = Instant::now() + options.stop_wait;
    loop {
        if !process_alive(record.pid, &record.start_token) {
            return Ok(false);
        }
        if store
            .get(id)
            .is_ok_and(|job| job.state == JobState::Cancelled)
        {
            return Ok(false);
        }
        if Instant::now() >= deadline {
            terminate_group(record.pid, options.grace);
            return Ok(true);
        }
        thread::sleep(POLL);
    }
}

/// The cancelled job as one store write; a VersionConflict re-reads once (the stale writer was
/// the driver finishing) and repeats.
fn record_cancelled(store: &JobStore, job: Job) -> Result<Job, CancelError> {
    let id = job.id.clone();
    match apply_cancel(store, job) {
        Ok(job) => Ok(job),
        Err(StoreError::VersionConflict { .. }) => {
            let reread = store.get(&id)?;
            if reread.state == JobState::Cancelled {
                return Ok(reread);
            }
            Ok(apply_cancel(store, reread)?)
        }
        Err(error) => Err(error.into()),
    }
}

/// The cancelled job: the open attempt closed as stopped, the state moved, the decision recorded,
/// the failure cleared.
fn apply_cancel(store: &JobStore, mut job: Job) -> Result<Job, StoreError> {
    let now = now_ms();
    if let Some(index) = job.pending_attempt() {
        let attempt = &mut job.attempts[index];
        attempt.ended_at = Some(iso_ms(now));
        attempt.outcome = Some(AttemptOutcome::Stopped);
    }
    job.state = next_state(job.state, job.access, JobEvent::Cancelled).map_err(|illegal| {
        StoreError::Io {
            message: illegal.to_string(),
        }
    })?;
    job.decision = Some(Decision {
        kind: JobState::Cancelled,
        at: iso_ms(now),
        commit: None,
    });
    job.failure = None;
    job.updated_at = iso_ms(now);
    store.update(&job)
}

/// Removes the stop file and the worktree and its branch once the job is cancelled; a worktree
/// that cannot be removed is reported, the cancellation stands.
fn cleanup(job: &Job, dir: &Path) -> Option<String> {
    let _ = fs::remove_file(dir.join("stop"));
    let repo = Path::new(&job.repo_root);
    match worktree::remove(repo, Path::new(&job.worktree), job.branch.as_deref()) {
        Ok(()) => None,
        Err(error) => Some(format!(
            "huddle job {} is cancelled, but its worktree could not be removed: {error}",
            job.id
        )),
    }
}
