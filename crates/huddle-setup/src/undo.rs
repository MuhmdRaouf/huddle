//! The undo of the settings patch: puts settings.json back the way the ledger found it. A file
//! nothing else touched comes back byte for byte; a file something else changed keeps its new
//! shape and loses only huddle's keys. A proxy that is no longer huddle's stops the undo.

use std::fs;
use std::fs::File;
use std::io;
use std::io::Write as _;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

use serde_json::{Map, Value};

use huddle_config::paths::Paths;

use crate::settings::{
    Ledger, SettingsError, backup_path, drop_pristine, ledger_path, load_doc, pristine_doc,
    settings_path, sha256_hex, write_doc,
};

/// What one `undo` did.
#[derive(Debug, PartialEq, Eq)]
pub enum UndoOutcome {
    /// No ledger: nothing was ever patched.
    Nothing,
    /// The file is back; `exact` is true when its bytes equal the original.
    Restored { exact: bool },
    /// The proxy is no longer huddle's; nothing was touched except the ledger.
    NotOurs,
}

pub fn undo(paths: &Paths) -> Result<UndoOutcome, SettingsError> {
    let Some(ledger) = read_ledger(&ledger_path(paths))? else {
        drop_pristine(paths)?;
        return Ok(UndoOutcome::Nothing);
    };
    let settings = settings_path(paths);
    let backup = backup_path(paths);
    let current = match fs::read(&settings) {
        Ok(bytes) => Some(bytes),
        Err(source) if source.kind() == io::ErrorKind::NotFound => None,
        Err(source) => {
            return Err(SettingsError::Io {
                path: settings.clone(),
                source,
            });
        }
    };

    // The file still holds the bytes huddle wrote: the original comes straight back.
    if let Some(bytes) = &current
        && sha256_hex(bytes) == ledger.after_sha256
    {
        if ledger.created_file {
            remove_file_if_there(&settings)?;
        } else {
            let original = fs::read(&backup).map_err(|source| SettingsError::Io {
                path: backup.clone(),
                source,
            })?;
            restore_bytes(&settings, &original)?;
        }
        drop_files(paths)?;
        drop_pristine(paths)?;
        return Ok(UndoOutcome::Restored { exact: true });
    }

    // Something else changed the file: take only huddle's keys back out.
    let (_, mut doc) = load_doc(&settings)?;
    if current.is_none() {
        drop_files(paths)?;
        drop_pristine(paths)?;
        return Ok(UndoOutcome::Restored { exact: true });
    }

    let proxy_ours = doc
        .get("env")
        .and_then(|env| env.get("HTTPS_PROXY"))
        .and_then(Value::as_str)
        .map(proxy_is_ours)
        .unwrap_or(true);
    if !proxy_ours {
        drop_files(paths)?;
        drop_pristine(paths)?;
        return Ok(UndoOutcome::NotOurs);
    }

    if let Some(env) = doc.get_mut("env").and_then(Value::as_object_mut) {
        for (key, before) in &ledger.before {
            match before {
                Some(value) => {
                    env.insert(key.clone(), Value::String(value.clone()));
                }
                None => {
                    // Tool search was huddle's doing only while it still says what huddle wrote.
                    if key == "ENABLE_TOOL_SEARCH"
                        && env.get(key).and_then(Value::as_str) != Some("true")
                    {
                        continue;
                    }
                    env.shift_remove(key);
                }
            }
        }
    }
    if ledger.created_env
        && doc
            .get("env")
            .is_some_and(|env| env.as_object().is_some_and(Map::is_empty))
    {
        doc.shift_remove("env");
    }

    // What is left equals the pristine copy: its own bytes come back, formatting included,
    // however many other writers touched the file in between.
    if let Some((bytes, pristine)) = pristine_doc(paths)?
        && doc == pristine
    {
        restore_bytes(&settings, &bytes)?;
        drop_files(paths)?;
        drop_pristine(paths)?;
        return Ok(UndoOutcome::Restored { exact: true });
    }

    // A result equal to the backup gets the backup's own bytes, unusual formatting included.
    if let Ok((Some(original), backup_doc)) = load_doc(&backup)
        && doc == backup_doc
    {
        restore_bytes(&settings, &original)?;
        drop_files(paths)?;
        drop_pristine(paths)?;
        return Ok(UndoOutcome::Restored { exact: true });
    }

    write_doc(&settings, &doc, current.as_deref())?;
    drop_files(paths)?;
    drop_pristine(paths)?;
    Ok(UndoOutcome::Restored { exact: false })
}

/// Whether an `HTTPS_PROXY` value names a loopback router: huddle never adopts a loopback proxy
/// the user set, so any loopback value counts as ours.
fn proxy_is_ours(value: &str) -> bool {
    let without_scheme = value
        .strip_prefix("http://")
        .or_else(|| value.strip_prefix("https://"))
        .unwrap_or(value);
    without_scheme.starts_with("127.")
        || without_scheme.starts_with("localhost:")
        || without_scheme.starts_with("[::1]:")
}

/// Writes `bytes` over `path` through a temp file beside it, renamed over the target: a reader
/// sees the old file or the new one. The file's current mode is kept. A failure before the
/// rename removes the temp file.
fn restore_bytes(path: &Path, bytes: &[u8]) -> Result<(), SettingsError> {
    let temp = PathBuf::from(format!(
        "{}.huddle-tmp.{}",
        path.display(),
        std::process::id()
    ));
    let land = || -> Result<(), SettingsError> {
        let mode = match fs::metadata(path) {
            Ok(meta) => meta.permissions().mode() & 0o777,
            Err(_) => 0o600,
        };
        let mut file = File::create(&temp).map_err(|source| SettingsError::Io {
            path: temp.clone(),
            source,
        })?;
        file.write_all(bytes).map_err(|source| SettingsError::Io {
            path: temp.clone(),
            source,
        })?;
        file.sync_all().map_err(|source| SettingsError::Io {
            path: temp.clone(),
            source,
        })?;
        let mut permissions = file
            .metadata()
            .map_err(|source| SettingsError::Io {
                path: temp.clone(),
                source,
            })?
            .permissions();
        permissions.set_mode(mode);
        file.set_permissions(permissions)
            .map_err(|source| SettingsError::Io {
                path: temp.clone(),
                source,
            })?;
        fs::rename(&temp, path).map_err(|source| SettingsError::Io {
            path: path.to_path_buf(),
            source,
        })
    };
    if let Err(err) = land() {
        let _ = fs::remove_file(&temp);
        return Err(err);
    }
    Ok(())
}

fn drop_files(paths: &Paths) -> Result<(), SettingsError> {
    remove_file_if_there(&ledger_path(paths))?;
    remove_file_if_there(&backup_path(paths))?;
    Ok(())
}

fn remove_file_if_there(path: &Path) -> Result<(), SettingsError> {
    match fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(source) if source.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(source) => Err(SettingsError::Io {
            path: path.to_path_buf(),
            source,
        }),
    }
}

fn read_ledger(path: &Path) -> Result<Option<Ledger>, SettingsError> {
    match fs::read(path) {
        Ok(bytes) => serde_json::from_slice(&bytes)
            .map(Some)
            .map_err(|err| SettingsError::Json {
                path: path.to_path_buf(),
                message: err.to_string(),
            }),
        Err(source) if source.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(source) => Err(SettingsError::Io {
            path: path.to_path_buf(),
            source,
        }),
    }
}
