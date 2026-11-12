//! The `huddle router log` word: the router's event lines, or its request lines, read from the
//! log files. It needs no router.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_router::clock::{iso_utc_ms, now_ms};
use huddle_router::crashlog::{LOG_KEEP, is_event};

const USAGE: &str = "usage: huddle router log [--since <n>m|<n>h] [--requests | --all]";
const LAST_LINES: usize = 40;

#[derive(Clone, Copy, PartialEq)]
enum Kind {
    Events,
    Requests,
    All,
}

struct Query {
    kind: Kind,
    /// Oldest ISO timestamp wanted; ISO UTC strings order like the times they name.
    since: Option<String>,
}

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

/// `30m` or `2h` in milliseconds.
fn window_ms(text: &str) -> Option<i64> {
    let (digits, unit) = text.split_at(text.len().checked_sub(1)?);
    let count: i64 = digits.parse().ok().filter(|count| *count > 0)?;
    match unit {
        "m" => count.checked_mul(60_000),
        "h" => count.checked_mul(3_600_000),
        _ => None,
    }
}

/// The lines to print, oldest first.
fn lines_of(state_dir: &Path, query: &Query) -> Vec<String> {
    let requests = || tail(&state_dir.join("router.log"), query, |text| !is_event(text));
    let events = || tail(&state_dir.join("router-events.log"), query, is_event);
    let mut lines = match query.kind {
        Kind::Events => events(),
        Kind::Requests => requests(),
        Kind::All => {
            // router.log carries the event lines too; the events log keeps them longer.
            let mut merged = requests();
            merged.extend(events());
            merged.sort_by(|a, b| stamp(a).cmp(stamp(b)));
            merged
        }
    };
    if query.since.is_none() && lines.len() > LAST_LINES {
        lines.drain(..lines.len() - LAST_LINES);
    }
    lines
}

/// The wanted lines of `path` and its rotated files, oldest first. Older files are read only
/// while the lines found do not yet cover the query.
fn tail(path: &Path, query: &Query, wanted: impl Fn(&str) -> bool) -> Vec<String> {
    let mut found: Vec<String> = Vec::new();
    for file in files_newest_first(path) {
        let Ok(text) = fs::read_to_string(&file) else {
            continue;
        };
        let all: Vec<&str> = text.lines().collect();
        let reaches_back = match (&query.since, all.first()) {
            (Some(since), Some(first)) => stamp(first) < since.as_str(),
            _ => false,
        };
        let mut chosen: Vec<String> = all
            .into_iter()
            .filter(|line| wanted(text_of(line)))
            .filter(|line| {
                query
                    .since
                    .as_deref()
                    .is_none_or(|since| stamp(line) >= since)
            })
            .map(str::to_owned)
            .collect();
        chosen.append(&mut found);
        found = chosen;
        if reaches_back || (query.since.is_none() && found.len() >= LAST_LINES) {
            break;
        }
    }
    found
}

fn files_newest_first(path: &Path) -> Vec<PathBuf> {
    let mut files = vec![path.to_path_buf()];
    for number in 1..=LOG_KEEP {
        let mut name = path.as_os_str().to_os_string();
        name.push(format!(".{number}"));
        files.push(PathBuf::from(name));
    }
    files
}

/// The timestamp field of a line.
fn stamp(line: &str) -> &str {
    line.split_once(' ').map_or("", |(stamp, _)| stamp)
}

/// A line without its timestamp.
fn text_of(line: &str) -> &str {
    line.split_once(' ').map_or(line, |(_, text)| text)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(list: &[&str]) -> Vec<String> {
        list.iter().map(|text| (*text).to_owned()).collect()
    }

    #[test]
    fn a_window_is_minutes_or_hours() {
        assert_eq!(window_ms("30m"), Some(1_800_000));
        assert_eq!(window_ms("2h"), Some(7_200_000));
        assert_eq!(window_ms("0m"), None);
        assert_eq!(window_ms("5"), None);
        assert_eq!(window_ms("soon"), None);
        assert_eq!(window_ms(""), None);
    }

    #[test]
    fn an_unknown_flag_or_a_dangling_since_is_a_usage_error() {
        assert!(parse(&args(&["--nope"]), 0).is_none());
        assert!(parse(&args(&["--since"]), 0).is_none());
        assert!(parse(&args(&["--since", "10m"]), 3_600_000).is_some());
    }
}
