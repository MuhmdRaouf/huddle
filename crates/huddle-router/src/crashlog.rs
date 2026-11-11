//! huddle-router: crashlog.

use std::any::Any;
use std::fs;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use crate::clock;

/// The largest `router.log` in bytes before the next line rotates it to `router.log.1`.
pub const ROUTER_LOG_LIMIT: u64 = 4 * 1024 * 1024;

/// One log line, handed to the sink the caller chose.
pub type Logger = Arc<dyn Fn(String) + Send + Sync>;

/// Appends `<ISO ts> <line>\n` to `path` (0600). Renames to `<path>.1` before the append that
/// would pass `limit`. Never fails: a full disk costs the line. Does not recreate a removed
/// directory.
pub fn rotating_log(path: &Path, limit: u64) -> Logger {
    let path = path.to_path_buf();
    let written = Mutex::new(fs::metadata(&path).map(|meta| meta.len()).unwrap_or(0));
    Arc::new(move |line: String| {
        let text = format!("{} {}\n", clock::iso_utc_ms(clock::now_ms()), line);
        let mut written = written.lock().unwrap_or_else(|locked| locked.into_inner());
        if *written + text.len() as u64 > limit {
            let _ = fs::rename(&path, rolled_path(&path));
            *written = 0;
        }
        let opened = fs::OpenOptions::new()
            .append(true)
            .create(true)
            .mode(huddle_config::files::FILE_MODE)
            .open(&path);
        if let Ok(mut file) = opened {
            let _ = file.write_all(text.as_bytes());
        }
        *written += text.len() as u64;
    })
}

/// `router.log.1` beside `path`: the file a rotation renames it to.
fn rolled_path(path: &Path) -> PathBuf {
    let mut name = path.as_os_str().to_os_string();
    name.push(".1");
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
}
