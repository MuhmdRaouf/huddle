//! The result word: one job's review packet, its landing, or its return to the harness.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_jobs::accept::{self, AcceptError, AcceptOutcome};
use huddle_jobs::checks;
use huddle_jobs::checks_render;
use huddle_jobs::driver;
use huddle_jobs::git::{self, GitError};
use huddle_jobs::id::now_ms;
use huddle_jobs::liveness;
use huddle_jobs::model::Job;
use huddle_jobs::render;
use huddle_jobs::return_job::{self, ReturnError};
use huddle_jobs::store::{JobStore, StoreError};

use super::run;

const USAGE: &str =
    "usage: huddle result <id> [--diff] [--accept [--no-commit]] [--return <feedback>] [--json]";

pub fn run(args: &[String], env: &Env) -> i32 {
    let parsed = match run::parse_args(
        args,
        &["diff", "accept", "no-commit", "json", "help"],
        &["return"],
    ) {
        Ok(parsed) => parsed,
        Err(message) => return usage_error(&message),
    };
    if parsed.has("help") {
        let _ = writeln!(std::io::stdout(), "{USAGE}");
        return 0;
    }
    if parsed.positionals.is_empty() {
        return usage_error("result needs a job id");
    }
    if parsed.positionals.len() > 1 {
        return usage_error("result takes one job id");
    }
    if parsed.has("no-commit") && !parsed.has("accept") {
        return usage_error("--no-commit needs --accept");
    }
    if parsed.has("accept") && parsed.value("return").is_some() {
        return usage_error("--accept and --return exclude each other");
    }
    let cwd = match std::env::current_dir() {
        Ok(cwd) => cwd,
        Err(error) => return fail(&error.to_string()),
    };
    let repo = match git::main_root(&cwd) {
        Ok(repo) => repo,
        Err(error) => return fail(&error.to_string()),
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let store = JobStore::for_repo(&paths, &repo);
    if let Err(error) = liveness::sweep(&store, now_ms()) {
        return fail(&error.to_string());
    }
    let id = &parsed.positionals[0];
    if parsed.has("accept") {
        return accept_job(&store, id, parsed.has("no-commit"), parsed.has("json"));
    }
    if let Some(feedback) = parsed.value("return") {
        return return_to_harness(&store, id, feedback, parsed.has("json"), env);
    }
    show_report(&store, id, parsed.has("diff"), parsed.has("json"))
}

/// Accepts the job in its folder (a commit, or the change left uncommitted under --no-commit).
fn accept_job(store: &JobStore, id: &str, no_commit: bool, json: bool) -> i32 {
    let outcome = if no_commit {
        accept::accept_no_commit(store, id)
    } else {
        accept::accept(store, id)
    };
    let outcome = match outcome {
        Ok(outcome) => outcome,
        Err(error) => return accept_failed(id, &error),
    };
    if json {
        return print_json(&outcome.job);
    }
    let line = accept_line(&outcome, no_commit);
    let _ = writeln!(std::io::stdout(), "{line}");
    0
}

/// Where the accepted work landed.
fn accept_line(outcome: &AcceptOutcome, no_commit: bool) -> String {
    let id = &outcome.job.id;
    let dir = &outcome.job.dir;
    if no_commit {
        return format!("huddle job {id} accepted: the change stays uncommitted in {dir}");
    }
    match outcome.commit.as_deref() {
        Some(sha) => {
            let short = sha.get(..12).unwrap_or(sha);
            format!("huddle job {id} accepted: commit {short} on the current branch of {dir}")
        }
        None => format!("huddle job {id} accepted: no changes to commit"),
    }
}

/// Reports an accept that did not happen: the job still awaits review.
fn accept_failed(id: &str, error: &AcceptError) -> i32 {
    let mut stderr = std::io::stderr();
    match error {
        AcceptError::Checks(_) => {
            let _ = writeln!(
                stderr,
                "huddle: {error}\nthe job still awaits review: fix it, or /huddle:result {id} \
                 --return \"<feedback>\"."
            );
            5
        }
        AcceptError::Git(GitError::WrongBranch { .. }) => {
            let _ = writeln!(stderr, "huddle: {error}");
            5
        }
        AcceptError::Git(GitError::Failed { command, .. }) if command.starts_with("git commit") => {
            let _ = writeln!(stderr, "huddle: {error}");
            let _ = writeln!(
                stderr,
                "the job still awaits review. Fix what the commit hook reports, then accept it \
                 again."
            );
            1
        }
        AcceptError::Git(_) => {
            let _ = writeln!(stderr, "huddle: {error}");
            1
        }
        AcceptError::WrongState { .. } => {
            let _ = writeln!(stderr, "huddle: {error}");
            2
        }
        AcceptError::Store(StoreError::NotFound { .. } | StoreError::Ambiguous { .. }) => {
            let _ = writeln!(stderr, "huddle: {error}");
            4
        }
        AcceptError::Store(_) => {
            let _ = writeln!(stderr, "huddle: {error}");
            1
        }
    }
}

/// Sends the job back with the feedback and starts its detached driver.
fn return_to_harness(store: &JobStore, id: &str, feedback: &str, json: bool, env: &Env) -> i32 {
    let job = match return_job::return_job(store, id, feedback) {
        Ok(job) => job,
        Err(error) => return return_failed(&error),
    };
    let exe = match std::env::current_exe() {
        Ok(exe) => exe,
        Err(error) => return cannot_start(&job.id, &error.to_string()),
    };
    if let Err(error) = driver::spawn_driver(&exe, &store.job_dir(&job.id), env) {
        return cannot_start(&job.id, &format!("{error:?}"));
    }
    if json {
        return print_json(&job);
    }
    let _ = writeln!(
        std::io::stdout(),
        "huddle job {} returned with feedback: attempt {} runs detached. /huddle:result {} once \
         it awaits review again.",
        job.id,
        job.attempts.len(),
        job.id
    );
    0
}

/// Reports a return that did not happen.
fn return_failed(error: &ReturnError) -> i32 {
    let mut stderr = std::io::stderr();
    match error {
        ReturnError::EmptyFeedback => {
            let _ = writeln!(stderr, "huddle: return needs feedback for the harness");
            2
        }
        ReturnError::WrongState { .. } | ReturnError::NotWrite { .. } => {
            let _ = writeln!(stderr, "huddle: {error}");
            2
        }
        ReturnError::Store(StoreError::NotFound { .. } | StoreError::Ambiguous { .. }) => {
            let _ = writeln!(stderr, "huddle: {error}");
            4
        }
        _ => {
            let _ = writeln!(stderr, "huddle: {error}");
            1
        }
    }
}

/// Prints the review packet: the report, the changes and the next command; --diff adds the
/// fenced patch. The last attempt's checks follow the packet; a corrupt verification fails the
/// command, so the evidence is never silently missing.
fn show_report(store: &JobStore, id: &str, with_diff: bool, json: bool) -> i32 {
    let job = match store.find(id) {
        Ok(job) => job,
        Err(error) => return store_failed(&error),
    };
    let view = match run::review_view(&job, with_diff) {
        Ok(view) => view,
        Err(error) => return fail(&error.to_string()),
    };
    if json {
        return print_json(&job);
    }
    let _ = writeln!(std::io::stdout(), "{}", render::result_packet(&job, &view));
    if let Some(attempt) = job.last_attempt() {
        match checks::read_verification(&store.job_dir(&job.id), attempt.n) {
            Ok(Some(v)) => {
                let _ = write!(
                    std::io::stdout(),
                    "\n{}",
                    checks_render::verification_section(&v)
                );
            }
            Ok(None) => {}
            Err(error) => {
                let _ = writeln!(
                    std::io::stderr(),
                    "huddle: the checks could not be read: {error}"
                );
                return 1;
            }
        }
    }
    0
}

/// One failed store lookup: the id was not found or was ambiguous, or the store itself broke.
fn store_failed(error: &StoreError) -> i32 {
    let _ = writeln!(std::io::stderr(), "huddle: {error}");
    match error {
        StoreError::NotFound { .. } | StoreError::Ambiguous { .. } => 4,
        _ => 1,
    }
}

/// The one line a queued job whose driver never started gets.
fn cannot_start(id: &str, error: &str) -> i32 {
    let _ = writeln!(
        std::io::stderr(),
        "huddle: job {id} is queued, but its driver could not start: {error}"
    );
    1
}

/// The job as JSON, on stdout; a serialisation failure is an unexpected failure.
fn print_json(job: &Job) -> i32 {
    match serde_json::to_string_pretty(job) {
        Ok(text) => {
            let _ = writeln!(std::io::stdout(), "{text}");
            0
        }
        Err(error) => fail(&error.to_string()),
    }
}

/// One `huddle: <message>` line on stderr, exit 1.
fn fail(message: &str) -> i32 {
    let _ = writeln!(std::io::stderr(), "huddle: {message}");
    1
}

/// The `huddle: <message>` line and the usage line on stderr, exit 2.
fn usage_error(message: &str) -> i32 {
    let _ = writeln!(std::io::stderr(), "huddle: {message}");
    let _ = writeln!(std::io::stderr(), "{USAGE}");
    2
}
