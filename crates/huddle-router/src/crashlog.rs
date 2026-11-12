//! huddle-router: crashlog.

use std::any::Any;
use std::fs;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use crate::clock;

/// The largest `router.log` in bytes before the next line rotates it to `router.log.1`.
pub const ROUTER_LOG_LIMIT: u64 = 4 * 1024 * 1024;

/// The largest `router-events.log` in bytes before the next line rotates it.
pub const EVENTS_LOG_LIMIT: u64 = 1024 * 1024;

/// Rotated files kept beside each log: `.1` is the newest, the one past `.5` is deleted.
pub const LOG_KEEP: usize = 5;

/// The prefix of every event line; a request line starts with its HTTP method.
const EVENT_PREFIX: &str = "huddle-router:";

/// One log line, handed to the sink the caller chose.
pub type Logger = Arc<dyn Fn(String) + Send + Sync>;

/// The router's two logs in `state_dir`: every line goes to `router.log`, and each event line
/// (start, exit, handover, path failing, probe, recovered, reload, restart reason, crash) also
/// goes to `router-events.log`, which request lines never fill.
pub fn router_logs(state_dir: &Path) -> Logger {
    let all = rotating_log(&state_dir.join("router.log"), ROUTER_LOG_LIMIT);
    let events = rotating_log(&state_dir.join("router-events.log"), EVENTS_LOG_LIMIT);
    Arc::new(move |line: String| {
        if is_event(&line) {
            events(line.clone());
        }
        all(line);
    })
}

/// Whether `line` (without its timestamp) is an event line.
pub fn is_event(line: &str) -> bool {
    line.starts_with(EVENT_PREFIX)
}

/// Appends `<ISO ts> <line>\n` to `path` (0644). Before the append that would pass `limit`
/// it rotates `path` to `<path>.1`, shifting older files up and deleting the one past
/// `LOG_KEEP`. The size comes from the disk and the rotation runs under an exclusive lock on
/// `<path>.lock`, so two routers writing the same file during a handover rotate it once and
/// lose no line. Never fails: a full disk costs the line. Does not recreate a removed
/// directory.
pub fn rotating_log(path: &Path, limit: u64) -> Logger {
    let path = path.to_path_buf();
    Arc::new(move |line: String| {
        let text = format!("{} {}\n", clock::iso_utc_ms(clock::now_ms()), line);
        if size_of(&path) + text.len() as u64 > limit {
            rotate(&path, limit, text.len() as u64);
        }
        let opened = fs::OpenOptions::new()
            .append(true)
            .create(true)
            .mode(huddle_config::files::FILE_MODE)
            .open(&path);
        if let Ok(mut file) = opened {
            let _ = file.write_all(text.as_bytes());
        }
    })
}

fn size_of(path: &Path) -> u64 {
    fs::metadata(path).map(|meta| meta.len()).unwrap_or(0)
}

/// Rotates `path` unless a writer holding the lock before us already did.
fn rotate(path: &Path, limit: u64, incoming: u64) {
    let lock = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .mode(huddle_config::files::FILE_MODE)
        .open(sibling(path, ".lock"));
    let Ok(lock) = lock else {
        return;
    };
    if lock.lock().is_err() {
        return;
    }
    if size_of(path) + incoming > limit {
        let _ = fs::remove_file(rolled_path(path, LOG_KEEP));
        for number in (1..LOG_KEEP).rev() {
            let _ = fs::rename(rolled_path(path, number), rolled_path(path, number + 1));
        }
        let _ = fs::rename(path, rolled_path(path, 1));
    }
}

/// `router.log.<number>` beside `path`.
fn rolled_path(path: &Path, number: usize) -> PathBuf {
    sibling(path, &format!(".{number}"))
}

fn sibling(path: &Path, suffix: &str) -> PathBuf {
    let mut name = path.as_os_str().to_os_string();
    name.push(suffix);
    PathBuf::from(name)
}

/// Installs a panic hook that writes one crash line through `log`, then returns; the process
/// ends on its own.
pub fn install_crash_hook(log: &Logger) {
    let log = Arc::clone(log);
    std::panic::set_hook(Box::new(move |info| {
        let (file, line) = match info.location() {
            Some(place) => (place.file().to_string(), place.line()),
            None => ("<unknown>".to_string(), 0),
        };
        log(crash_line(&payload_message(info.payload()), &file, line));
    }));
}

/// The crash line for a message and a location.
pub fn crash_line(message: &str, file: &str, line: u32) -> String {
    format!("huddle-router: crash: {message} at {file}:{line}")
}

/// The panic's message: a `&str` or `String` payload as itself, anything else unnamed.
fn payload_message(payload: &(dyn Any + Send)) -> String {
    if let Some(text) = payload.downcast_ref::<&str>() {
        (*text).to_string()
    } else if let Some(text) = payload.downcast_ref::<String>() {
        text.clone()
    } else {
        "<unknown>".to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::{fs, os::unix::fs::PermissionsExt};

    #[test]
    fn a_line_lands_with_an_iso_timestamp_and_a_private_mode() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, ROUTER_LOG_LIMIT);
        log("hello".to_string());
        let text = fs::read_to_string(&path).expect("read");
        assert!(text.ends_with(" hello\n"), "{text}");
        assert_eq!(
            text.split(' ').next().expect("ts").len(),
            "2026-11-09T10:15:30.123Z".len()
        );
        assert_eq!(
            fs::metadata(&path).expect("stat").permissions().mode() & 0o777,
            0o644
        );
    }

    #[test]
    fn a_line_past_the_limit_rotates_first() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, 100);
        log("a".repeat(50));
        log("b".repeat(50));
        let rolled = fs::read_to_string(home.root().join("router.log.1")).expect("rolled");
        assert!(rolled.contains(&"a".repeat(50)));
        let current = fs::read_to_string(&path).expect("current");
        assert!(current.contains(&"b".repeat(50)));
        assert!(!current.contains(&"a".repeat(50)));
    }

    #[test]
    fn a_removed_directory_is_not_recreated() {
        let home = TempHome::new();
        let dir = home.root().join("gone");
        let log = rotating_log(&dir.join("router.log"), ROUTER_LOG_LIMIT);
        log("line".to_string());
        assert!(!dir.exists());
    }

    #[test]
    fn a_full_disk_costs_the_line_only() {
        let home = TempHome::new();
        let dir = home.root().join("logdir");
        fs::create_dir(&dir).expect("mkdir");
        let log = rotating_log(&dir, 100);
        log("line".to_string());
    }

    #[test]
    fn the_crash_line_names_message_and_place() {
        assert_eq!(
            crash_line("boom", "src/x.rs", 7),
            "huddle-router: crash: boom at src/x.rs:7"
        );
    }

    #[test]
    fn the_hook_writes_a_crash_line_before_the_panic_unwinds() {
        let lines = Arc::new(std::sync::Mutex::new(Vec::new()));
        let sink = Arc::clone(&lines);
        let log: Logger = Arc::new(move |line: String| sink.lock().expect("lines").push(line));
        let previous = std::panic::take_hook();
        install_crash_hook(&log);
        let result = std::panic::catch_unwind(|| panic!("boom"));
        std::panic::set_hook(previous);
        assert!(result.is_err());
        let text = lines.lock().expect("lines").join("\n");
        assert!(text.contains("huddle-router: crash: boom at "), "{text}");
        assert!(text.contains("crashlog.rs:"), "{text}");
    }

    #[test]
    fn k17_event_lines_go_to_the_events_log_and_requests_do_not() {
        let home = TempHome::new();
        let log = router_logs(home.root());
        log("POST /v1/messages model=- -> anthropic 200".to_string());
        log("huddle-router: Anthropic path failing (ETIMEDOUT)".to_string());
        let events = fs::read_to_string(home.root().join("router-events.log")).unwrap_or_default();
        assert!(events.contains("Anthropic path failing"), "{events}");
        assert!(!events.contains("POST /v1/messages"), "{events}");
        let requests = fs::read_to_string(home.root().join("router.log")).expect("router.log");
        assert!(requests.contains("POST /v1/messages"), "{requests}");
        assert!(requests.contains("Anthropic path failing"), "{requests}");
    }

    #[test]
    fn k17_router_log_keeps_five_old_files() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, 100);
        for round in 0..9 {
            log(format!("{round}{}", "x".repeat(60)));
        }
        let first_of = |name: &str| {
            fs::read_to_string(home.root().join(name))
                .unwrap_or_default()
                .split(' ')
                .nth(1)
                .unwrap_or("")
                .chars()
                .next()
        };
        assert_eq!(first_of("router.log"), Some('8'));
        assert_eq!(first_of("router.log.1"), Some('7'));
        assert_eq!(first_of("router.log.5"), Some('3'));
        assert!(!home.root().join("router.log.6").exists());
    }

    #[test]
    fn k17_two_writers_never_lose_a_line_across_a_rotation() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let writers: Vec<_> = ["a", "b"]
            .into_iter()
            .map(|tag| {
                let log = rotating_log(&path, 2000);
                std::thread::spawn(move || {
                    for round in 0..40 {
                        log(format!("{tag}{round:02}"));
                    }
                })
            })
            .collect();
        for writer in writers {
            writer.join().expect("writer");
        }
        let mut all = String::new();
        for name in [
            "router.log",
            "router.log.1",
            "router.log.2",
            "router.log.3",
            "router.log.4",
            "router.log.5",
        ] {
            all.push_str(&fs::read_to_string(home.root().join(name)).unwrap_or_default());
        }
        for tag in ["a", "b"] {
            for round in 0..40 {
                let want = format!(" {tag}{round:02}\n");
                assert_eq!(all.matches(&want).count(), 1, "{want} in {all}");
            }
        }
    }
}
