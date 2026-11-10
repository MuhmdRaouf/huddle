//! huddle-jobs: model.

use serde::{Deserialize, Serialize};
use std::fmt;

/// Where a job is in its lifecycle.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum JobState {
    Queued,
    Running,
    AwaitingReview,
    Completed,
    Failed,
    Accepted,
    Cancelled,
}

/// What a job's worktree may do.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Access {
    Readonly,
    Exec,
    Write,
}

/// What happened to a job.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum JobEvent {
    Started,
    Finished,
    Failed,
    Returned,
    Accepted,
    Cancelled,
}

/// Which run of the worker an attempt is.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AttemptKind {
    Initial,
    ReviewFix,
}

/// How a worker attempt ended. `kind` picks the variant; job.json carries only that variant's
/// fields, so a null `exitCode` or `signal` survives a round trip.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum AttemptOutcome {
    Completed,
    Crashed {
        #[serde(rename = "exitCode")]
        exit_code: Option<i32>,
        signal: Option<String>,
        #[serde(rename = "stderrTail")]
        stderr_tail: String,
    },
    Error {
        message: String,
    },
    Timeout,
    Stopped,
}

/// One path a run touched, with the reason.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FileNote {
    pub path: String,
    pub why: String,
}

/// What the worker reported.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Report {
    pub summary: String,
    pub files: Vec<FileNote>,
    pub open_items: Vec<String>,
}

/// One run of the worker on a job.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Attempt {
    pub n: u32,
    pub kind: AttemptKind,
    pub prompt: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub feedback: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    pub started_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ended_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub outcome: Option<AttemptOutcome>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub report: Option<Report>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub report_text: Option<String>,
}

/// How a job's review ended.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Decision {
    pub kind: JobState,
    pub at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub commit: Option<String>,
}

/// The on-disk job.json: one brief a driver works through attempts until review decides it.
/// Field order is the JSON key order.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Job {
    pub id: String,
    pub harness: String,
    pub title: String,
    pub task: String,
    pub access: Access,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub effort: Option<String>,
    pub state: JobState,
    pub repo_root: String,
    pub base_sha: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub base_branch: Option<String>,
    pub worktree: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch: Option<String>,
    pub attempts: Vec<Attempt>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failure: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub decision: Option<Decision>,
    pub created_at: String,
    pub updated_at: String,
    pub version: u64,
}

impl JobState {
    pub fn as_str(self) -> &'static str {
        match self {
            JobState::Queued => "queued",
            JobState::Running => "running",
            JobState::AwaitingReview => "awaiting_review",
            JobState::Completed => "completed",
            JobState::Failed => "failed",
            JobState::Accepted => "accepted",
            JobState::Cancelled => "cancelled",
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            JobState::Queued => "queued",
            JobState::Running => "running",
            JobState::AwaitingReview => "awaiting review",
            JobState::Completed => "completed",
            JobState::Failed => "failed",
            JobState::Accepted => "accepted",
            JobState::Cancelled => "cancelled",
        }
    }

    /// A terminal state accepts no event at all.
    pub fn is_terminal(self) -> bool {
        matches!(
            self,
            JobState::Completed | JobState::Accepted | JobState::Cancelled
        )
    }

    pub fn is_active(self) -> bool {
        matches!(self, JobState::Queued | JobState::Running)
    }

    /// The state in words, as "the job <words>" reads. Every state has its own words; there is no
    /// fallback text.
    pub fn words(self) -> &'static str {
        match self {
            JobState::Queued => "is queued",
            JobState::Running => "is running",
            JobState::AwaitingReview => "awaits review",
            JobState::Completed => "has completed",
            JobState::Failed => "has failed",
            JobState::Accepted => "was already accepted",
            JobState::Cancelled => "was already cancelled",
        }
    }
}

impl JobEvent {
    /// The event in words, as "cannot be <words>" reads.
    pub fn words(self) -> &'static str {
        match self {
            JobEvent::Started => "started",
            JobEvent::Finished => "finished",
            JobEvent::Failed => "failed",
            JobEvent::Returned => "returned",
            JobEvent::Accepted => "accepted",
            JobEvent::Cancelled => "cancelled",
        }
    }
}

/// The one way a state machine step can fail: the state does not accept the event.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct IllegalTransition {
    pub from: JobState,
    pub event: JobEvent,
}

impl fmt::Display for IllegalTransition {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "the job {}, so it cannot be {}",
            self.from.words(),
            self.event.words()
        )
    }
}

impl std::error::Error for IllegalTransition {}

/// The one legal move for (state, event), or IllegalTransition. A finished run lands in
/// awaiting_review only when the worktree takes writes.
pub fn next_state(
    state: JobState,
    access: Access,
    event: JobEvent,
) -> Result<JobState, IllegalTransition> {
    match (state, event) {
        (JobState::Queued, JobEvent::Started) => Ok(JobState::Running),
        (JobState::Queued, JobEvent::Failed) => Ok(JobState::Failed),
        (JobState::Queued, JobEvent::Cancelled) => Ok(JobState::Cancelled),
        (JobState::Running, JobEvent::Finished) => Ok(match access {
            Access::Write => JobState::AwaitingReview,
            Access::Readonly | Access::Exec => JobState::Completed,
        }),
        (JobState::Running, JobEvent::Failed) => Ok(JobState::Failed),
        (JobState::Running, JobEvent::Cancelled) => Ok(JobState::Cancelled),
        (JobState::AwaitingReview, JobEvent::Returned) => Ok(JobState::Queued),
        (JobState::AwaitingReview, JobEvent::Accepted) => Ok(JobState::Accepted),
        (JobState::AwaitingReview, JobEvent::Cancelled) => Ok(JobState::Cancelled),
        (JobState::Failed, JobEvent::Returned) => Ok(JobState::Queued),
        (JobState::Failed, JobEvent::Cancelled) => Ok(JobState::Cancelled),
        _ => Err(IllegalTransition { from: state, event }),
    }
}

/// "job <id> <state.words()>; this only works on a job that <allowed words joined ", " and last
/// " or ">".
pub fn wrong_state_text(id: &str, state: JobState, allowed: &[JobState]) -> String {
    let words: Vec<String> = allowed
        .iter()
        .map(|s| s.words().replacen("was already ", "was ", 1))
        .collect();
    let list = match words.as_slice() {
        [] => String::new(),
        [one] => one.clone(),
        [head @ .., last] => format!("{} or {}", head.join(", "), last),
    };
    format!(
        "job {} {}; this only works on a job that {}",
        id,
        state.words(),
        list
    )
}

/// First non-blank line of the task, whitespace runs collapsed to one space, cut to 72 chars
/// with "…" as the last char when cut. A task with no non-blank line gives "(no title)".
pub fn task_title(task: &str) -> String {
    let Some(title) = task
        .lines()
        .map(|line| line.split_whitespace().collect::<Vec<&str>>().join(" "))
        .find(|line| !line.is_empty())
    else {
        return "(no title)".to_string();
    };
    if title.chars().count() <= 72 {
        return title;
    }
    let mut cut: String = title.chars().take(71).collect();
    cut.push('…');
    cut
}

impl Job {
    /// The most recent attempt, or None before the first run.
    pub fn last_attempt(&self) -> Option<&Attempt> {
        self.attempts.last()
    }

    /// Index of the last attempt while it has no outcome, else None.
    pub fn pending_attempt(&self) -> Option<usize> {
        let last = self.last_attempt()?;
        (last.outcome.is_none()).then_some(self.attempts.len() - 1)
    }
}
