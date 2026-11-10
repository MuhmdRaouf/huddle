//! huddle-jobs: return_job.

use crate::id::{iso_ms, now_ms};
use crate::model::{
    Access, Attempt, AttemptKind, Job, JobEvent, JobState, next_state, wrong_state_text,
};
use crate::report;
use crate::store::{JobStore, StoreError};
use std::fmt;
use std::fs;
use std::io::ErrorKind;
use std::path::Path;

/// The one way a return can fail.
#[derive(Debug, PartialEq, Eq)]
pub enum ReturnError {
    EmptyFeedback,
    WrongState { id: String, state: JobState },
    NoWorktree { id: String },
    Io(String),
    Store(StoreError),
}

impl fmt::Display for ReturnError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ReturnError::EmptyFeedback => write!(f, "return needs feedback for the harness"),
            ReturnError::WrongState { id, state } => write!(
                f,
                "{}",
                wrong_state_text(id, *state, &[JobState::AwaitingReview, JobState::Failed])
            ),
            ReturnError::NoWorktree { id } => write!(
                f,
                "job {id} kept no worktree (it was not a write job): start a new /huddle:run instead"
            ),
            ReturnError::Io(message) => write!(f, "{message}"),
            ReturnError::Store(error) => write!(f, "{error}"),
        }
    }
}

impl std::error::Error for ReturnError {}

impl From<StoreError> for ReturnError {
    fn from(error: StoreError) -> Self {
        ReturnError::Store(error)
    }
}

/// Requeues a job awaiting review, or a failed job, with a review_fix attempt that resumes the
/// previous session on the same worktree. A stale stop request is cleared first, so the run the
/// reviewer asked for is never stopped on arrival. The caller starts the driver.
pub fn return_job(store: &JobStore, id: &str, feedback: &str) -> Result<Job, ReturnError> {
    let trimmed = feedback.trim();
    if trimmed.is_empty() {
        return Err(ReturnError::EmptyFeedback);
    }
    let mut job = store.find(id)?;
    if !matches!(job.state, JobState::AwaitingReview | JobState::Failed) {
        return Err(ReturnError::WrongState {
            id: job.id,
            state: job.state,
        });
    }
    if job.access != Access::Write {
        return Err(ReturnError::NoWorktree { id: job.id.clone() });
    }
    // The stop file goes before the record changes: a return that queues the job while a stop
    // request survives would end the new run on arrival.
    remove_stop_file(&store.job_dir(&job.id))?;
    let session_id = job.attempts.last().and_then(|a| a.session_id.clone());
    let now = iso_ms(now_ms());
    job.attempts.push(Attempt {
        n: job.attempts.len() as u32 + 1,
        kind: AttemptKind::ReviewFix,
        prompt: report::review_fix_prompt(trimmed),
        feedback: Some(trimmed.to_string()),
        session_id,
        started_at: now.clone(),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    });
    job.failure = None;
    job.state = next_state(job.state, job.access, JobEvent::Returned)
        .map_err(|error| ReturnError::Io(error.to_string()))?;
    job.updated_at = now;
    Ok(store.update(&job)?)
}

/// Removes the job's stop request file; a missing file is fine.
fn remove_stop_file(job_dir: &Path) -> Result<(), ReturnError> {
    match fs::remove_file(job_dir.join("stop")) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == ErrorKind::NotFound => Ok(()),
        Err(error) => Err(ReturnError::Io(error.to_string())),
    }
}
