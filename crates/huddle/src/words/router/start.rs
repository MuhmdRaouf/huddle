//! The `huddle router start` word: one daemon for this home, spawned detached.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::start::{DEFAULT_BUDGET, StartOptions, start_by};

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
    };
    match start_by(&options, &super::reason_of(args)) {
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
