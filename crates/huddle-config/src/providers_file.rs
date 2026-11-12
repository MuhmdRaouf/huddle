//! providers.toml: the providers huddle-router serves, re-read on every poll so enabling a
//! provider needs no restart. The names are recorded as they are given and dropped when no
//! compiled provider owns them, so the file can never turn a typo into a lane. Every write is
//! locked and atomic, and every key the user added to the file survives it.

use std::fmt;
use std::fs::{self, OpenOptions};
use std::io;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::thread;
use std::time::{Duration, Instant};

use serde::Deserialize;
use toml_edit::{Array, DocumentMut, Item};

use crate::atomic::write_atomic;
use crate::files::DIR_MODE;

/// The file's name inside the config dir.
pub const PROVIDERS_FILE: &str = "providers.toml";
/// The file the lock is taken on: the write replaces providers.toml's inode, so a lock on it
/// would not survive the rename.
pub const LOCK_FILE: &str = "providers.toml.lock";

/// How long `set_enabled` waits for the lock. The critical section is a read-modify-write of a
/// file of a few names, so a longer wait hides a stuck writer.
const LOCK_WAIT: Duration = Duration::from_secs(5);
/// The pause between lock tries.
const LOCK_RETRY: Duration = Duration::from_millis(50);

/// What reading or writing providers.toml can refuse.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ProvidersFileError {
    /// Any read error other than a missing file: `<file>: <message>`.
    Read { file: String, message: String },
    /// Malformed or wrong-shape file: `<file>: not valid providers.toml (<detail>)`.
    Invalid { file: String, detail: String },
    /// The lock is held past the 5 s wait: `<file>: lock held past 5 s`.
    Locked { file: String },
    /// Writing the file failed: `<file>: <message>`.
    Write { file: String, message: String },
}

impl fmt::Display for ProvidersFileError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ProvidersFileError::Read { file, message } => write!(f, "{file}: {message}"),
            ProvidersFileError::Invalid { file, detail } => {
                write!(f, "{file}: not valid providers.toml ({detail})")
            }
            ProvidersFileError::Locked { file } => write!(f, "{file}: lock held past 5 s"),
            ProvidersFileError::Write { file, message } => write!(f, "{file}: {message}"),
        }
    }
}

impl std::error::Error for ProvidersFileError {}

/// `<config_dir>/providers.toml`.
/// The file `setup --install` creates when `providers.toml` is missing: no provider enabled.
pub const DEFAULT_PROVIDERS: &str = "\
# Providers whose lane huddle-router serves. /huddle:<provider>:setup adds one, /huddle:<provider>:remove takes it out.
providers = []
";

pub fn providers_path(dir: &Path) -> PathBuf {
    dir.join(PROVIDERS_FILE)
}

/// The file's shape. `serde(default)` keeps a missing key from failing the parse; a key that is
/// there but of the wrong type is a parse error, which the reads turn into `Invalid`.
#[derive(Debug, Default, Deserialize)]
struct FileShape {
    #[serde(default)]
    providers: Vec<String>,
}

/// Reads providers.toml and answers the enabled providers filtered to `known`, in `known`
/// order. A missing file is an empty list; any other read error, or TOML of the wrong shape, is
/// an error.
pub fn read_enabled(dir: &Path, known: &[&str]) -> Result<Vec<String>, ProvidersFileError> {
    let path = providers_path(dir);
    let parsed = parse_file(&path)?;
    Ok(known
        .iter()
        .filter(|name| parsed.providers.contains(&name.to_string()))
        .map(|name| name.to_string())
        .collect())
}

/// Turns a provider on or off. The read-modify-write runs under the lock file, and the write
/// itself is atomic, so a reader sees the whole old file or the whole new one. No write, and no
/// mtime change, when the provider is already in the wanted state.
pub fn set_enabled(dir: &Path, name: &str, on: bool) -> Result<(), ProvidersFileError> {
    let path = providers_path(dir);
    let lock_path = dir.join(LOCK_FILE);
    let file_text = path.display().to_string();
    let lock_text = lock_path.display().to_string();

    crate::files::create_dir(dir, DIR_MODE).map_err(|err| ProvidersFileError::Write {
        file: file_text.clone(),
        message: err.to_string(),
    })?;
    let lock = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .mode(0o600)
        .open(&lock_path)
        .map_err(|err| ProvidersFileError::Write {
            file: lock_text.clone(),
            message: err.to_string(),
        })?;
    take_lock(&lock, &lock_text)?;

    let mut doc = read_current(&path)?;
    let mut providers = providers_of(&doc, &file_text)?;
    let has = providers.iter().any(|kept| kept == name);
    if has == on {
        return Ok(());
    }
    if on {
        providers.push(name.to_string());
    } else {
        providers.retain(|kept| kept != name);
    }
    match doc.get_mut("providers").and_then(Item::as_array_mut) {
        Some(array) => {
            array.clear();
            array.extend(providers.iter().map(String::as_str));
            array.fmt();
        }
        None => {
            let array: Array = providers.iter().map(String::as_str).collect();
            doc.insert("providers", toml_edit::value(array));
        }
    }
    let text = doc.to_string();
    write_atomic(&path, text.as_bytes(), crate::files::FILE_MODE).map_err(|err| {
        ProvidersFileError::Write {
            file: file_text,
            message: err.to_string(),
        }
    })
}

/// Parses providers.toml into the file's shape, telling a missing file from a broken one.
fn parse_file(path: &Path) -> Result<FileShape, ProvidersFileError> {
    let text = path.display().to_string();
    let raw = match fs::read(path) {
        Ok(raw) => raw,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(FileShape::default()),
        Err(err) => {
            return Err(ProvidersFileError::Read {
                file: text,
                message: err.to_string(),
            });
        }
    };
    let raw = String::from_utf8_lossy(&raw);
    toml::from_str(&raw).map_err(|err| ProvidersFileError::Invalid {
        file: text,
        detail: err.message().to_string(),
    })
}

/// Parses the current file for an in-place edit, starting from [`DEFAULT_PROVIDERS`] when the
/// file is missing or unreadable, so a fresh config dir is a valid starting point. Every key,
/// comment and blank line the user wrote stays.
fn read_current(path: &Path) -> Result<DocumentMut, ProvidersFileError> {
    let text = match fs::read(path) {
        Ok(raw) => String::from_utf8_lossy(&raw).into_owned(),
        Err(_) => DEFAULT_PROVIDERS.to_string(),
    };
    text.parse::<DocumentMut>()
        .map_err(|err| ProvidersFileError::Invalid {
            file: path.display().to_string(),
            detail: err.message().to_string(),
        })
}

/// The `providers` list of the current file, refusing a value of the wrong shape.
fn providers_of(doc: &DocumentMut, file: &str) -> Result<Vec<String>, ProvidersFileError> {
    let wrong = || ProvidersFileError::Invalid {
        file: file.to_string(),
        detail: "providers is not a list of names".to_string(),
    };
    match doc.get("providers") {
        None => Ok(Vec::new()),
        Some(item) => item
            .as_array()
            .ok_or_else(wrong)?
            .iter()
            .map(|value| value.as_str().map(str::to_string).ok_or_else(wrong))
            .collect(),
    }
}

/// Takes the file lock, waiting up to 5 s in 50 ms steps. The lock dies with the `File`, so the
/// caller keeps it in scope for the whole critical section.
fn take_lock(file: &fs::File, lock_text: &str) -> Result<(), ProvidersFileError> {
    let deadline = Instant::now() + LOCK_WAIT;
    loop {
        match file.try_lock() {
            Ok(()) => return Ok(()),
            Err(std::fs::TryLockError::WouldBlock) => {
                if Instant::now() >= deadline {
                    return Err(ProvidersFileError::Locked {
                        file: lock_text.to_string(),
                    });
                }
                thread::sleep(LOCK_RETRY);
            }
            Err(std::fs::TryLockError::Error(err)) => {
                return Err(ProvidersFileError::Write {
                    file: lock_text.to_string(),
                    message: err.to_string(),
                });
            }
        }
    }
}
