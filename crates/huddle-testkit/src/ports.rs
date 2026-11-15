//! Loopback ports for tests.

use std::collections::hash_map::RandomState;
use std::ffi::OsString;
use std::fs::{self, OpenOptions};
use std::hash::{BuildHasher, Hasher};
use std::io;
use std::net::TcpListener;
use std::path::{Path, PathBuf};
use std::thread;
use std::time::{Duration, Instant, SystemTime};

const FIRST_PORT: u16 = 20000;
const LAST_PORT: u16 = 29999;
const STALE_AFTER: Duration = Duration::from_secs(3600);
// A leased port can stay busy for a moment after the socket that last held it closed, longer on a
// loaded machine: the lease holder waits this long for it before it gives the port up.
const BIND_WAIT: Duration = Duration::from_secs(2);
const BIND_RETRY: Duration = Duration::from_millis(20);
/// Directory of the port leases, under the temp directory. The guard does not count it as a leak.
pub const LEASE_DIR: &str = "huddle-port-leases";
/// The variable that names the lease folder. The suite guard sets it to a folder outside its
/// private `TMPDIR`, so two guarded runs at once lease from the same place.
pub const LEASE_DIR_VAR: &str = "HUDDLE_PORT_LEASES";

/// A listener on a port the kernel picks: the port is held from the moment the test learns it.
pub fn listen() -> TcpListener {
    match TcpListener::bind("127.0.0.1:0") {
        Ok(listener) => listener,
        Err(err) => panic!("listen on 127.0.0.1:0: {err}"),
    }
}

/// A port for a child process to bind, outside the kernel's ephemeral ranges (macOS 49152-65535,
/// Linux 32768-60999), so no `127.0.0.1:0` listener of a parallel test can take it. The answer is a
/// lease file in the lease folder that every test binary and every guarded run respects; it expires
/// after an hour.
pub fn free_port() -> u16 {
    let dir = lease_dir(std::env::var_os(LEASE_DIR_VAR), &std::env::temp_dir());
    match free_in(&dir, FIRST_PORT, LAST_PORT) {
        Ok(port) => port,
        Err(err) => panic!("free port: {err}"),
    }
}

/// The lease folder: the variable's value when it is an absolute path, else `LEASE_DIR` under `temp`.
pub fn lease_dir(var: Option<OsString>, temp: &Path) -> PathBuf {
    match var.map(PathBuf::from) {
        Some(dir) if dir.is_absolute() => dir,
        _ => temp.join(LEASE_DIR),
    }
}

/// The search over a lease directory and a port range of the caller's.
pub fn free_in(dir: &Path, first: u16, last: u16) -> io::Result<u16> {
    let span = u32::from(last - first) + 1;
    let start = (RandomState::new().build_hasher().finish() % u64::from(span)) as u32;
    for offset in 0..span {
        let port = first + ((start + offset) % span) as u16;
        if take_lease(dir, port)? {
            if binds_within(port, BIND_WAIT) {
                return Ok(port);
            }
            let _ = fs::remove_file(dir.join(port.to_string()));
        }
    }
    Err(io::Error::other(format!("no free port in {first}-{last}")))
}

// Answers whether the port could be bound before `within` ran out. Only a port in use is retried.
fn binds_within(port: u16, within: Duration) -> bool {
    let deadline = Instant::now() + within;
    loop {
        match TcpListener::bind(("127.0.0.1", port)) {
            Ok(_) => return true,
            Err(err) if err.kind() == io::ErrorKind::AddrInUse && Instant::now() < deadline => {
                thread::sleep(BIND_RETRY);
            }
            Err(_) => return false,
        }
    }
}

// Answers whether the caller holds `port` now.
fn take_lease(dir: &Path, port: u16) -> io::Result<bool> {
    let file = dir.join(port.to_string());
    if fs::create_dir_all(dir).is_err() {
        return Ok(true);
    }
    match create(&file) {
        Ok(()) => Ok(true),
        Err(err) if err.kind() == io::ErrorKind::AlreadyExists => {
            if !is_stale(&file) {
                return Ok(false);
            }
            take_stale(dir, &file, port)
        }
        Err(err) if vanished(&err) => {
            let _ = fs::create_dir_all(dir);
            match create(&file) {
                Ok(()) => Ok(true),
                Err(err) if err.kind() == io::ErrorKind::AlreadyExists => Ok(false),
                Err(_) => Ok(true),
            }
        }
        Err(err) => Err(err),
    }
}

// Takes over a stale lease under a per-port takeover lock. Without it, a caller whose staleness
// check is out of date removes the lease another caller has just re-created, and both hold the port.
fn take_stale(dir: &Path, file: &Path, port: u16) -> io::Result<bool> {
    let lock = dir.join(format!("{port}.takeover"));
    if create(&lock).is_err() {
        return Ok(false);
    }
    let taken = if is_stale(file) {
        let _ = fs::remove_file(file);
        match create(file) {
            Ok(()) => Ok(true),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => Ok(false),
            Err(err) => Err(err),
        }
    } else {
        Ok(false)
    };
    let _ = fs::remove_file(&lock);
    taken
}

fn create(file: &Path) -> io::Result<()> {
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(file)
        .map(|_| ())
}

fn is_stale(file: &Path) -> bool {
    fs::metadata(file)
        .and_then(|meta| meta.modified())
        .ok()
        .and_then(|modified| SystemTime::now().duration_since(modified).ok())
        .is_some_and(|age| age > STALE_AFTER)
}

// Another binary removed the lease directory between the make and the create: ENOENT on Linux and
// macOS, EINVAL on APFS.
fn vanished(err: &io::Error) -> bool {
    err.kind() == io::ErrorKind::NotFound || err.kind() == io::ErrorKind::InvalidInput
}
