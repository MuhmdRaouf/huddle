//! huddle-router: reload. The daemon's providers.json poll: re-read the file each tick, say
//! when the lanes should change, and log a bad file once per distinct error.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use huddle_config::providers_file::{providers_path, read_enabled};

/// How often the daemon polls providers.json.
pub const POLL: Duration = Duration::from_secs(2);

/// What one poll tells the daemon to do with its lanes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ReloadAction {
    Keep,
    /// The enabled list changed and is non-empty: swap to these providers.
    Swap(Vec<String>),
}

/// Watches the router dir's providers.json. One poll per tick; the fingerprint decides whether
/// what the read brought becomes a lane change, while every read is taken so a broken file is
/// noticed and logged.
pub struct ProvidersWatch {
    router_dir: PathBuf,
    known: Vec<&'static str>,
    baseline: Fingerprint,
    last_error: Option<String>,
}

impl ProvidersWatch {
    /// Takes the baseline fingerprint before the first read: a provider enabled while the daemon
    /// starts changes the file after this snapshot, so the first tick swaps instead of missing it.
    pub fn new(router_dir: PathBuf, known: Vec<&'static str>) -> ProvidersWatch {
        let baseline = fingerprint(&providers_path(&router_dir));
        ProvidersWatch {
            router_dir,
            known,
            baseline,
            last_error: None,
        }
    }

    /// One poll. `log` is called at most once per distinct error: the same bad file logs one
    /// line, and a read that succeeds resets the log so a later break is reported again.
    pub fn tick(&mut self, log: &dyn Fn(String)) -> ReloadAction {
        match read_enabled(&self.router_dir, &self.known) {
            Err(err) => {
                let line = format!("huddle-router: {err}");
                if self.last_error.as_deref() != Some(line.as_str()) {
                    log(line.clone());
                    self.last_error = Some(line);
                }
                ReloadAction::Keep
            }
            Ok(names) => {
                self.last_error = None;
                let now = fingerprint(&providers_path(&self.router_dir));
                let changed = now != self.baseline;
                self.baseline = now;
                if changed && !names.is_empty() {
                    ReloadAction::Swap(names)
                } else {
                    // Nothing is enabled, or nothing changed: keep serving. Sessions still point
                    // at the router, and an empty answer would cut them off.
                    ReloadAction::Keep
                }
            }
        }
    }
}

/// What a watched file must keep for its poll to skip it: that it exists, its size and its mtime.
/// A missing file has one fingerprint like any other, so a file that appears is a change too.
#[derive(Debug, Clone, PartialEq, Eq)]
struct Fingerprint {
    exists: bool,
    len: u64,
    modified: SystemTime,
}

impl Default for Fingerprint {
    fn default() -> Fingerprint {
        Fingerprint {
            exists: false,
            len: 0,
            modified: SystemTime::UNIX_EPOCH,
        }
    }
}

fn fingerprint(path: &Path) -> Fingerprint {
    match fs::metadata(path) {
        Err(_) => Fingerprint::default(),
        Ok(meta) => Fingerprint {
            exists: true,
            len: meta.len(),
            modified: meta.modified().unwrap_or(SystemTime::UNIX_EPOCH),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::{cell::RefCell, fs, path::PathBuf, rc::Rc, thread, time::Duration};

    const KNOWN: [&str; 5] = ["zai", "kimi", "deepseek", "minimax", "qwen"];

    fn write_providers(dir: &PathBuf, text: &str) {
        fs::create_dir_all(dir).expect("mkdir");
        fs::write(dir.join("providers.json"), text).expect("write");
        // A second write in the same mtime tick must still move the fingerprint.
        thread::sleep(Duration::from_millis(20));
    }

    fn logged() -> (Rc<RefCell<Vec<String>>>, impl Fn(String)) {
        let lines = Rc::new(RefCell::new(Vec::new()));
        let sink = Rc::clone(&lines);
        (lines, move |line: String| sink.borrow_mut().push(line))
    }

    #[test]
    fn the_first_tick_after_a_provider_appears_swaps() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (_lines, log) = logged();
        assert_eq!(watch.tick(&log), ReloadAction::Keep, "no file yet");
        write_providers(&dir, "{\"providers\":[\"zai\"]}");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
    }

    #[test]
    fn a_file_that_did_not_change_keeps_the_lanes() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        write_providers(&dir, "{\"providers\":[\"zai\"]}");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (_lines, log) = logged();
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Keep,
            "the baseline was taken at new"
        );
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
    }

    #[test]
    fn enabling_a_second_provider_swaps_again() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (_lines, log) = logged();
        write_providers(&dir, "{\"providers\":[\"zai\"]}");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "{\"providers\":[\"zai\",\"kimi\"]}");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string(), "kimi".to_string()])
        );
    }

    #[test]
    fn disabling_every_provider_keeps_the_lanes() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (_lines, log) = logged();
        write_providers(&dir, "{\"providers\":[\"zai\"]}");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "{\"providers\":[]}");
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
    }

    #[test]
    fn a_malformed_file_is_logged_once_per_distinct_error_and_keeps_the_lanes() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (lines, log) = logged();
        write_providers(&dir, "{\"providers\":[\"zai\"]}");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "not json");
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
        let logged = lines.borrow().clone();
        assert_eq!(logged.len(), 1, "one line, not one per poll: {logged:?}");
        assert!(logged[0].contains("not valid providers.json"), "{logged:?}");
    }

    #[test]
    fn a_new_bad_error_is_logged_again_after_a_good_read() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (lines, log) = logged();
        write_providers(&dir, "not json");
        watch.tick(&log);
        write_providers(&dir, "{\"providers\":[\"zai\"]}");
        watch.tick(&log);
        write_providers(&dir, "not json");
        watch.tick(&log);
        assert_eq!(lines.borrow().len(), 2);
    }
}
