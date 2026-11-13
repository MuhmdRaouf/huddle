//! The delegation tools: run a task on a harness, list jobs, review or decide one, cancel one.
//! Each tool calls the same job and harness functions as the CLI word of the same name and
//! answers with the same text. All of them block, so the server runs them off the async tasks.

use std::path::PathBuf;
use std::thread;
use std::time::Duration;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::check::check_entry;
use huddle_harness::user_entries::all_entries;
use huddle_jobs::accept::{self, AcceptError};
use huddle_jobs::cancel::{self, CancelError};
use huddle_jobs::checks;
use huddle_jobs::checks_render;
use huddle_jobs::driver::{self, SubmitInput};
use huddle_jobs::git;
use huddle_jobs::id::{new_job_id, now_ms};
use huddle_jobs::liveness;
use huddle_jobs::model::{Access, Job};
use huddle_jobs::overview;
use huddle_jobs::render;
use huddle_jobs::return_job;
use huddle_jobs::review::review_view;
use huddle_jobs::store::JobStore;
use rmcp::schemars::{self, JsonSchema};
use serde::{Deserialize, Serialize};

/// The wait polls at this delay first, doubling up to MAX_POLL_MS.
const FIRST_POLL_MS: u64 = 1_000;
const MAX_POLL_MS: u64 = 5_000;

/// What a delegation tool needs besides its input: the environment `main` read, and the huddle
/// binary the detached driver runs as.
pub struct Context {
    pub env: Env,
    pub exe: PathBuf,
}

/// Why a tool did not do its work, in the CLI's words. The server turns it into a tool error.
pub type ToolError = String;

#[derive(Debug, Deserialize, JsonSchema)]
pub struct RunInput {
    /// The harness id, such as "opencode".
    pub harness: String,
    /// The task for the harness.
    pub task: String,
    /// The folder the harness works in. Defaults to the folder this server runs in.
    pub dir: Option<String>,
    /// Return at once with the job id instead of waiting for the report.
    #[serde(default)]
    pub background: bool,
}

#[derive(Debug, Serialize, JsonSchema)]
pub struct RunOutput {
    pub job_id: String,
    pub title: String,
    pub harness: String,
    /// The job's state: queued, running, awaiting_review, completed, failed and so on.
    pub state: String,
    /// The result packet, once the job settled. Absent for a background run.
    pub report: Option<String>,
}

#[derive(Debug, Deserialize, JsonSchema)]
pub struct StatusInput {
    /// List the live jobs of every repository instead of the repository this server runs in.
    #[serde(default)]
    pub all_repos: bool,
}

#[derive(Debug, Serialize, JsonSchema)]
pub struct JobSummary {
    pub id: String,
    pub harness: String,
    pub title: String,
    pub state: String,
    /// One line on why the job is stuck or failed.
    pub reason: Option<String>,
}

#[derive(Debug, Serialize, JsonSchema)]
pub struct StatusOutput {
    pub jobs: Vec<JobSummary>,
}

/// What `huddle_result` does with the job.
#[derive(Debug, Clone, Copy, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ResultAction {
    /// Commit the job's change on the current branch of its folder.
    Accept,
    /// Accept the job and leave its change uncommitted.
    AcceptNoCommit,
    /// Send the job back to the harness with `feedback`.
    Return,
}

#[derive(Debug, Deserialize, JsonSchema)]
pub struct ResultInput {
    /// The job id or a unique prefix of it.
    pub id: String,
    /// Include the job's diff in the report. Ignored when an action is given.
    #[serde(default)]
    pub diff: bool,
    /// Decide the job instead of reading its report.
    pub action: Option<ResultAction>,
    /// What the harness should fix. Required for the return action.
    pub feedback: Option<String>,
}

#[derive(Debug, Serialize, JsonSchema)]
pub struct ResultOutput {
    pub job_id: String,
    pub state: String,
    /// The result packet, or the line the CLI prints for the action.
    pub text: String,
}

#[derive(Debug, Deserialize, JsonSchema)]
pub struct CancelInput {
    /// The job id or a unique prefix of it.
    pub id: String,
}

#[derive(Debug, Serialize, JsonSchema)]
pub struct CancelOutput {
    pub job_id: String,
    pub state: String,
    pub message: String,
}

pub fn run(ctx: &Context, input: RunInput) -> Result<RunOutput, ToolError> {
    let RunInput {
        harness,
        task,
        dir,
        background,
    } = input;
    if task.trim().is_empty() {
        return Err("run needs a harness id and a task".to_string());
    }
    let paths = paths_of(&ctx.env)?;
    let entries =
        all_entries(&paths.config_dir.join("harnesses.toml")).map_err(|e| e.to_string())?;
    let Some(entry) = entries.iter().find(|entry| entry.id == harness) else {
        return Err(format!(
            "no harness named \"{harness}\": /huddle:setup lists them"
        ));
    };
    if !entry
        .run
        .accepted_access()
        .iter()
        .any(|a| a.as_str() == "write")
    {
        let supported: Vec<&str> = entry
            .run
            .accepted_access()
            .iter()
            .map(|a| a.as_str())
            .collect();
        return Err(format!(
            "{harness} cannot run with write access: it supports {}",
            supported.join(", ")
        ));
    }
    let report = check_entry(entry, &ctx.env, None);
    if let Some(line) = report.checks.iter().find(|l| !l.ok && l.name == "binary") {
        return Err(format!(
            "{harness} is not ready: {}: /huddle:setup {harness} shows what to fix",
            line.detail
        ));
    }
    let folder = match dir {
        Some(dir) => PathBuf::from(dir),
        None => std::env::current_dir().map_err(|e| e.to_string())?,
    };
    let input = SubmitInput {
        id: new_job_id(now_ms()).map_err(|e| e.to_string())?,
        harness,
        task,
        access: Access::Write,
        effort: None,
        dir: folder,
    };
    let (store, job) = driver::submit(&paths, &input).map_err(|e| e.to_string())?;
    driver::spawn_driver(&ctx.exe, &store.job_dir(&job.id), &ctx.env).map_err(|error| {
        format!(
            "job {} is queued, but its driver could not start: {error:?}",
            job.id
        )
    })?;
    if background {
        return Ok(run_output(&job, None));
    }
    wait_for_report(&store, &job.id)
}

/// Sleeps and polls until the job settles, sweeping each round so a dead driver fails its job.
fn wait_for_report(store: &JobStore, job_id: &str) -> Result<RunOutput, ToolError> {
    let mut delay = Duration::from_millis(FIRST_POLL_MS);
    loop {
        thread::sleep(delay);
        delay = (delay * 2).min(Duration::from_millis(MAX_POLL_MS));
        let _ = liveness::sweep(store, now_ms());
        let Ok(job) = store.get(job_id) else {
            continue;
        };
        if job.state.is_active() {
            continue;
        }
        let packet = packet_of(store, &job, false)?;
        return Ok(run_output(&job, Some(packet)));
    }
}

fn run_output(job: &Job, report: Option<String>) -> RunOutput {
    RunOutput {
        job_id: job.id.clone(),
        title: job.title.clone(),
        harness: job.harness.clone(),
        state: job.state.as_str().to_string(),
        report,
    }
}

pub fn status(ctx: &Context, input: StatusInput) -> Result<StatusOutput, ToolError> {
    let paths = paths_of(&ctx.env)?;
    if input.all_repos {
        let rows = overview::overview(&paths, now_ms());
        let jobs = rows
            .into_iter()
            .map(|row| JobSummary {
                id: row.id,
                harness: row.harness,
                title: row.title,
                state: row.state.to_string(),
                reason: row.why,
            })
            .collect();
        return Ok(StatusOutput { jobs });
    }
    let store = repo_store(&paths)?;
    liveness::sweep(&store, now_ms()).map_err(|e| e.to_string())?;
    let jobs = store
        .list()
        .map_err(|e| e.to_string())?
        .into_iter()
        .map(|job| JobSummary {
            reason: job.failure.clone(),
            id: job.id,
            harness: job.harness,
            title: job.title,
            state: job.state.as_str().to_string(),
        })
        .collect();
    Ok(StatusOutput { jobs })
}

pub fn result(ctx: &Context, input: ResultInput) -> Result<ResultOutput, ToolError> {
    let paths = paths_of(&ctx.env)?;
    let store = repo_store(&paths)?;
    liveness::sweep(&store, now_ms()).map_err(|e| e.to_string())?;
    let id = input.id.as_str();
    match input.action {
        Some(action @ (ResultAction::Accept | ResultAction::AcceptNoCommit)) => {
            let no_commit = matches!(action, ResultAction::AcceptNoCommit);
            let outcome = if no_commit {
                accept::accept_no_commit(&store, id)
            } else {
                accept::accept(&store, id)
            }
            .map_err(|error| accept_failed(id, &error))?;
            Ok(ResultOutput {
                job_id: outcome.job.id.clone(),
                state: outcome.job.state.as_str().to_string(),
                text: render::accepted_line(&outcome, no_commit),
            })
        }
        Some(ResultAction::Return) => {
            let feedback = input.feedback.as_deref().unwrap_or_default();
            let job = return_job::return_job(&store, id, feedback).map_err(|e| e.to_string())?;
            driver::spawn_driver(&ctx.exe, &store.job_dir(&job.id), &ctx.env).map_err(|error| {
                format!(
                    "job {} is queued, but its driver could not start: {error:?}",
                    job.id
                )
            })?;
            Ok(ResultOutput {
                job_id: job.id.clone(),
                state: job.state.as_str().to_string(),
                text: render::returned_line(&job),
            })
        }
        None => {
            let job = store.find(id).map_err(|e| e.to_string())?;
            let text = packet_of(&store, &job, input.diff)?;
            Ok(ResultOutput {
                job_id: job.id.clone(),
                state: job.state.as_str().to_string(),
                text,
            })
        }
    }
}

pub fn cancel(ctx: &Context, input: CancelInput) -> Result<CancelOutput, ToolError> {
    let paths = paths_of(&ctx.env)?;
    let store = repo_store(&paths)?;
    let outcome = cancel::cancel(&store, &input.id, &cancel::CANCEL_OPTIONS)
        .map_err(|error: CancelError| error.to_string())?;
    let message = if outcome.forced_driver {
        format!(
            "huddle job {} cancelled: its driver did not stop on its own and was terminated; the folder is untouched",
            outcome.job.id
        )
    } else {
        format!(
            "huddle job {} cancelled: the folder is untouched",
            outcome.job.id
        )
    };
    Ok(CancelOutput {
        job_id: outcome.job.id.clone(),
        state: outcome.job.state.as_str().to_string(),
        message,
    })
}

fn paths_of(env: &Env) -> Result<Paths, ToolError> {
    Paths::from_env(env).map_err(|e| e.to_string())
}

/// The job store of the repository this server runs in (its main checkout, whatever worktree).
fn repo_store(paths: &Paths) -> Result<JobStore, ToolError> {
    let cwd = std::env::current_dir().map_err(|e| e.to_string())?;
    let root = git::main_root(&cwd).map_err(|e| e.to_string())?;
    Ok(JobStore::for_repo(paths, &root))
}

/// The result packet and the last attempt's checks, as the CLI prints them.
fn packet_of(store: &JobStore, job: &Job, with_diff: bool) -> Result<String, ToolError> {
    let view = review_view(job, with_diff).map_err(|e| e.to_string())?;
    let mut text = render::result_packet(job, &view);
    if let Some(attempt) = job.last_attempt() {
        let verification = checks::read_verification(&store.job_dir(&job.id), attempt.n)
            .map_err(|e| format!("the checks could not be read: {e}"))?;
        if let Some(v) = verification {
            text.push_str("\n\n");
            text.push_str(&checks_render::verification_section(&v));
        }
    }
    Ok(text)
}

/// An accept that did not happen leaves the job awaiting review; the text says what to do.
fn accept_failed(id: &str, error: &AcceptError) -> ToolError {
    match error {
        AcceptError::Checks(_) => format!(
            "{error}\nthe job still awaits review: fix it, or return it with feedback (huddle_result {id}, action \"return\")."
        ),
        _ => error.to_string(),
    }
}
