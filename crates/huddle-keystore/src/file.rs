//! The key file `~/.config/<p>-plugin-cc/env`: `NAME=<key>` lines, 0600 in a 0700 directory.

use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::port::{Keystore, KeystoreError, SecretString, assert_valid_key, service_name};
use huddle_config::files::{PRIVATE_DIR_MODE, create_dir};

/// The mode every key file is written with.
const PRIVATE_FILE: u32 = 0o600;

/// The key file as a store: always available, the first key variable name is the line it writes.
pub struct FileStore {
    home: PathBuf,
}

impl FileStore {
    pub fn new(home: PathBuf) -> FileStore {
        FileStore { home }
    }

    /// The provider's key file.
    pub fn path_for(&self, provider: &str) -> PathBuf {
        self.home
            .join(".config")
            .join(service_name(provider))
            .join("env")
    }
}

/// The key variable names a line may use for this provider, first one is written.
pub fn key_env_names(provider: &str) -> Vec<String> {
    let mut names = vec![format!("{}_API_KEY", provider.to_uppercase())];
    if provider == "qwen" {
        names.push("DASHSCOPE_API_KEY".to_string());
    }
    names
}

impl Keystore for FileStore {
    fn get(&self, provider: &str) -> Result<Option<SecretString>, KeystoreError> {
        let path = self.path_for(provider);
        // One open handle serves the mode checks and the read, so the file cannot be swapped in
        // between.
        let mut handle = match File::open(&path) {
            Ok(handle) => handle,
            Err(source) => return missing_or_read(&path, source),
        };
        let mode = handle
            .metadata()
            .map_err(|source| read_error(&path, source))?
            .permissions()
            .mode();
        if mode & 0o077 != 0 {
            return Err(KeystoreError::InsecureFile {
                path,
                mode: mode & 0o777,
            });
        }
        let dir = path
            .parent()
            .ok_or_else(|| read_error(&path, no_parent()))?
            .to_path_buf();
        let dir_mode = fs::metadata(&dir)
            .map_err(|source| read_error(&dir, source))?
            .permissions()
            .mode();
        if dir_mode & 0o022 != 0 {
            return Err(KeystoreError::InsecureDir {
                path: dir,
                mode: dir_mode & 0o777,
            });
        }
        let mut text = String::new();
        handle
            .read_to_string(&mut text)
            .map_err(|source| read_error(&path, source))?;
        let names = key_env_names(provider);
        for line in text.lines() {
            if let Some(value) = line_value(line, &names) {
                if value.is_empty() {
                    continue;
                }
                return Ok(Some(SecretString::new(value)));
            }
        }
        Ok(None)
    }

    fn set(&self, provider: &str, key: &SecretString) -> Result<(), KeystoreError> {
        assert_valid_key(key.expose())?;
        let path = self.path_for(provider);
        let names = key_env_names(provider);
        let current = read_text(&path)?.unwrap_or_default();
        let mut kept: Vec<&str> = Vec::new();
        for line in current.lines() {
            if line_value(line, &names).is_none() {
                kept.push(line);
            }
        }
        while kept.last().is_some_and(|line| line.trim().is_empty()) {
            kept.pop();
        }
        let key_line = format!("{}={}", names[0], key.expose());
        kept.push(&key_line);
        write_private(&path, &format!("{}\n", kept.join("\n")))
    }

    fn delete(&self, provider: &str) -> Result<(), KeystoreError> {
        let path = self.path_for(provider);
        let names = key_env_names(provider);
        let text = match read_text(&path)? {
            Some(text) => text,
            None => return Ok(()),
        };
        let mut kept: Vec<&str> = Vec::new();
        let mut removed = false;
        for line in text.lines() {
            if line_value(line, &names).is_some() {
                removed = true;
            } else {
                kept.push(line);
            }
        }
        if !removed {
            return Ok(());
        }
        if kept.iter().all(|line| line.trim().is_empty()) {
            return fs::remove_file(&path).map_err(|source| write_error(&path, source));
        }
        write_private(&path, &format!("{}\n", kept.join("\n")))
    }
}

/// The file's whole text, none when there is no file.
fn read_text(path: &Path) -> Result<Option<String>, KeystoreError> {
    match fs::read(path) {
        Ok(bytes) => Ok(Some(String::from_utf8_lossy(&bytes).into_owned())),
        Err(source) => missing_or_read(path, source).map(|_| None),
    }
}

/// A missing file is no key; any other open or read failure names the errno.
fn missing_or_read(path: &Path, source: io::Error) -> Result<Option<SecretString>, KeystoreError> {
    if source.kind() == io::ErrorKind::NotFound {
        return Ok(None);
    }
    Err(read_error(path, source))
}

fn read_error(path: &Path, source: io::Error) -> KeystoreError {
    KeystoreError::Read {
        path: path.to_path_buf(),
        source,
    }
}

fn write_error(path: &Path, source: io::Error) -> KeystoreError {
    KeystoreError::Write {
        path: path.to_path_buf(),
        source,
    }
}

fn no_parent() -> io::Error {
    io::Error::new(io::ErrorKind::NotFound, "no parent directory")
}

/// The key a raw line holds, when it holds one: optional `export`, a key name, `=`, the value
/// with one pair of matching quotes gone.
fn line_value(raw: &str, names: &[String]) -> Option<String> {
    let line = raw.trim();
    let line = match line.strip_prefix("export") {
        Some(rest) if rest.starts_with(char::is_whitespace) => rest.trim_start(),
        _ => line,
    };
    let mut tail = None;
    for name in names {
        if let Some(rest) = line.strip_prefix(name.as_str()) {
            tail = Some(rest);
            break;
        }
    }
    let value = tail?.trim_start().strip_prefix('=')?.trim();
    Some(unquote(value).to_string())
}

/// One pair of matching `"` or `'` quotes gone.
fn unquote(value: &str) -> &str {
    let bytes = value.as_bytes();
    if bytes.len() >= 2
        && (bytes[0] == b'"' || bytes[0] == b'\'')
        && bytes[bytes.len() - 1] == bytes[0]
    {
        return &value[1..value.len() - 1];
    }
    value
}

/// Writes text as a 0600 file in a 0700 directory, atomically: temp file in the same directory,
/// synced, renamed over the target.
fn write_private(path: &Path, text: &str) -> Result<(), KeystoreError> {
    let dir = path
        .parent()
        .ok_or_else(|| write_error(path, no_parent()))?;
    create_dir(dir, PRIVATE_DIR_MODE).map_err(|source| write_error(path, source))?;
    let temp = temp_path(path);
    let write = || -> io::Result<()> {
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(PRIVATE_FILE)
            .open(&temp)?;
        file.write_all(text.as_bytes())?;
        file.set_permissions(fs::Permissions::from_mode(PRIVATE_FILE))?;
        file.sync_all()
    };
    if let Err(source) = write() {
        let _ = fs::remove_file(&temp);
        return Err(write_error(path, source));
    }
    if let Err(source) = fs::rename(&temp, path) {
        let _ = fs::remove_file(&temp);
        return Err(write_error(path, source));
    }
    Ok(())
}

/// A hidden temp file next to the target, named once per write.
fn temp_path(path: &Path) -> PathBuf {
    let name = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("env");
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_nanos())
        .unwrap_or(0);
    let mut temp = path.to_path_buf();
    temp.set_file_name(format!(".{name}.tmp-{nanos}"));
    temp
}
