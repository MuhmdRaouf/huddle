//! The key store port: what every store answers, the key's names and shape, the tool runner and
//! the platform selection.

use std::ffi::{CStr, OsStr, OsString};
use std::fmt;
use std::io::{Read, Write};
use std::os::unix::ffi::OsStrExt;
use std::path::{Component, Path, PathBuf};
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

use huddle_config::paths::Paths;

use crate::file::FileStore;

/// How long a store tool may run before it is killed.
const TOOL_TIMEOUT: Duration = Duration::from_secs(60);

/// How often a running tool is polled for its exit.
const TOOL_POLL: Duration = Duration::from_millis(20);

/// The account name every provider's key uses in an OS store.
pub const ACCOUNT: &str = "api-key";

/// A key held in memory. Never printed: Debug and Display show `***`.
#[derive(Clone)]
pub struct SecretString(String);

impl SecretString {
    pub fn new(value: String) -> SecretString {
        SecretString(value)
    }

    pub fn expose(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for SecretString {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("***")
    }
}

impl fmt::Display for SecretString {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("***")
    }
}

/// What reading, writing or removing a key can refuse.
#[derive(Debug)]
pub enum KeystoreError {
    /// The key does not fit the accepted shape; carries no part of the key.
    BadShape,
    InsecureFile {
        path: PathBuf,
        mode: u32,
    },
    InsecureDir {
        path: PathBuf,
        mode: u32,
    },
    Read {
        path: PathBuf,
        source: std::io::Error,
    },
    Write {
        path: PathBuf,
        source: std::io::Error,
    },
    /// A store tool failed: `could not <action> the key in <label> (<why>)`, never the key.
    Tool {
        label: &'static str,
        action: &'static str,
        why: String,
    },
}

impl fmt::Display for KeystoreError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            KeystoreError::BadShape => {
                f.write_str("the key must be 16-512 characters of letters, digits, '.', '_' or '-'")
            }
            KeystoreError::InsecureFile { path, mode } => write!(
                f,
                "key file {} has mode {:04o}: run chmod 600 on it",
                path.display(),
                mode
            ),
            KeystoreError::InsecureDir { path, mode } => write!(
                f,
                "key file directory {} is writable by others (mode {:04o})",
                path.display(),
                mode
            ),
            KeystoreError::Read { path, source } => {
                write!(f, "cannot read key file {} ({})", path.display(), source)
            }
            KeystoreError::Write { path, source } => {
                write!(f, "cannot write key file {} ({})", path.display(), source)
            }
            KeystoreError::Tool { label, action, why } => {
                write!(f, "could not {action} the key in {label} ({why})")
            }
        }
    }
}

impl std::error::Error for KeystoreError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            KeystoreError::Read { source, .. } | KeystoreError::Write { source, .. } => {
                Some(source)
            }
            _ => None,
        }
    }
}

/// One provider's key: read it, write it, remove it.
pub trait Keystore: Send + Sync {
    fn get(&self, provider: &str) -> Result<Option<SecretString>, KeystoreError>;
    fn set(&self, provider: &str, key: &SecretString) -> Result<(), KeystoreError>;
    fn delete(&self, provider: &str) -> Result<(), KeystoreError>;
}

/// An OS store: a Keystore that can also say whether it answers at all.
pub trait OsStore: Keystore {
    fn available(&self) -> bool;
}

/// True when the key fits the accepted shape: 16-512 characters of `[A-Za-z0-9._-]`.
pub fn is_valid_key(key: &str) -> bool {
    (16..=512).contains(&key.chars().count())
        && key
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '.' || c == '_' || c == '-')
}

/// Refuses a key of the wrong shape before anything is written.
pub fn assert_valid_key(key: &str) -> Result<(), KeystoreError> {
    if is_valid_key(key) {
        Ok(())
    } else {
        Err(KeystoreError::BadShape)
    }
}

/// An OS store's name for the provider's key.
pub fn service_name(provider: &str) -> String {
    format!("{provider}-plugin-cc")
}

/// What a platform tool returned. A `None` code is a signal; `missing` means the tool itself
/// could not be started.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RunResult {
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
    pub missing: bool,
}

impl RunResult {
    /// True when the tool ran to the given exit code; a signal or a missing tool did not.
    pub fn exited(&self, code: i32) -> bool {
        self.code == Some(code)
    }
}

/// Runs one store tool. Tests pass a fake.
pub trait ToolRunner: Send + Sync {
    fn run(&self, program: &str, args: &[&str], stdin: Option<&str>) -> RunResult;
}

/// Spawns without a shell, writes stdin and closes it, kills the tool past 60 s.
pub struct SystemRunner;

impl ToolRunner for SystemRunner {
    fn run(&self, program: &str, args: &[&str], stdin: Option<&str>) -> RunResult {
        let missing = RunResult {
            code: None,
            stdout: String::new(),
            stderr: String::new(),
            missing: true,
        };
        let mut child = match Command::new(program)
            .args(args)
            .stdin(if stdin.is_some() {
                Stdio::piped()
            } else {
                Stdio::null()
            })
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
        {
            Ok(child) => child,
            Err(_) => return missing,
        };
        // Both pipes are drained while the tool runs, so a chatty tool cannot block us.
        let stdout = drain(child.stdout.take());
        let stderr = drain(child.stderr.take());
        if let Some(text) = stdin
            && let Some(mut handle) = child.stdin.take()
        {
            // A tool that exits before reading the whole input is its own failure; the
            // broken pipe never carries the key anywhere.
            let _ = handle.write_all(text.as_bytes());
        }
        let code = wait_within(&mut child, TOOL_TIMEOUT);
        RunResult {
            code,
            stdout: stdout.join().unwrap_or_default(),
            stderr: stderr.join().unwrap_or_default(),
            missing: false,
        }
    }
}

/// Reads one pipe to its end on its own thread.
fn drain<R: Read + Send + 'static>(pipe: Option<R>) -> thread::JoinHandle<String> {
    thread::spawn(move || {
        let mut text = String::new();
        if let Some(mut pipe) = pipe {
            let mut bytes = Vec::new();
            if pipe.read_to_end(&mut bytes).is_ok() {
                text = String::from_utf8_lossy(&bytes).into_owned();
            }
        }
        text
    })
}

/// Waits for the child within the budget; a tool that hangs is killed and answers a signal.
fn wait_within(child: &mut std::process::Child, budget: Duration) -> Option<i32> {
    let deadline = Instant::now() + budget;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return status.code(),
            Ok(None) if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                return None;
            }
            Ok(None) => thread::sleep(TOOL_POLL),
            Err(_) => return None,
        }
    }
}

struct Layered {
    os: Option<Box<dyn OsStore>>,
    file: FileStore,
}

impl Keystore for Layered {
    fn get(&self, provider: &str) -> Result<Option<SecretString>, KeystoreError> {
        if let Some(os) = &self.os
            && let Ok(Some(key)) = os.get(provider)
        {
            return Ok(Some(key));
        }
        self.file.get(provider)
    }

    fn set(&self, provider: &str, key: &SecretString) -> Result<(), KeystoreError> {
        if let Some(os) = &self.os
            && os.available()
        {
            return os.set(provider, key);
        }
        self.file.set(provider, key)
    }

    fn delete(&self, provider: &str) -> Result<(), KeystoreError> {
        let mut first = None;
        if let Some(os) = &self.os
            && let Err(error) = os.delete(provider)
        {
            first = Some(error);
        }
        if let Err(error) = self.file.delete(provider)
            && first.is_none()
        {
            first = Some(error);
        }
        match first {
            Some(error) => Err(error),
            None => Ok(()),
        }
    }
}

/// The platform's OS store: the macOS Keychain on macOS, the Secret Service on Linux; other
/// platforms have none.
#[cfg(target_os = "macos")]
fn os_store(_paths: &Paths) -> Option<Box<dyn OsStore>> {
    Some(Box::new(crate::macos::MacosStore::new(Box::new(
        SystemRunner,
    ))))
}

#[cfg(target_os = "linux")]
fn os_store(_paths: &Paths) -> Option<Box<dyn OsStore>> {
    Some(Box::new(crate::linux::LinuxStore::new(Box::new(
        SystemRunner,
    ))))
}

#[cfg(not(any(target_os = "macos", target_os = "linux")))]
fn os_store(_paths: &Paths) -> Option<Box<dyn OsStore>> {
    None
}

/// The two halves `platform_default` joins. The router's key cache calls this directly: it gives
/// the os store a shorter budget than the file.
pub fn platform_parts(paths: &Paths) -> (Option<Box<dyn OsStore>>, FileStore) {
    let file = FileStore::new(paths.config_dir.clone(), paths.home.clone());
    let os = if runs_as_login_account(paths) {
        os_store(paths)
    } else {
        None
    };
    (os, file)
}

pub fn platform_default(paths: &Paths) -> Box<dyn Keystore> {
    let (os, file) = platform_parts(paths);
    Box::new(Layered { os, file })
}

/// True when `paths.home`, made absolute and cleaned, equals the login account's home from
/// getpwuid_r. A sandbox or a test home is a different account: no OS store.
fn runs_as_login_account(paths: &Paths) -> bool {
    match (clean_absolute(&paths.home), login_home()) {
        (Some(home), Some(login)) => home == login,
        _ => false,
    }
}

/// The path made absolute, `.` dropped and `..` resolved, without following symlinks.
fn clean_absolute(path: &Path) -> Option<PathBuf> {
    let absolute = std::path::absolute(path).ok()?;
    let mut parts: Vec<OsString> = Vec::new();
    for component in absolute.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                parts.pop();
            }
            component => parts.push(component.as_os_str().to_os_string()),
        }
    }
    let mut cleaned = PathBuf::new();
    for part in parts {
        cleaned.push(part);
    }
    Some(cleaned)
}

/// The login account's home directory, or none when the account database has no entry.
fn login_home() -> Option<PathBuf> {
    let uid = unsafe { libc::getuid() };
    let mut entry: libc::passwd = unsafe { std::mem::zeroed() };
    let mut buffer = vec![0u8; 4096];
    let mut result: *mut libc::passwd = std::ptr::null_mut();
    let status = unsafe {
        libc::getpwuid_r(
            uid,
            &mut entry,
            buffer.as_mut_ptr() as *mut libc::c_char,
            buffer.len(),
            &mut result,
        )
    };
    if status != 0 || result.is_null() {
        return None;
    }
    let home = unsafe { CStr::from_ptr(entry.pw_dir) }.to_bytes();
    if home.is_empty() {
        return None;
    }
    Some(PathBuf::from(OsStr::from_bytes(home)))
}
