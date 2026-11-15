//! huddle-router: reload. The daemon's providers.toml poll: stat the file each tick, read it
//! when its fingerprint changed, say when the lanes should change, and log a bad file once per
//! distinct error.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

use huddle_config::dashboard_hosts;
use huddle_config::providers_file::{providers_path, read_enabled};

/// How often the daemon polls providers.toml.
pub const POLL: Duration = Duration::from_secs(2);

/// What one poll tells the daemon to do with its lanes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ReloadAction {
    Keep,
    /// The enabled list changed and is non-empty: swap to these providers.
    Swap(Vec<String>),
}

/// Watches the router dir's providers.toml. One poll per tick: the poll stats the file first
/// and reads it only when its fingerprint (mtime, length) changed, so an untouched file costs
/// no read. What the read brings becomes a lane change, and every read is taken so a broken
/// file is noticed and logged.
pub struct ProvidersWatch {
    config_dir: PathBuf,
    known: Vec<&'static str>,
    /// The fingerprint the last tick saw. `new` takes it before the first read, so a provider
    /// enabled while the daemon starts changes the file after this snapshot and the first tick
    /// swaps instead of missing it.
    last: Option<Fingerprint>,
    last_error: Option<String>,
}

impl ProvidersWatch {
    /// Takes the baseline fingerprint before the first read: a provider enabled while the daemon
    /// starts changes the file after this snapshot, so the first tick swaps instead of missing it.
    pub fn new(config_dir: PathBuf, known: Vec<&'static str>) -> ProvidersWatch {
        let baseline = fingerprint(&providers_path(&config_dir));
        ProvidersWatch {
            config_dir,
            known,
            last: Some(baseline),
            last_error: None,
        }
    }

    /// One poll. `log` is called at most once per distinct error: the same bad file logs one
    /// line, and a read that succeeds resets the log so a later break is reported again.
    pub fn tick(&mut self, log: &dyn Fn(String)) -> ReloadAction {
        let now = fingerprint(&providers_path(&self.config_dir));
        if self.last.as_ref() == Some(&now) {
            return ReloadAction::Keep;
        }
        self.last = Some(now);
        match read_enabled(&self.config_dir, &self.known) {
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
                if names.is_empty() {
                    // Nothing is enabled: keep serving. Sessions still point at the router, and
                    // an empty answer would cut them off.
                    ReloadAction::Keep
                } else {
                    ReloadAction::Swap(names)
                }
            }
        }
    }
}

/// Watches config.toml's dashboard hosts. The poll stats the file first and reads it only when
/// its fingerprint (mtime, length) changed, so an untouched file costs no read while an added
/// host still applies within one poll.
pub struct DashboardHostsWatch {
    path: PathBuf,
    /// The fingerprint the last tick saw; `None` until the first read, so the first tick always
    /// reads and startup gets the file's hosts.
    last: Option<Fingerprint>,
    /// The hosts of the last read. A tick whose fingerprint matches the last one answers these
    /// without reading again.
    hosts: Vec<String>,
    last_error: Option<String>,
}

impl DashboardHostsWatch {
    pub fn new(path: PathBuf) -> DashboardHostsWatch {
        DashboardHostsWatch {
            path,
            last: None,
            hosts: Vec::new(),
            last_error: None,
        }
    }

    /// One poll: the hosts the file lists now, or while its fingerprint holds, the hosts of the
    /// last read. A file that cannot be read or parsed lists none, so the router accepts the
    /// loopback only, and `log` gets the error once per distinct error; a good read resets
    /// that, so a later break is reported again.
    pub fn tick(&mut self, log: &dyn Fn(String)) -> Vec<String> {
        let now = fingerprint(&self.path);
        if self.last.as_ref() == Some(&now) {
            return self.hosts.clone();
        }
        self.last = Some(now);
        match dashboard_hosts::read(&self.path) {
            Ok(hosts) => {
                self.last_error = None;
                self.hosts = hosts.clone();
                hosts
            }
            Err(err) => {
                self.hosts = Vec::new();
                let line = format!("huddle-router: {err}");
                if self.last_error.as_deref() != Some(line.as_str()) {
                    log(line.clone());
                    self.last_error = Some(line);
                }
                Vec::new()
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
        fs::write(dir.join("providers.toml"), text).expect("write");
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
        write_providers(&dir, "providers = [\"zai\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
    }

    #[test]
    fn a_file_that_did_not_change_keeps_the_lanes() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        write_providers(&dir, "providers = [\"zai\"]\n");
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
        write_providers(&dir, "providers = [\"zai\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "providers = [\"zai\", \"kimi\"]\n");
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
        write_providers(&dir, "providers = [\"zai\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "providers = []\n");
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
    }

    #[test]
    fn a_malformed_file_is_logged_once_per_distinct_error_and_keeps_the_lanes() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (lines, log) = logged();
        write_providers(&dir, "providers = [\"zai\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "providers = [");
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
        let logged = lines.borrow().clone();
        assert_eq!(logged.len(), 1, "one line, not one per poll: {logged:?}");
        assert!(logged[0].contains("not valid providers.toml"), "{logged:?}");
    }

    #[test]
    fn a_new_bad_error_is_logged_again_after_a_good_read() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (lines, log) = logged();
        write_providers(&dir, "providers = [");
        watch.tick(&log);
        write_providers(&dir, "providers = [\"zai\"]\n");
        watch.tick(&log);
        write_providers(&dir, "providers = [");
        watch.tick(&log);
        assert_eq!(lines.borrow().len(), 2);
    }

    fn write_config(path: &PathBuf, text: &str) {
        fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
        fs::write(path, text).expect("write");
    }

    /// Takes the read bit away without touching mtime or length: only a tick that re-reads the
    /// file would notice, so an error or an empty answer after this proves the read ran.
    fn make_unreadable(path: &PathBuf) {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o000)).expect("chmod");
    }

    fn make_readable_again(path: &PathBuf) {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o644)).expect("chmod back");
    }

    #[test]
    fn l14_an_unchanged_file_is_not_read_again() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (lines, log) = logged();
        write_providers(&dir, "providers = [\"zai\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        let path = providers_path(&dir);
        make_unreadable(&path);
        assert_eq!(watch.tick(&log), ReloadAction::Keep);
        assert!(
            lines.borrow().is_empty(),
            "the unchanged file was read again: {lines:?}"
        );
        make_readable_again(&path);
    }

    #[test]
    fn l14_a_changed_file_is_picked_up_on_the_next_tick() {
        let home = TempHome::new();
        let dir = home.root().join("router");
        let mut watch = ProvidersWatch::new(dir.clone(), KNOWN.to_vec());
        let (_lines, log) = logged();
        write_providers(&dir, "providers = [\"zai\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string()])
        );
        write_providers(&dir, "providers = [\"zai\", \"kimi\"]\n");
        assert_eq!(
            watch.tick(&log),
            ReloadAction::Swap(vec!["zai".to_string(), "kimi".to_string()])
        );
    }

    #[test]
    fn l14_an_unchanged_hosts_file_is_not_read_again() {
        let home = TempHome::new();
        let path = home.root().join("cfg").join("config.toml");
        write_config(&path, "[dashboard]\nhosts = [\"hub.raouf.me\"]\n");
        let mut watch = DashboardHostsWatch::new(path.clone());
        let (lines, log) = logged();
        assert_eq!(watch.tick(&log), vec!["hub.raouf.me".to_string()]);
        make_unreadable(&path);
        assert_eq!(
            watch.tick(&log),
            vec!["hub.raouf.me".to_string()],
            "the skip keeps the hosts of the last read"
        );
        assert!(
            lines.borrow().is_empty(),
            "the unchanged file was read again: {lines:?}"
        );
        make_readable_again(&path);
    }

    #[test]
    fn the_hosts_watch_follows_the_file() {
        let home = TempHome::new();
        let path = home.root().join("cfg").join("config.toml");
        let mut watch = DashboardHostsWatch::new(path.clone());
        let (_lines, log) = logged();
        assert_eq!(watch.tick(&log), Vec::<String>::new(), "no file yet");
        write_config(&path, "[dashboard]\nhosts = [\"hub.raouf.me\"]\n");
        assert_eq!(watch.tick(&log), vec!["hub.raouf.me".to_string()]);
        write_config(&path, "[dashboard]\nhosts = []\n");
        assert_eq!(watch.tick(&log), Vec::<String>::new());
    }

    #[test]
    fn a_broken_hosts_file_lists_none_and_logs_once_per_distinct_error() {
        let home = TempHome::new();
        let path = home.root().join("cfg").join("config.toml");
        let mut watch = DashboardHostsWatch::new(path.clone());
        let (lines, log) = logged();
        write_config(&path, "[dashboard]\nhosts = [\"hub.raouf.me\"]\n");
        assert_eq!(watch.tick(&log), vec!["hub.raouf.me".to_string()]);
        write_config(&path, "not = [toml");
        assert_eq!(watch.tick(&log), Vec::<String>::new());
        assert_eq!(watch.tick(&log), Vec::<String>::new());
        let logged = lines.borrow().clone();
        assert_eq!(logged.len(), 1, "one line, not one per poll: {logged:?}");
        assert!(logged[0].starts_with("huddle-router: "), "{logged:?}");
        assert!(logged[0].contains("config.toml"), "{logged:?}");
        write_config(&path, "[dashboard]\nhosts = []\n");
        watch.tick(&log);
        write_config(&path, "not = [toml");
        watch.tick(&log);
        assert_eq!(
            lines.borrow().len(),
            2,
            "a new break after a good read logs again"
        );
    }
}
