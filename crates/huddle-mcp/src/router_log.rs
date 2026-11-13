//! The router's log lines: event lines or request lines, read from the log files. The
//! `huddle router log` word and the `huddle_router_log` tool both read through here.

use std::fs;
use std::path::{Path, PathBuf};

/// The prefix of every event line; a request line starts with its HTTP method.
const EVENT_PREFIX: &str = "huddle-router:";
/// Rotated files kept beside each log.
const LOG_KEEP: usize = 5;
/// The lines returned when no window is given.
pub const LAST_LINES: usize = 40;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Events,
    Requests,
    All,
}

#[derive(Debug)]
pub struct Query {
    pub kind: Kind,
    /// Oldest ISO timestamp wanted; ISO UTC strings order like the times they name.
    pub since: Option<String>,
}

/// `30m` or `2h` in milliseconds.
pub fn window_ms(text: &str) -> Option<i64> {
    let (digits, unit) = text.split_at(text.len().checked_sub(1)?);
    let count: i64 = digits.parse().ok().filter(|count| *count > 0)?;
    match unit {
        "m" => count.checked_mul(60_000),
        "h" => count.checked_mul(3_600_000),
        _ => None,
    }
}

/// Whether `line` (without its timestamp) is an event line.
pub fn is_event(line: &str) -> bool {
    line.starts_with(EVENT_PREFIX)
}

/// The lines to print, oldest first.
pub fn lines_of(state_dir: &Path, query: &Query) -> Vec<String> {
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
pub fn stamp(line: &str) -> &str {
    line.split_once(' ').map_or("", |(stamp, _)| stamp)
}

/// A line without its timestamp.
pub fn text_of(line: &str) -> &str {
    line.split_once(' ').map_or(line, |(_, text)| text)
}

#[cfg(test)]
mod tests {
    use super::*;

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
    fn event_lines_carry_the_router_prefix() {
        assert!(is_event("huddle-router: exit pid=1 reason=SIGTERM"));
        assert!(!is_event("POST /v1/messages 200"));
    }
}
