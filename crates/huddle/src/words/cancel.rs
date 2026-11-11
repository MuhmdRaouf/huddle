//! The cancel word: stop a job and mark it cancelled; its folder stays as it is.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_jobs::cancel::{self, CancelError};
use huddle_jobs::git;
use huddle_jobs::store::{JobStore, StoreError};

use super::run::parse_args;

const USAGE: &str = "usage: huddle cancel <id> [--json]";

pub fn run(args: &[String], env: &Env) -> i32 {
    let parsed = match parse_args(args, &["json", "help"], &[]) {
        Ok(parsed) => parsed,
        Err(message) => {
            let _ = writeln!(std::io::stderr(), "huddle: {message}\n{USAGE}");
            return 2;
        }
    };
    if parsed.has("help") {
        let _ = writeln!(std::io::stdout(), "{USAGE}");
        return 0;
    }
    if parsed.positionals.len() != 1 {
        let _ = writeln!(std::io::stderr(), "huddle: cancel needs a job id\n{USAGE}");
        return 2;
    }
    let id = parsed.positionals[0].as_str();
    let cwd = match std::env::current_dir() {
        Ok(cwd) => cwd,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let root = match git::main_root(&cwd) {
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
    match cancel::cancel(&store, id, &cancel::CANCEL_OPTIONS) {
        Ok(outcome) => {
            if parsed.has("json") {
                let line = match serde_json::to_string_pretty(&outcome.job) {
                    Ok(line) => line,
                    Err(error) => {
                        let _ = writeln!(std::io::stderr(), "huddle: {error}");
                        return 1;
                    }
                };
                let _ = writeln!(std::io::stdout(), "{line}");
            } else if outcome.forced_driver {
                let _ = writeln!(
                    std::io::stdout(),
                    "huddle job {} cancelled: its driver did not stop on its own and was terminated; the folder is untouched",
                    outcome.job.id
                );
            } else {
                let _ = writeln!(
                    std::io::stdout(),
                    "huddle job {} cancelled: the folder is untouched",
                    outcome.job.id
                );
            }
            0
        }
        Err(error @ CancelError::WrongState { .. }) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            2
        }
        Err(CancelError::Store(
            error @ (StoreError::NotFound { .. } | StoreError::Ambiguous { .. }),
        )) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            4
        }
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            1
        }
    }
}
