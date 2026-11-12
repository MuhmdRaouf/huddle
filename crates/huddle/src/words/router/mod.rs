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

/// Who asked for this start, from `--by <reason>`: letters, digits, spaces, `-` and `_`, at most
/// 40 characters, so a reason cannot forge a log line. Without one, `router start`.
pub(super) fn reason_of(args: &[String]) -> String {
    let Some(raw) = args
        .iter()
        .position(|argument| argument == "--by")
        .and_then(|at| args.get(at + 1))
    else {
        return "router start".to_owned();
    };
    let clean: String = raw
        .chars()
        .filter(|c| c.is_ascii_alphanumeric() || matches!(c, ' ' | '-' | '_'))
        .take(40)
        .collect();
    if clean.is_empty() {
        "router start".to_owned()
    } else {
        clean
    }
}
