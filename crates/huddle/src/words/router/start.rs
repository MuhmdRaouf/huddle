//! The `huddle router start` word: one daemon for this home, spawned detached.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::start::{
    DEFAULT_BUDGET, IF_INSTALLED, StartOptions, UPDATE_WAIT, start_by, start_if_installed,
};
use huddle_setup::settings::ledger_path;

pub fn run(args: &[String], env: &Env) -> i32 {
    let Some(port) = super::router_port(env) else {
        return 2;
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let executable = match std::env::current_exe() {
        Ok(executable) => executable,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let options = StartOptions {
        env,
        paths: &paths,
        port,
        executable,
        budget: DEFAULT_BUDGET,
        update_wait: UPDATE_WAIT,
    };
    let by = super::reason_of(args);
    let started = if args.iter().any(|arg| arg == IF_INSTALLED) {
        start_if_installed(&options, &by, &ledger_path(&paths))
    } else {
        start_by(&options, &by)
    };
    match started {
        Ok(line) => {
            let _ = writeln!(std::io::stdout(), "{line}");
            0
        }
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            1
        }
    }
}
