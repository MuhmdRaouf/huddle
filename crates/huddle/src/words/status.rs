//! The status word: run the liveness sweep, then print the job table for this repository or one
//! job in full with the last lines of its attempt log.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::table::Wire;
use huddle_harness::user_entries::all_entries;
use huddle_jobs::git;
use huddle_jobs::id::now_ms;
use huddle_jobs::liveness;
use huddle_jobs::model::Job;
use huddle_jobs::render;
use huddle_jobs::store::{JobStore, StoreError};
use serde::Serialize;

use super::run::parse_args;

const USAGE: &str = "usage: huddle status [<id>] [--json]";
/// Log lines a job detail shows; the detail header names the same count.
const LOG_LINES: usize = 4;
/// Chars of a log line the detail keeps.
const LOG_LINE_CHARS: usize = 160;

pub fn run(args: &[String], env: &Env) -> i32 {
    let parsed = match parse_args(args, &["json", "help"], &[]) {
        Ok(parsed) => parsed,
        Err(message) => {
            let _ = writeln!(std::io::stderr(), "huddle: {message}");
            let _ = writeln!(std::io::stderr(), "{USAGE}");
            return 2;
        }
    };
    if parsed.has("help") {
        let _ = writeln!(std::io::stdout(), "{USAGE}");
        return 0;
    }
    if parsed.positionals.len() > 1 {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: status takes at most one job id\n{USAGE}"
        );
        return 2;
    }
    let cwd = match std::env::current_dir() {
        Ok(cwd) => cwd,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let root = match git::root(&cwd) {
        Ok(root) => root,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let store = JobStore::for_repo(&paths, &root);
    let now = now_ms();
    if let Err(error) = liveness::sweep(&store, now) {
        let _ = writeln!(std::io::stderr(), "huddle: {error}");
        return 1;
    }
    let Some(id) = parsed.positionals.first().map(String::as_str) else {
        let jobs = match store.list() {
            Ok(jobs) => jobs,
            Err(error) => {
                let _ = writeln!(std::io::stderr(), "huddle: {error}");
                return 1;
            }
        };
        if parsed.has("json") {
            return print_json(&jobs);
        }
        let _ = writeln!(std::io::stdout(), "{}", render::status_table(&jobs, now));
        return 0;
    };
    let job = match store.find(id) {
        Ok(job) => job,
        Err(error @ (StoreError::NotFound { .. } | StoreError::Ambiguous { .. })) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 4;
        }
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    if parsed.has("json") {
        return print_json(&job);
    }
    let tail = log_tail(&store, &paths, &job);
    let _ = writeln!(
        std::io::stdout(),
        "{}",
        render::job_detail(&job, &tail, now)
    );
    0
}

/// Pretty JSON on stdout, or the error on stderr with exit 1.
fn print_json<T: Serialize>(value: &T) -> i32 {
    match serde_json::to_string_pretty(value) {
        Ok(text) => {
            let _ = writeln!(std::io::stdout(), "{text}");
            0
        }
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            1
        }
    }
}

/// The last non-empty lines of the latest attempt's log: the RPC frame file for an RPC harness,
/// stdout.log otherwise. No attempt or a missing file is no lines.
fn log_tail(store: &JobStore, paths: &Paths, job: &Job) -> Vec<String> {
    let Some(attempt) = job.attempts.last() else {
        return Vec::new();
    };
    let dir = store
        .job_dir(&job.id)
        .join(format!("attempt-{}", attempt.n));
    let file = if is_rpc_harness(paths, &job.harness) {
        "rpc-frames.jsonl"
    } else {
        "stdout.log"
    };
    let Ok(bytes) = std::fs::read(dir.join(file)) else {
        return Vec::new();
    };
    let text = String::from_utf8_lossy(&bytes);
    let lines: Vec<&str> = text
        .lines()
        .filter(|line| !line.trim().is_empty())
        .collect();
    let start = lines.len().saturating_sub(LOG_LINES);
    lines[start..]
        .iter()
        .map(|line| render::one_line(line, LOG_LINE_CHARS))
        .collect()
}

/// The harness speaks the RPC protocol: its attempt log is the frame file, not stdout. An
/// unreadable harness list or an unknown harness id reads as a text harness.
fn is_rpc_harness(paths: &Paths, harness: &str) -> bool {
    let Ok(entries) = all_entries(&paths.config_dir.join("harnesses.toml")) else {
        return false;
    };
    match entries.iter().find(|entry| entry.id == harness) {
        Some(entry) => matches!(entry.wire, Wire::OmpRpc { .. } | Wire::PiRpc { .. }),
        None => false,
    }
}
