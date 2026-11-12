//! One holder per lock file: an exclusive, non-blocking `flock` held for the life of the
//! [`LockGuard`], so exactly one router serves a home. The kernel drops the lock when its holder
//! dies, however it dies, so a stale file never blocks a start. The file is never deleted:
//! deleting it would let a racing starter lock a fresh inode while the old lock is still held.

use huddle_config::files::{DIR_MODE, create_dir};
use std::fmt;
use std::fs::{File, OpenOptions, TryLockError};
use std::io::{self, Read, Write};
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::thread;
use std::time::{Duration, Instant};

/// How often `wait` retries the lock.
const RETRY: Duration = Duration::from_millis(50);

/// A second starter's answer: another process holds the lock.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Held {
    pub pid: u32,
    pub holder: String,
}

impl fmt::Display for Held {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.holder.is_empty() {
            write!(f, "held by pid {}", self.pid)
        } else {
            write!(
                f,
                "{} (pid {}) already serves this home",
                self.holder, self.pid
            )
        }
    }
}

#[derive(Debug)]
pub enum SingletonError {
    Held(Held),
    Io { path: PathBuf, source: io::Error },
}

/// Holding this keeps the flock; dropping it releases. The file stays on disk.
#[derive(Debug)]
pub struct LockGuard {
    /// Held for the flock; the kernel releases it when the guard drops.
    #[allow(dead_code)]
    file: File,
}

/// Takes the lock at `path`, refusing while another process holds it. Creates the file and its
/// directory when missing; a file a dead process left behind is free. On refusal, reads the pid
/// the holder wrote and answers [`SingletonError::Held`].
pub fn lock(path: &Path, holder: &str) -> Result<LockGuard, SingletonError> {
    let parent = path.parent().unwrap_or_else(|| Path::new(""));
    create_dir(parent, DIR_MODE).map_err(|source| SingletonError::Io {
        path: parent.to_path_buf(),
        source,
    })?;
    let mut file = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .mode(0o600)
        .open(path)
        .map_err(|source| SingletonError::Io {
            path: path.to_path_buf(),
            source,
        })?;
    match file.try_lock() {
        Ok(()) => {}
        Err(TryLockError::WouldBlock) => {
            return Err(SingletonError::Held(Held {
                pid: pid_line(&mut file),
                holder: holder.to_string(),
            }));
        }
        Err(TryLockError::Error(source)) => {
            return Err(SingletonError::Io {
                path: path.to_path_buf(),
                source,
            });
        }
    }
    file.set_len(0).map_err(|source| SingletonError::Io {
        path: path.to_path_buf(),
        source,
    })?;
    writeln!(file, "{}", std::process::id()).map_err(|source| SingletonError::Io {
        path: path.to_path_buf(),
        source,
    })?;
    file.sync_all().map_err(|source| SingletonError::Io {
        path: path.to_path_buf(),
        source,
    })?;
    Ok(LockGuard { file })
}

/// Takes the lock at `path`, waiting up to `within` for the holder to let go: for the short
/// critical sections that must join the queue rather than fail. Polls every 50 ms; past the
/// deadline the last [`Held`] stands. Blocking: never call it on an async task (use
/// `spawn_blocking`).
pub fn wait(path: &Path, holder: &str, within: Duration) -> Result<LockGuard, SingletonError> {
    let deadline = Instant::now() + within;
    loop {
        match lock(path, holder) {
            Ok(guard) => return Ok(guard),
            Err(error @ SingletonError::Held(_)) => {
                if Instant::now() >= deadline {
                    return Err(error);
                }
                thread::sleep(RETRY);
            }
            Err(error) => return Err(error),
        }
    }
}

/// How long a router start waits for the lock: longer than a probe's momentary hold (see
/// [`holder`]), far shorter than a live router's life.
pub const START_WAIT: Duration = Duration::from_millis(200);

/// Takes the lock for a router start. `flock` has no read-only look, so [`holder`] takes the
/// lock for an instant; waiting out [`START_WAIT`] keeps that instant from refusing a start,
/// while a live holder still refuses it.
pub fn take_for_start(path: &Path, holder: &str) -> Result<LockGuard, SingletonError> {
    wait(path, holder, START_WAIT)
}

/// Answers the pid holding the lock at `path`: it tries the same non-blocking flock on a fresh
/// handle. No file, a free lock, or an unreadable file is `None`; held names the pid on the
/// file's first line, 0 when it does not parse.
pub fn holder(path: &Path) -> Option<u32> {
    let mut file = OpenOptions::new().read(true).write(true).open(path).ok()?;
    match file.try_lock() {
        Ok(()) => {
            let _ = file.unlock();
            None
        }
        Err(TryLockError::WouldBlock) => Some(pid_line(&mut file)),
        Err(TryLockError::Error(_)) => None,
    }
}

/// Reads the pid a lock file names: its first line, trimmed; 0 when it names none.
fn pid_line(file: &mut File) -> u32 {
    let mut raw = Vec::new();
    if file.read_to_end(&mut raw).is_err() {
        return 0;
    }
    let line = raw.split(|byte| *byte == b'\n').next().unwrap_or(&[]);
    String::from_utf8_lossy(line).trim().parse().unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::{
        fs,
        sync::{Arc, Barrier},
        thread,
        time::{Duration, Instant},
    };

    #[test]
    fn a_first_lock_holds_a_second_is_refused_naming_the_holder() {
        let home = TempHome::new();
        let path = home.root().join("run/router.lock");
        let first = lock(&path, "huddle-router").expect("first");
        match lock(&path, "huddle-router") {
            Err(SingletonError::Held(held)) => {
                assert_eq!(held.pid, std::process::id());
                assert_eq!(
                    held.to_string(),
                    format!("huddle-router (pid {}) already serves this home", held.pid)
                );
            }
            other => panic!("want Held, got {other:?}"),
        }
        assert_eq!(holder(&path), Some(std::process::id()));
        drop(first);
        // A fork of this process can inherit the lock's fd for a moment before it execs, so
        // the release can land a beat after the drop.
        let deadline = Instant::now() + Duration::from_secs(2);
        while holder(&path).is_some() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(10));
        }
        assert_eq!(holder(&path), None, "free after release");
        assert!(path.exists(), "the file is never deleted");
    }

    #[test]
    fn lock_creates_the_lock_files_directory_with_the_folder_mode() {
        let home = TempHome::new();
        let path = home.root().join("a/b/router.lock");
        let _held = lock(&path, "").expect("lock");
        use std::os::unix::fs::PermissionsExt;
        let mode = fs::metadata(path.parent().expect("parent"))
            .expect("stat")
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(mode, 0o755);
    }

    #[test]
    fn a_stale_lock_file_never_blocks_a_start() {
        let home = TempHome::new();
        let path = home.root().join("run/router.lock");
        fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
        fs::write(&path, "99999\n").expect("seed");
        let _held = lock(&path, "huddle-router").expect("a stale file is free");
    }

    #[test]
    fn wait_joins_the_queue_and_names_the_holder_past_its_deadline() {
        let home = TempHome::new();
        let path = home.root().join("router.lock");
        let first = lock(&path, "huddle-router").expect("first");
        match wait(&path, "huddle-router", Duration::from_millis(60)) {
            Err(SingletonError::Held(held)) => assert_eq!(held.pid, std::process::id()),
            other => panic!("want Held, got {other:?}"),
        }
        let path_two = path.clone();
        let waiter =
            thread::spawn(move || wait(&path_two, "huddle-router", Duration::from_secs(2)));
        thread::sleep(Duration::from_millis(30));
        drop(first);
        waiter
            .join()
            .expect("join")
            .expect("the waiter gets the lock when the holder lets go");
    }

    #[test]
    fn holder_answers_none_without_a_file_or_when_free() {
        let home = TempHome::new();
        let path = home.root().join("x.lock");
        assert_eq!(holder(&path), None);
        fs::write(&path, "123\n").expect("seed");
        assert_eq!(holder(&path), None);
    }

    #[test]
    fn held_error_words_with_and_without_a_holder_name() {
        assert_eq!(
            Held {
                pid: 7,
                holder: String::new()
            }
            .to_string(),
            "held by pid 7"
        );
        assert_eq!(
            Held {
                pid: 7,
                holder: "a Huddle server".into()
            }
            .to_string(),
            "a Huddle server (pid 7) already serves this home"
        );
    }

    #[test]
    fn lock_reports_its_own_failures() {
        let home = TempHome::new();
        let blocker = home.root().join("not-a-dir");
        fs::write(&blocker, "x").expect("seed");
        let path = blocker.join("router.lock");
        assert!(matches!(lock(&path, ""), Err(SingletonError::Io { .. })));
    }

    #[test]
    fn five_starters_racing_one_lock_leave_exactly_one_holder() {
        let home = TempHome::new();
        let path = Arc::new(home.root().join("run/router.lock"));
        let barrier = Arc::new(Barrier::new(5));
        let starters: Vec<_> = (0..5)
            .map(|_| {
                let path = Arc::clone(&path);
                let barrier = Arc::clone(&barrier);
                thread::spawn(move || {
                    barrier.wait();
                    lock(&path, "huddle-router").ok()
                })
            })
            .collect();
        let holders: Vec<_> = starters
            .into_iter()
            .filter_map(|t| t.join().expect("join"))
            .collect();
        assert_eq!(holders.len(), 1, "exactly one starter holds the lock");
    }

    #[test]
    fn b6_a_probe_never_refuses_a_starting_router() {
        let home = TempHome::new();
        let path = Arc::new(home.root().join("run/router.lock"));
        lock(&path, "seed").expect("create the file");
        let stop = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let probe = {
            let (path, stop) = (Arc::clone(&path), Arc::clone(&stop));
            thread::spawn(move || {
                while !stop.load(std::sync::atomic::Ordering::Relaxed) {
                    let _ = holder(&path);
                }
            })
        };
        for _ in 0..200 {
            let guard = take_for_start(&path, "huddle-router")
                .expect("a probe's momentary hold never refuses a start");
            drop(guard);
        }
        stop.store(true, std::sync::atomic::Ordering::Relaxed);
        probe.join().expect("join");
    }

    #[test]
    fn b6_a_live_holder_still_refuses_a_start_after_its_wait() {
        let home = TempHome::new();
        let path = home.root().join("run/router.lock");
        let _held = lock(&path, "huddle-router").expect("first");
        let began = Instant::now();
        assert!(matches!(
            take_for_start(&path, "huddle-router"),
            Err(SingletonError::Held(_))
        ));
        assert!(began.elapsed() >= START_WAIT);
    }
}
