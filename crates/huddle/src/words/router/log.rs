//! The `huddle router log` word: the router's event lines, or its request lines, read from the
//! log files. It needs no router.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_mcp::router_log::{Kind, Query, lines_of, window_ms};
use huddle_router::clock::{iso_utc_ms, now_ms};

const USAGE: &str = "usage: huddle router log [--since <n>m|<n>h] [--requests | --all]";

pub fn run(args: &[String], env: &Env) -> i32 {
    let Some(query) = parse(args, now_ms()) else {
        let _ = writeln!(std::io::stderr(), "{USAGE}");
        return 2;
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let mut stdout = std::io::stdout().lock();
    for line in lines_of(&paths.state_dir, &query) {
        let _ = writeln!(stdout, "{line}");
    }
    0
}

fn parse(args: &[String], now: i64) -> Option<Query> {
    let mut kind = Kind::Events;
    let mut since = None;
    let mut rest = args.iter();
    while let Some(argument) = rest.next() {
        match argument.as_str() {
            "--requests" => kind = Kind::Requests,
            "--all" => kind = Kind::All,
            "--since" => since = Some(iso_utc_ms(now - window_ms(rest.next()?)?)),
            _ => return None,
        }
    }
    Some(Query { kind, since })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(list: &[&str]) -> Vec<String> {
        list.iter().map(|text| (*text).to_owned()).collect()
    }

    #[test]
    fn an_unknown_flag_or_a_dangling_since_is_a_usage_error() {
        assert!(parse(&args(&["--nope"]), 0).is_none());
        assert!(parse(&args(&["--since"]), 0).is_none());
        assert!(parse(&args(&["--since", "10m"]), 3_600_000).is_some());
    }

    #[test]
    fn the_shared_log_reader_agrees_with_the_router_on_event_lines() {
        assert_eq!(huddle_router::crashlog::LOG_KEEP, 5);
        let event = "huddle-router: probe";
        assert_eq!(
            huddle_router::crashlog::is_event(event),
            huddle_mcp::router_log::is_event(event)
        );
        let request = "POST /v1/messages 200";
        assert_eq!(
            huddle_router::crashlog::is_event(request),
            huddle_mcp::router_log::is_event(request)
        );
    }
}
