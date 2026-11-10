pub mod serve;
pub mod start;
pub mod status;
pub mod stop;

use std::io::Write;

use huddle_config::env::Env;

const USAGE: &str = "usage: huddle router serve [--takeover] | start | status [--json] | stop";

pub fn run(args: &[String], env: &Env) -> i32 {
    let Some((word, rest)) = args.split_first() else {
        let _ = writeln!(std::io::stderr(), "{USAGE}");
        return 2;
    };
    match word.as_str() {
        "serve" => serve::run(rest, env),
        "start" => start::run(rest, env),
        "status" => status::run(rest, env),
        "stop" => stop::run(rest, env),
        _ => {
            let _ = writeln!(std::io::stderr(), "{USAGE}");
            2
        }
    }
}

/// The router port for one word; `None` means the failure is already on stderr and the word
/// answers 2.
pub(super) fn router_port(env: &Env) -> Option<u16> {
    match huddle_config::paths::router_port(env) {
        Ok(port) => Some(port),
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            None
        }
    }
}
