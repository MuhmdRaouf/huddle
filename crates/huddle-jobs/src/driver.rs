//! huddle-jobs: driver.

use crate::checks;
use crate::git::{self, GitError};
use crate::id::{iso_ms, now_ms};
use crate::liveness::{self, ProcessRecord};
use crate::model::{
    self, Access, Attempt, AttemptKind, AttemptOutcome, Decision, Job, JobEvent, JobState,
};
use crate::report;
use crate::store::{JobStore, StoreError};
use crate::verify;
use crate::worktree;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::env::harness_env;
use huddle_harness::runner::{self, Outcome, RunCtx};
use huddle_harness::table::{
    Access as HarnessAccess, Brief, Entry, ExitInfo, SessionOwner, SessionRef,
};
use huddle_harness::user_entries;
use huddle_procs::detached::{self, DetachError, DetachSpec};
use huddle_procs::live::start_token;
use huddle_procs::stream::{Forced, StopHandle};
use std::fmt;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, PoisonError};
use std::thread;
use std::time::Duration;

/// How often the driver's watcher looks for a stop file while the harness runs.
const STOP_POLL: Duration = Duration::from_millis(250);

/// One brief to submit: the job's id, its harness, the task text, the access, the effort and
/// the working directory the repository is found from.
pub struct SubmitInput {
    pub id: String,
    pub harness: String,
    pub task: String,
    pub access: Access,
    pub effort: Option<String>,
    pub cwd: PathBuf,
}

/// Every way submitting or driving a job can fail.
#[derive(Debug, PartialEq, Eq)]
pub enum DriveError {
    Git(GitError),
    Store(StoreError),
    WrongState { id: String, state: JobState },
    Io(String),
}

impl fmt::Display for DriveError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            DriveError::Git(err) => err.fmt(f),
            DriveError::Store(err) => err.fmt(f),
            DriveError::WrongState { id, state } => {
                f.write_str(&model::wrong_state_text(id, *state, &[JobState::Queued]))
            }
            DriveError::Io(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for DriveError {}

/// The store in diagnostics, where a submit result prints.
impl fmt::Debug for JobStore {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("JobStore").finish()
    }
}

/// Creates the record first, then the worktree: a worktree failure removes the job directory,
/// so a failed submit leaves nothing behind.
pub fn submit(paths: &Paths, input: &SubmitInput) -> Result<(JobStore, Job), DriveError> {
    let root = git::root(&input.cwd).map_err(DriveError::Git)?;
    let base_sha = git::resolve(&root, "HEAD").map_err(DriveError::Git)?;
    let base_branch = git::current_branch(&root).map_err(DriveError::Git)?;
    let store = JobStore::for_repo(paths, &root);
    let worktree_path = store.worktree_path(&input.id);
    let branch = (input.access == Access::Write).then(|| worktree::job_branch(&input.id));
    let now = iso_ms(now_ms());
    let job = Job {
        id: input.id.clone(),
        harness: input.harness.clone(),
        title: model::task_title(&input.task),
        task: input.task.clone(),
        access: input.access,
        effort: input.effort.clone(),
        state: JobState::Queued,
        repo_root: root.to_string_lossy().into_owned(),
        base_sha,
        base_branch,
        worktree: worktree_path.to_string_lossy().into_owned(),
        branch,
        attempts: Vec::new(),
        failure: None,
        decision: None,
        created_at: now.clone(),
        updated_at: now,
        version: 0,
    };
    let stored = store.create(&job).map_err(DriveError::Store)?;
    let added = match input.access {
        Access::Write => worktree::add_write(
            &root,
            &worktree_path,
            stored.branch.as_deref().unwrap_or_default(),
            &stored.base_sha,
        ),
        Access::Readonly | Access::Exec => {
            worktree::add_detached(&root, &worktree_path, &stored.base_sha)
        }
    };
    if let Err(err) = added {
        let _ = fs::remove_dir_all(store.job_dir(&input.id));
        return Err(DriveError::Git(err));
    }
    Ok((store, stored))
}

/// Drives one queued job through one attempt of its harness and lands it: awaiting_review for
/// write access, completed otherwise, failed on a broken run, cancelled on a stop file.
pub fn drive(store: &JobStore, job_id: &str, env: &Env) -> Result<Job, DriveError> {
    let (mut job, index) = claim(store, job_id)?;
    let dir = store.job_dir(job_id);
    write_driver_record(&dir)?;
    let turn = run_attempt(&job, index, &dir, env);
    let event = finish(&mut job, index, turn, &dir, env);
    apply_outcome(&mut job, index, event)?;
    let job = land(store, job, event)?;
    remove_throwaway_worktree(&job);
    clear_run_files(&dir);
    Ok(job)
}

/// Reads the record, refuses a job that is not queued, clears a stale stop file, prepares the
/// attempt to run, and moves the job to running. A lost claim (another driver updated first)
/// is a store error.
fn claim(store: &JobStore, job_id: &str) -> Result<(Job, usize), DriveError> {
    let mut job = store.get(job_id).map_err(DriveError::Store)?;
    if job.state != JobState::Queued {
        return Err(DriveError::WrongState {
            id: job.id.clone(),
            state: job.state,
        });
    }
    // A stop file a previous run left behind must not stop this one.
    let _ = fs::remove_file(store.job_dir(job_id).join("stop"));
    let index = match job.pending_attempt() {
        Some(index) => index,
        None => {
            job.attempts.push(Attempt {
                n: job.attempts.len() as u32 + 1,
                kind: AttemptKind::Initial,
                prompt: report::initial_prompt(&job.task, job.access),
                feedback: None,
                session_id: None,
                started_at: iso_ms(now_ms()),
                ended_at: None,
                outcome: None,
                report: None,
                report_text: None,
            });
            job.attempts.len() - 1
        }
    };
    let now = iso_ms(now_ms());
    job.attempts[index].started_at = now.clone();
    job.updated_at = now;
    job.state = model::next_state(job.state, job.access, JobEvent::Started)
        .map_err(|err| DriveError::Io(err.to_string()))?;
    let stored = store.update(&job).map_err(DriveError::Store)?;
    Ok((stored, index))
}

/// Writes the driver's own process record, so the sweep can tell a live driver from a dead one.
fn write_driver_record(job_dir: &Path) -> Result<(), DriveError> {
    let pid = std::process::id();
    let record = ProcessRecord {
        pid,
        start_token: start_token(pid).unwrap_or_default(),
    };
    liveness::write_record(&liveness::driver_record_path(job_dir), &record)
        .map_err(|err| DriveError::Io(err.to_string()))
}

/// One run's result and the pieces the attempt keeps from it.
struct Turn {
    run: Result<Outcome, String>,
    /// The key a new session started with, when one did.
    new_key: Option<String>,
    /// The entry runs sessions for the caller, so a return can resume one.
    caller_owned: bool,
}

impl Turn {
    fn failed(message: String) -> Turn {
        Turn {
            run: Err(message),
            new_key: None,
            caller_owned: false,
        }
    }
}

/// Finds the job's harness entry and runs one turn of it in the worktree. Every failure of the
/// setup and of the run itself is the run's error text, never a drive error: the job carries it.
fn run_attempt(job: &Job, index: usize, job_dir: &Path, env: &Env) -> Turn {
    let attempt = &job.attempts[index];
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(err) => return Turn::failed(err.to_string()),
    };
    let entries = match user_entries::all_entries(&paths.config_dir.join("harnesses.json")) {
        Ok(entries) => entries,
        Err(err) => return Turn::failed(err.to_string()),
    };
    let Some(entry) = entries.iter().find(|entry| entry.id == job.harness) else {
        return Turn::failed(format!("no harness named \"{}\"", job.harness));
    };
    let (session, new_key) = match &attempt.session_id {
        Some(id) => (SessionRef::Resume { id: id.clone() }, None),
        None => match runner::new_session_key() {
            Ok(key) => (SessionRef::New { key: key.clone() }, Some(key)),
            Err(err) => return Turn::failed(err.to_string()),
        },
    };
    let brief = Brief {
        prompt: attempt.prompt.clone(),
        access: harness_access(job.access),
        effort: job.effort.clone(),
        session,
    };
    let run = run_with_stop_watch(
        entry,
        &brief,
        Path::new(&job.worktree),
        &job_dir.join(format!("attempt-{}", attempt.n)),
        job_dir,
        env,
    );
    Turn {
        run,
        new_key,
        caller_owned: entry.run.session.owner == SessionOwner::Caller,
    }
}

/// Runs the turn on this thread while a watcher thread polls the job's stop file; a sighting
/// interrupts the harness's process group once, as a stop. The run's end ends the watch first,
/// so a stop file that appears after the run never changes the outcome.
fn run_with_stop_watch(
    entry: &Entry,
    brief: &Brief,
    cwd: &Path,
    log_dir: &Path,
    job_dir: &Path,
    env: &Env,
) -> Result<Outcome, String> {
    let stop_file = job_dir.join("stop");
    let slot: Mutex<Option<StopHandle>> = Mutex::new(None);
    let done = AtomicBool::new(false);
    let on_spawn = |pid: u32, stop: StopHandle| {
        let record = ProcessRecord {
            pid,
            start_token: start_token(pid).unwrap_or_default(),
        };
        let _ = liveness::write_record(&liveness::harness_record_path(job_dir), &record);
        *slot.lock().unwrap_or_else(PoisonError::into_inner) = Some(stop);
    };
    let mut ctx = RunCtx::new(env, cwd.to_path_buf(), log_dir.to_path_buf());
    ctx.on_spawn = Some(&on_spawn);
    thread::scope(|scope| {
        let watcher = scope.spawn(|| watch_stop_file(&stop_file, &slot, &done));
        let run = runner::run_turn(entry, brief, &ctx);
        done.store(true, Ordering::Relaxed);
        let _ = watcher.join();
        run
    })
    .map_err(|err| err.to_string())
}

/// Watches the job's stop file until it appears (the harness group is interrupted once) or the
/// run ends. A sighting before the harness spawned keeps waiting for the handle.
fn watch_stop_file(stop_file: &Path, slot: &Mutex<Option<StopHandle>>, done: &AtomicBool) {
    loop {
        if done.load(Ordering::Relaxed) {
            return;
        }
        if stop_file.exists() {
            let stop = slot.lock().unwrap_or_else(PoisonError::into_inner).take();
            if let Some(stop) = stop {
                stop.interrupt(Forced::Stopped);
                return;
            }
        }
        thread::sleep(STOP_POLL);
    }
}

/// Lands the run's result in the attempt: the outcome, the report text, the report, the
/// session, and the end time. The event follows the outcome alone, never a stop file. A
/// completed attempt runs its checks first; a check that cannot run is the attempt's error.
fn finish(job: &mut Job, index: usize, turn: Turn, dir: &Path, env: &Env) -> JobEvent {
    let now = iso_ms(now_ms());
    let mut outcome = match turn.run {
        Err(message) => AttemptOutcome::Error { message },
        Ok(run) => {
            if !run.report_text.is_empty() {
                job.attempts[index].report_text = Some(run.report_text.clone());
            }
            job.attempts[index].report = report::extract_report(&run.report_text);
            job.attempts[index].session_id = run.session_id.or(match turn.new_key {
                Some(key) if turn.caller_owned => Some(key),
                _ => None,
            });
            outcome_of(&run.exit, run.failure.as_deref())
        }
    };
    if outcome == AttemptOutcome::Completed
        && let Err(error) = run_checks(job, index, dir, env)
    {
        outcome = AttemptOutcome::Error {
            message: format!("the checks could not run: {error}"),
        };
    }
    let attempt = &mut job.attempts[index];
    attempt.outcome = Some(outcome.clone());
    attempt.ended_at = Some(now);
    match outcome {
        AttemptOutcome::Completed => JobEvent::Finished,
        AttemptOutcome::Stopped => JobEvent::Cancelled,
        AttemptOutcome::Crashed { .. } | AttemptOutcome::Error { .. } | AttemptOutcome::Timeout => {
            JobEvent::Failed
        }
    }
}

/// Runs a completed attempt's checks in its worktree and writes the verification into the job
/// directory. No checks: nothing runs. The verdict does not change the outcome: a failing gate
/// still lands for review.
fn run_checks(job: &Job, index: usize, dir: &Path, env: &Env) -> Result<(), String> {
    let checks = checks::read_checks(dir).map_err(|error| error.to_string())?;
    if checks.is_empty() {
        return Ok(());
    }
    let verification = verify::verify_worktree(
        &checks,
        job.access,
        Path::new(&job.worktree),
        &job.base_sha,
        &harness_env(env, &[]),
    )
    .map_err(|error| error.to_string())?;
    checks::write_verification(dir, job.attempts[index].n, &verification)
        .map_err(|error| error.to_string())
}

/// The job fields the attempt's end brings: the failure text, the cancel decision, the state.
fn apply_outcome(job: &mut Job, index: usize, event: JobEvent) -> Result<(), DriveError> {
    if event == JobEvent::Failed
        && let Some(outcome) = job.attempts[index].outcome.as_ref()
    {
        job.failure = failure_text(outcome);
    }
    if event == JobEvent::Cancelled {
        job.decision = Some(Decision {
            kind: JobState::Cancelled,
            at: iso_ms(now_ms()),
            commit: None,
        });
    }
    job.state = model::next_state(job.state, job.access, event)
        .map_err(|err| DriveError::Io(err.to_string()))?;
    Ok(())
}

/// The reason a failed job carries: the error's message, the crash's exit or signal, or the
/// timeout.
fn failure_text(outcome: &AttemptOutcome) -> Option<String> {
    match outcome {
        AttemptOutcome::Error { message } => Some(message.clone()),
        AttemptOutcome::Crashed {
            exit_code, signal, ..
        } => Some(match (exit_code, signal) {
            (Some(code), _) => format!("crashed (exit {code})"),
            (None, Some(signal)) => format!("killed by {signal}"),
            (None, None) => "crashed".to_string(),
        }),
        AttemptOutcome::Timeout => Some("timed out".to_string()),
        AttemptOutcome::Completed | AttemptOutcome::Stopped => None,
    }
}

/// Persists the finished job. A record another writer changed is re-read once: a terminal
/// state there stands, otherwise the run's fields go onto the fresh record.
fn land(store: &JobStore, job: Job, event: JobEvent) -> Result<Job, DriveError> {
    match store.update(&job) {
        Ok(updated) => Ok(updated),
        Err(StoreError::VersionConflict { .. }) => {
            let mut fresh = store.get(&job.id).map_err(DriveError::Store)?;
            if fresh.state.is_terminal() {
                return Ok(fresh);
            }
            fresh.attempts = job.attempts;
            fresh.failure = job.failure;
            fresh.decision = job.decision;
            fresh.state = model::next_state(fresh.state, fresh.access, event)
                .map_err(|err| DriveError::Io(err.to_string()))?;
            store.update(&fresh).map_err(DriveError::Store)
        }
        Err(err) => Err(DriveError::Store(err)),
    }
}

/// Removes the worktree of a job that keeps nothing: any access but write, unless cancelled.
/// A failure is left to the sweep.
fn remove_throwaway_worktree(job: &Job) {
    if job.access != Access::Write && job.state != JobState::Cancelled {
        let _ = worktree::remove(Path::new(&job.repo_root), Path::new(&job.worktree), None);
    }
}

/// Removes the stop file and the process records a run leaves behind; each may be missing.
fn clear_run_files(job_dir: &Path) {
    let _ = fs::remove_file(job_dir.join("stop"));
    let _ = fs::remove_file(liveness::driver_record_path(job_dir));
    let _ = fs::remove_file(liveness::harness_record_path(job_dir));
}

/// The attempt's outcome from the run's exit alone: a forced stop or timeout wins, then the
/// exit code and the failure line decide between completed, an error, and a crash.
pub fn outcome_of(exit: &ExitInfo, failure: Option<&str>) -> AttemptOutcome {
    match exit.forced.as_deref() {
        Some("stopped") => return AttemptOutcome::Stopped,
        Some("timeout") => return AttemptOutcome::Timeout,
        _ => {}
    }
    if exit.code == Some(0) && failure.is_none() {
        return AttemptOutcome::Completed;
    }
    match failure {
        Some(message) => AttemptOutcome::Error {
            message: message.to_string(),
        },
        None => AttemptOutcome::Crashed {
            exit_code: exit.code,
            signal: exit.signal.clone(),
            stderr_tail: exit.stderr_tail.clone(),
        },
    }
}

/// The model's access in the harness table's terms.
fn harness_access(access: Access) -> HarnessAccess {
    match access {
        Access::Readonly => HarnessAccess::Readonly,
        Access::Exec => HarnessAccess::Exec,
        Access::Write => HarnessAccess::Write,
    }
}

/// Starts the detached process that drives the job: the huddle binary re-run for the job
/// directory, its output appended to the job's driver log.
pub fn spawn_driver(exe: &Path, job_dir: &Path, env: &Env) -> Result<(), DetachError> {
    detached::start_detached(&DetachSpec {
        program: exe.to_path_buf(),
        args: vec![
            "run".to_string(),
            "--drive".to_string(),
            job_dir.to_string_lossy().into_owned(),
        ],
        env: env.pairs(),
        log_file: Some(job_dir.join("driver.log")),
        cwd: job_dir.to_path_buf(),
    })
}
