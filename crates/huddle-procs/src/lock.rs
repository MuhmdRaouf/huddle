//! huddle-procs: lock. A pid lock file: atomic create, dead-holder reclaim, a bounded wait.

use std::fs;
use std::io::{self, ErrorKind, Write};
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::process;
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use crate::live;

/// How often a take re-reads a lock it found stale before reporting it held.
const RECLAIM_ATTEMPTS: u32 = 5;

/// 40 × 25 ms bounds every EBUSY retry: processes hold these files for microseconds, so a
/// second of trying is beyond any honest collision.
const BUSY_ATTEMPTS: u32 = 40;
const BUSY_PAUSE: Duration = Duration::from_millis(25);

/// The holder a lock error names: 0 when the file holds nothing anybody wrote.
#[derive(Debug)]
pub struct LockHeld {
    pub holder_pid: u32,
}

#[derive(Debug)]
pub enum LockError {
    Held(LockHeld),
    Io { path: PathBuf, source: io::Error },
}

/// Held until dropped or released. Release removes the file only while it names our pid.
#[derive(Debug)]
pub struct LockFile {
    path: PathBuf,
    pid: u32,
    released: bool,
}

impl LockFile {
    /// Frees the lock. A late or second release is a no-op: the file is removed only while it
    /// still names our pid.
    pub fn release(mut self) -> Result<(), LockError> {
        let released = release(&self.path, self.pid);
        if released.is_ok() {
            self.released = true;
        }
        released
    }
}

impl Drop for LockFile {
    fn drop(&mut self) {
        if !self.released {
            let _ = release(&self.path, self.pid);
        }
    }
}

/// Takes an exclusive lock file naming this process; a lock whose holder is dead is reclaimed.
pub fn try_lock(path: &Path) -> Result<LockFile, LockError> {
    try_lock_as(path, process::id(), &live::is_alive)
}

/// Takes the lock, retrying with a short jittered pause until the timeout: for locks that are
/// only ever held briefly. At the deadline the last held error stands.
pub fn wait_lock(path: &Path, timeout: Duration) -> Result<LockFile, LockError> {
    let deadline = Instant::now() + timeout;
    loop {
        match try_lock(path) {
            Ok(lock) => return Ok(lock),
            Err(err) => {
                if Instant::now() >= deadline {
                    return Err(err);
                }
            }
        }
        let nanos = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .map(|since| since.subsec_nanos())
            .unwrap_or_default();
        thread::sleep(Duration::from_millis(u64::from(2 + nanos % 9)));
    }
}

/// The inner form the unit tests drive with a chosen pid and liveness.
fn try_lock_as(
    path: &Path,
    pid: u32,
    is_alive: &dyn Fn(u32) -> bool,
) -> Result<LockFile, LockError> {
    for attempt in 1..=RECLAIM_ATTEMPTS {
        if create_holding(path, pid)? {
            return Ok(LockFile {
                path: path.to_path_buf(),
                pid,
                released: false,
            });
        }
        let holder = read_holder(path)?;
        if !holder.found {
            continue;
        }
        if let Some(holder_pid) = holder.pid
            && is_alive(holder_pid)
        {
            return Err(LockError::Held(LockHeld { holder_pid }));
        }
        reclaim(path, &holder, pid, is_alive)?;
        thread::sleep(Duration::from_millis(u64::from(attempt)));
    }
    let holder = read_holder(path)?;
    Err(LockError::Held(LockHeld {
        holder_pid: holder.pid.unwrap_or(0),
    }))
}

/// Who holds a lock file: no file at all, or a file that names a pid or holds garbage.
struct Holder {
    found: bool,
    pid: Option<u32>,
}

/// Reads who holds the lock file: a missing file is nobody, content that does not parse as a
/// pid is found but held by nobody.
fn read_holder(path: &Path) -> Result<Holder, LockError> {
    let text = match retry_busy(|| fs::read_to_string(path)) {
        Ok(text) => text,
        Err(source) if source.kind() == ErrorKind::NotFound => {
            return Ok(Holder {
                found: false,
                pid: None,
            });
        }
        Err(source) => {
            return Err(LockError::Io {
                path: path.to_path_buf(),
                source,
            });
        }
    };
    Ok(Holder {
        found: true,
        pid: parse_pid(&text),
    })
}

/// Reads a process id from a pid or lock file: digits only, and never 0 or 1 (0 would signal
/// our own group, 1 is init, which no lock of ours is ever held by).
fn parse_pid(text: &str) -> Option<u32> {
    let trimmed = text.trim();
    if trimmed.is_empty() || !trimmed.bytes().all(|byte| byte.is_ascii_digit()) {
        return None;
    }
    trimmed.parse().ok().filter(|pid| *pid > 1)
}

/// Stale: a holder nobody can name, or one that is dead.
fn is_stale(holder: &Holder, is_alive: &dyn Fn(u32) -> bool) -> bool {
    match holder.pid {
        Some(pid) => !is_alive(pid),
        None => true,
    }
}

/// Removes path while it is still held by the stale holder the caller read. Deleting is a
/// check-then-act, so reclaimers serialise on a guard file: without it, a reclaimer that read
/// the stale pid could delete the fresh lock another reclaimer created meanwhile. A guard left
/// by a reclaimer that died is cleared for the next round.
fn reclaim(
    path: &Path,
    stale: &Holder,
    pid: u32,
    is_alive: &dyn Fn(u32) -> bool,
) -> Result<(), LockError> {
    let guard = guard_path(path);
    if !create_holding(&guard, pid)? {
        let guard_holder = read_holder(&guard)?;
        if guard_holder.found && is_stale(&guard_holder, is_alive) {
            return remove_force(&guard);
        }
        return Ok(());
    }
    let deleted = delete_if_held_by(path, stale);
    // The guard goes whether or not the delete worked, and its failure outranks the delete's.
    let cleanup = remove_force(&guard);
    deleted.and(cleanup)
}

/// Removes the lock only while it still names the same holder the reclaimer read: garbage
/// matches garbage, a pid matches itself, and anything else — including no file at all —
/// matches nothing.
fn delete_if_held_by(path: &Path, stale: &Holder) -> Result<(), LockError> {
    let current = read_holder(path)?;
    let same = current.found
        && match (current.pid, stale.pid) {
            (None, None) => true,
            (Some(pid), Some(stale_pid)) => pid == stale_pid,
            _ => false,
        };
    if same {
        remove_force(path)?;
    }
    Ok(())
}

/// Frees a lock: the file is removed only while it names our pid, so a late or repeated
/// release never frees someone else's.
fn release(path: &Path, pid: u32) -> Result<(), LockError> {
    let holder = read_holder(path)?;
    if holder.pid == Some(pid) {
        remove_force(path)?;
    }
    Ok(())
}

/// Creates path naming pid, atomically: the file appears complete or not at all, and only
/// when it did not exist. False means another holder won.
fn create_holding(path: &Path, pid: u32) -> Result<bool, LockError> {
    create_exclusive(path, &format!("{pid}\n"), pid)
}

/// Creates path holding text, atomically: a temp file that must not exist, then link(2), so a
/// reader never sees the file half-written. EEXIST from the link means another holder won.
fn create_exclusive(path: &Path, text: &str, pid: u32) -> Result<bool, LockError> {
    let tmp = tmp_path(path, pid);
    let file = match retry_busy(|| {
        fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&tmp)
    }) {
        Ok(file) => file,
        Err(source) => return Err(LockError::Io { path: tmp, source }),
    };
    let written = (&file).write_all(text.as_bytes());
    drop(file);
    let linked = retry_busy(|| fs::hard_link(&tmp, path));
    let removed = remove_force(&tmp);
    match (written, linked, removed) {
        (Err(source), _, _) => Err(LockError::Io { path: tmp, source }),
        (_, Err(source), _) if source.raw_os_error() == Some(libc::EEXIST) => Ok(false),
        (_, Err(source), _) => Err(LockError::Io {
            path: path.to_path_buf(),
            source,
        }),
        (_, Ok(()), Err(err)) => Err(err),
        (_, Ok(()), Ok(())) => Ok(true),
    }
}

/// The temp file's name: the lock path, then the time's nanoseconds and our pid in hex, so
/// concurrent creates of one pid never share a name and no rand is needed.
fn tmp_path(path: &Path, pid: u32) -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .map(|since| since.as_nanos())
        .unwrap_or_default();
    PathBuf::from(format!("{}.{:x}{:x}.tmp", path.display(), nanos, pid))
}

/// The reclaimer's guard file.
fn guard_path(path: &Path) -> PathBuf {
    PathBuf::from(format!("{}.reclaim", path.display()))
}

/// Removes a file; a missing file is fine, any other failure is an error. A busy answer is
/// retried past the collision, so a lock or slot file is retired rather than left naming a
/// holder nobody can reclaim.
fn remove_force(path: &Path) -> Result<(), LockError> {
    match retry_busy(|| fs::remove_file(path)) {
        Ok(()) => Ok(()),
        Err(source) if source.kind() == ErrorKind::NotFound => Ok(()),
        Err(source) => Err(LockError::Io {
            path: path.to_path_buf(),
            source,
        }),
    }
}

/// Runs an operation, retrying an EBUSY answer a bounded number of times.
fn retry_busy<T>(op: impl Fn() -> io::Result<T>) -> io::Result<T> {
    let mut attempt = 0;
    loop {
        match op() {
            Ok(value) => return Ok(value),
            Err(err) => {
                attempt += 1;
                if err.raw_os_error() != Some(libc::EBUSY) || attempt > BUSY_ATTEMPTS {
                    return Err(err);
                }
                thread::sleep(BUSY_PAUSE);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::fs;

    fn path_in(home: &TempHome) -> PathBuf {
        home.root().join("x.lock")
    }

    #[test]
    fn a_first_lock_holds_and_names_its_pid() {
        let home = TempHome::new();
        let path = path_in(&home);
        let lock = try_lock_as(&path, 101, &|_| true).expect("lock");
        assert_eq!(fs::read_to_string(&path).expect("read"), "101\n");
        lock.release().expect("release");
        assert!(!path.exists(), "a release removes our own lock file");
    }

    #[test]
    fn a_held_lock_is_refused_naming_the_holder() {
        let home = TempHome::new();
        let path = path_in(&home);
        let _first = try_lock_as(&path, 101, &|_| true).expect("first");
        match try_lock_as(&path, 202, &|pid| pid == 101) {
            Err(LockError::Held(held)) => assert_eq!(held.holder_pid, 101),
            other => panic!("want Held, got {other:?}"),
        }
    }

    #[test]
    fn garbage_in_a_lock_file_counts_as_nobody_holding_it() {
        let home = TempHome::new();
        let path = path_in(&home);
        fs::write(&path, "not a pid").expect("seed");
        let lock = try_lock_as(&path, 101, &|_| true).expect("reclaimed");
        assert_eq!(fs::read_to_string(&path).expect("read"), "101\n");
        lock.release().expect("release");
    }

    #[test]
    fn a_dead_holders_lock_is_reclaimed() {
        let home = TempHome::new();
        let path = path_in(&home);
        fs::write(&path, "202\n").expect("seed");
        let lock = try_lock_as(&path, 101, &|_| false).expect("reclaimed");
        assert_eq!(fs::read_to_string(&path).expect("read"), "101\n");
        lock.release().expect("release");
    }

    #[test]
    fn a_release_never_frees_someone_elses_lock() {
        let home = TempHome::new();
        let path = path_in(&home);
        let first = try_lock_as(&path, 101, &|_| true).expect("first");
        fs::write(&path, "202\n").expect("someone took it over");
        first.release().expect("release");
        assert_eq!(
            fs::read_to_string(&path).expect("read"),
            "202\n",
            "the other holder's lock stays"
        );
    }

    #[test]
    fn a_wait_joins_the_queue_and_names_the_holder_past_its_deadline() {
        let home = TempHome::new();
        let path = path_in(&home);
        let _first = try_lock(&path).expect("first");
        let waited = wait_lock(&path, Duration::from_millis(60));
        assert!(
            matches!(waited, Err(LockError::Held(LockHeld { holder_pid })) if holder_pid == std::process::id())
        );
    }

    #[test]
    fn no_temp_file_is_left_behind() {
        let home = TempHome::new();
        let path = path_in(&home);
        let lock = try_lock_as(&path, 101, &|_| true).expect("lock");
        let leftovers: Vec<_> = fs::read_dir(home.root())
            .expect("list")
            .filter_map(|entry| entry.ok())
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .filter(|name| name.ends_with(".tmp"))
            .collect();
        assert!(leftovers.is_empty(), "{leftovers:?}");
        lock.release().expect("release");
    }
}
