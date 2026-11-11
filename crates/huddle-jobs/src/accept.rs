//! huddle-jobs: accept.

use crate::checks;
use crate::git::{self, GitError};
use crate::id::{iso_ms, now_ms};
use crate::model::{Decision, Job, JobState, wrong_state_text};
use crate::store::{JobStore, StoreError};
use crate::verify;
use std::fmt;
use std::path::Path;

/// How an accept ended.
#[derive(Debug, PartialEq, Eq)]
pub struct AcceptOutcome {
    pub job: Job,
    /// The commit made in the job's folder, or None when nothing was committed.
    pub commit: Option<String>,
}

/// The one way an accept can fail.
#[derive(Debug, PartialEq, Eq)]
pub enum AcceptError {
    WrongState {
        id: String,
        state: JobState,
    },
    Store(StoreError),
    Git(GitError),
    /// The job's checks did not pass in its folder; the text names what failed.
    Checks(String),
}

impl fmt::Display for AcceptError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            AcceptError::WrongState { id, state } => {
                write!(
                    f,
                    "{}",
                    wrong_state_text(id, *state, &[JobState::AwaitingReview])
                )
            }
            AcceptError::Store(error) => write!(f, "{error}"),
            AcceptError::Git(error) => write!(f, "{error}"),
            AcceptError::Checks(text) => write!(f, "{text}"),
        }
    }
}

impl std::error::Error for AcceptError {}

impl From<StoreError> for AcceptError {
    fn from(error: StoreError) -> Self {
        AcceptError::Store(error)
    }
}

impl From<GitError> for AcceptError {
    fn from(error: GitError) -> Self {
        AcceptError::Git(error)
    }
}

/// Accepts a job awaiting review: its setup and gates run in its folder, the folder's change is committed
/// on the branch checked out there, and the decision is recorded. `id` is a full id or a unique
/// prefix. Nothing changed: no commit.
pub fn accept(store: &JobStore, id: &str) -> Result<AcceptOutcome, AcceptError> {
    let job = open_job(store, id)?;
    prove(store, &job)?;
    let commit = commit_pending(&job)?;
    decide(store, job, commit)
}

/// As accept, but the change stays in the folder, uncommitted. commit is None.
pub fn accept_no_commit(store: &JobStore, id: &str) -> Result<AcceptOutcome, AcceptError> {
    let job = open_job(store, id)?;
    prove(store, &job)?;
    decide(store, job, None)
}

/// The job awaiting review whose folder still has the branch it started on.
fn open_job(store: &JobStore, id: &str) -> Result<Job, AcceptError> {
    let job = store.find(id)?;
    if job.state != JobState::AwaitingReview {
        return Err(AcceptError::WrongState {
            id: job.id.clone(),
            state: job.state,
        });
    }
    refuse_moved_branch(&job)?;
    Ok(job)
}

fn decide(
    store: &JobStore,
    mut job: Job,
    commit: Option<String>,
) -> Result<AcceptOutcome, AcceptError> {
    let now = iso_ms(now_ms());
    job.state = JobState::Accepted;
    job.decision = Some(Decision {
        kind: JobState::Accepted,
        at: now.clone(),
        commit: commit.clone(),
    });
    job.updated_at = now;
    let job = store.update(&job)?;
    Ok(AcceptOutcome { job, commit })
}

/// Runs the job's setup and gates in its folder; a failure refuses the accept and leaves the
/// folder as it is. Scope and forbid findings are evidence the reviewer weighs, not a refusal.
fn prove(store: &JobStore, job: &Job) -> Result<(), AcceptError> {
    let checks = checks::read_checks(&store.job_dir(&job.id)).map_err(|error| {
        AcceptError::Checks(format!("the job's checks cannot be read: {error}"))
    })?;
    if checks.setup.is_empty() && checks.gates.is_empty() {
        return Ok(());
    }
    let env = huddle_harness::env::harness_env(&huddle_config::env::Env::from_process(), &[]);
    match verify::run_commands(&checks, Path::new(&job.dir), &env)
        .into_iter()
        .find(|gate| gate.failed())
    {
        None => Ok(()),
        Some(gate) => Err(AcceptError::Checks(format!(
            "{} failed: nothing was accepted\n{}",
            gate.run,
            gate.tail.trim()
        ))),
    }
}

/// Refuses unless the folder still has the branch the job started on; a commit on another
/// branch would surprise the user.
fn refuse_moved_branch(job: &Job) -> Result<(), AcceptError> {
    let Some(expected) = job.base_branch.as_deref() else {
        return Ok(());
    };
    let actual = match git::current_branch(Path::new(&job.dir))? {
        Some(branch) => branch,
        None => "a detached HEAD".to_string(),
    };
    if actual != expected {
        return Err(AcceptError::Git(GitError::WrongBranch {
            expected: expected.to_string(),
            actual,
        }));
    }
    Ok(())
}

/// Commits whatever the folder holds that is not yet committed and returns the commit; nothing
/// to commit gives None. The message is the task title and, when the last attempt reported one,
/// its summary. Nothing else signs the work.
fn commit_pending(job: &Job) -> Result<Option<String>, AcceptError> {
    let dir = Path::new(&job.dir);
    let changes = git::changes(dir, "HEAD")?;
    if changes.added.is_empty()
        && changes.modified.is_empty()
        && changes.deleted.is_empty()
        && changes.untracked.is_empty()
    {
        return Ok(None);
    }
    let mut message = job.title.clone();
    if let Some(summary) = job
        .last_attempt()
        .and_then(|attempt| attempt.report.as_ref())
        .map(|report| report.summary.trim())
        .filter(|summary| !summary.is_empty())
    {
        message.push_str("\n\n");
        message.push_str(summary);
    }
    message.push('\n');
    Ok(Some(git::commit_all(dir, &message)?))
}
