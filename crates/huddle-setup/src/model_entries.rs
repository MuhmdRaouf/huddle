//! A provider's entries in Claude Code's `/model`: the picker rows, the `availableModels` ids
//! and the default effort levels, with a ledger that names what was added.

use std::collections::BTreeMap;
use std::fs;
use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

use huddle_catalog::providers::{ModelRef, Provider};
use huddle_config::atomic::write_atomic;
use huddle_config::paths::Paths;
use serde_json::{Map, Value};

use crate::settings::{
    SettingsError, drop_pristine, load_doc, pristine_doc, save_pristine, settings_path, sha256_hex,
    write_doc,
};

/// What the provider added, so a later removal can take it back out.
#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelLedger {
    pub created_file: bool,
    pub options_added: Vec<String>,
    pub available_appended: Vec<String>,
    pub efforts_added: BTreeMap<String, String>,
    pub created_model_picker: bool,
    pub created_options: bool,
    pub created_model_settings: bool,
    pub after_sha256: String,
}

/// What one `apply` did.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ModelOutcome {
    pub changed: bool,
    pub lines: Vec<String>,
}

/// The provider's two catalog models, in picker order.
fn models(provider: &Provider) -> [ModelRef; 2] {
    [provider.main, provider.flash]
}

/// Adds the provider's models to Claude Code's settings.json: one `modelPicker.options` row per
/// catalog model, the ids into `availableModels` when that list already exists, and the default
/// effort level per model that publishes one. Idempotent: a run with nothing to change writes
/// nothing. A wrong-typed key refuses with nothing written.
pub fn apply(paths: &Paths, provider: &'static Provider) -> Result<ModelOutcome, SettingsError> {
    let settings = settings_path(paths);
    let (original, mut doc) = load_doc(&settings)?;

    if let Some(picker) = doc.get("modelPicker")
        && !picker.is_object()
    {
        return Err(SettingsError::Shape {
            path: settings.clone(),
            what: "modelPicker must be an object; nothing changed".to_string(),
        });
    }
    if let Some(options) = doc
        .get("modelPicker")
        .and_then(|picker| picker.get("options"))
        && !options.is_array()
    {
        return Err(SettingsError::Shape {
            path: settings.clone(),
            what: "modelPicker.options must be an array; nothing changed".to_string(),
        });
    }
    if let Some(available) = doc.get("availableModels")
        && !available.is_array()
    {
        return Err(SettingsError::Shape {
            path: settings.clone(),
            what: "availableModels must be an array; nothing changed".to_string(),
        });
    }
    if let Some(model_settings) = doc.get("modelSettings")
        && !model_settings.is_object()
    {
        return Err(SettingsError::Shape {
            path: settings.clone(),
            what: "modelSettings must be an object; nothing changed".to_string(),
        });
    }

    let mut created_model_picker = false;
    let mut created_options = false;
    let mut created_model_settings = false;
    let mut options_added = Vec::new();
    let mut available_appended = Vec::new();
    let mut efforts_added = BTreeMap::new();
    let mut lines = Vec::new();

    if doc.get("modelPicker").is_none() {
        created_model_picker = true;
        doc.insert("modelPicker".to_string(), Value::Object(Map::new()));
    }
    let Some(picker) = doc.get_mut("modelPicker").and_then(Value::as_object_mut) else {
        return Err(shape(
            settings,
            "modelPicker must be an object; nothing changed",
        ));
    };
    if picker.get("options").is_none() {
        created_options = true;
        picker.insert("options".to_string(), Value::Array(Vec::new()));
    }
    let Some(options) = picker.get_mut("options").and_then(Value::as_array_mut) else {
        return Err(shape(
            settings,
            "modelPicker.options must be an array; nothing changed",
        ));
    };

    for model in models(provider) {
        let found = options
            .iter()
            .position(|row| row.get("model").and_then(Value::as_str) == Some(model.id));
        match found {
            None => {
                let mut row = Map::new();
                row.insert("model".to_string(), Value::String(model.id.to_string()));
                row.insert("label".to_string(), Value::String(model.label.to_string()));
                if let Some(behaves_as) = model.behaves_as {
                    row.insert(
                        "behavesAs".to_string(),
                        Value::String(behaves_as.to_string()),
                    );
                }
                options.push(Value::Object(row));
                options_added.push(model.id.to_string());
                lines.push(format!("added {} ({}) to /model", model.label, model.id));
            }
            Some(index) => {
                let Some(row) = options[index].as_object_mut() else {
                    continue;
                };
                if row.get("label").and_then(Value::as_str) != Some(model.label) {
                    row.insert("label".to_string(), Value::String(model.label.to_string()));
                    lines.push(format!(
                        "updated the {} ({}) label in /model",
                        model.label, model.id
                    ));
                }
            }
        }
    }

    if let Some(available) = doc.get_mut("availableModels").and_then(Value::as_array_mut) {
        for model in models(provider) {
            let held = available
                .iter()
                .any(|value| value.as_str() == Some(model.id));
            if !held {
                available.push(Value::String(model.id.to_string()));
                available_appended.push(model.id.to_string());
                lines.push(format!("added {} to availableModels", model.id));
            }
        }
    }

    for model in models(provider) {
        let Some(level) = model.default_effort else {
            continue;
        };
        if doc.get("modelSettings").is_none() {
            created_model_settings = true;
            doc.insert("modelSettings".to_string(), Value::Object(Map::new()));
        }
        let Some(model_settings) = doc.get_mut("modelSettings").and_then(Value::as_object_mut)
        else {
            return Err(shape(
                settings,
                "modelSettings must be an object; nothing changed",
            ));
        };
        let has_level = model_settings
            .get(model.id)
            .and_then(|entry| entry.get("effortLevel"))
            .is_some();
        if has_level {
            continue;
        }
        let entry = model_settings
            .entry(model.id.to_string())
            .or_insert_with(|| Value::Object(Map::new()));
        let Some(entry) = entry.as_object_mut() else {
            return Err(shape(
                settings,
                format!(
                    "modelSettings.{} must be an object; nothing changed",
                    model.id
                ),
            ));
        };
        entry.insert("effortLevel".to_string(), Value::String(level.to_string()));
        efforts_added.insert(model.id.to_string(), level.to_string());
        lines.push(format!("set {} default effort to {}", model.label, level));
    }

    if lines.is_empty() {
        return Ok(ModelOutcome::default());
    }

    save_pristine(paths, original.as_deref())?;
    let ledger_file = ledger_path(paths, provider.id);
    let mut ledger = match read_ledger(&ledger_file)? {
        Some(ledger) => ledger,
        None => {
            if let Some(bytes) = original.as_deref() {
                let backup = backup_path(paths, provider.id);
                write_atomic(&backup, bytes, 0o600).map_err(|source| SettingsError::Io {
                    path: backup,
                    source,
                })?;
            }
            ModelLedger {
                created_file: original.is_none(),
                ..ModelLedger::default()
            }
        }
    };
    for id in options_added {
        if !ledger.options_added.contains(&id) {
            ledger.options_added.push(id);
        }
    }
    for id in available_appended {
        if !ledger.available_appended.contains(&id) {
            ledger.available_appended.push(id);
        }
    }
    for (id, level) in efforts_added {
        ledger.efforts_added.entry(id).or_insert(level);
    }
    ledger.created_model_picker |= created_model_picker;
    ledger.created_options |= created_options;
    ledger.created_model_settings |= created_model_settings;

    let bytes = write_doc(&settings, &doc, original.as_deref())?;
    ledger.after_sha256 = sha256_hex(&bytes);
    write_ledger(&ledger_file, &ledger)?;
    Ok(ModelOutcome {
        changed: true,
        lines,
    })
}

/// What one `remove` did.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RemoveOutcome {
    /// No ledger: this provider added nothing.
    Nothing,
    /// The settings are back; `exact` is true when the bytes equal the original.
    Removed { exact: bool, lines: Vec<String> },
}

/// Takes the provider's entries back out of Claude Code's settings.json, the ledger's way: a
/// file nothing else touched comes back byte for byte, a file something else changed keeps its
/// new shape and loses only this provider's entries. The ledger and the backup go with them.
pub fn remove(paths: &Paths, provider: &'static Provider) -> Result<RemoveOutcome, SettingsError> {
    let Some(ledger) = read_ledger(&ledger_path(paths, provider.id))? else {
        return Ok(RemoveOutcome::Nothing);
    };
    let settings = settings_path(paths);
    let backup = backup_path(paths, provider.id);
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

    // The file still holds the bytes this provider wrote: the original comes straight back.
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
        drop_files(paths, provider.id)?;
        drop_pristine(paths)?;
        return Ok(RemoveOutcome::Removed {
            exact: true,
            lines: ledger_lines(&ledger),
        });
    }

    // The file is gone: nothing of this provider's is left to take out.
    if current.is_none() {
        drop_files(paths, provider.id)?;
        drop_pristine(paths)?;
        return Ok(RemoveOutcome::Removed {
            exact: true,
            lines: ledger_lines(&ledger),
        });
    }

    // Something else changed the file: take only this provider's entries back out.
    let (_, mut doc) = load_doc(&settings)?;
    let mut lines = Vec::new();
    if let Some(picker) = doc.get_mut("modelPicker").and_then(Value::as_object_mut) {
        let mut options_removed = false;
        if let Some(options) = picker.get_mut("options").and_then(Value::as_array_mut) {
            let before = options.len();
            options.retain(|row| {
                let id = row.get("model").and_then(Value::as_str);
                !id.is_some_and(|id| ledger.options_added.iter().any(|added| added == id))
            });
            options_removed = options.len() != before;
        }
        if options_removed {
            lines.push(format!(
                "took {} out of /model",
                ledger.options_added.join(" and ")
            ));
        }
        if ledger.created_options
            && picker
                .get("options")
                .and_then(Value::as_array)
                .is_some_and(|options| options.is_empty())
        {
            picker.shift_remove("options");
        }
    }
    if ledger.created_model_picker
        && doc
            .get("modelPicker")
            .and_then(Value::as_object)
            .is_some_and(Map::is_empty)
    {
        doc.shift_remove("modelPicker");
    }

    if let Some(available) = doc.get_mut("availableModels").and_then(Value::as_array_mut)
        && !ledger.available_appended.is_empty()
    {
        let before = available.len();
        available.retain(|value| {
            let id = value.as_str();
            !id.is_some_and(|id| ledger.available_appended.iter().any(|added| added == id))
        });
        if available.len() != before {
            lines.push(format!(
                "took {} out of availableModels",
                ledger.available_appended.join(" and ")
            ));
        }
    }

    if let Some(model_settings) = doc.get_mut("modelSettings").and_then(Value::as_object_mut)
        && !ledger.efforts_added.is_empty()
    {
        for (id, level) in &ledger.efforts_added {
            let Some(entry) = model_settings
                .get_mut(id.as_str())
                .and_then(Value::as_object_mut)
            else {
                continue;
            };
            if entry.get("effortLevel").and_then(Value::as_str) != Some(level.as_str()) {
                continue;
            }
            entry.shift_remove("effortLevel");
            if entry.is_empty() {
                model_settings.shift_remove(id.as_str());
            }
        }
    }
    if ledger.created_model_settings
        && doc
            .get("modelSettings")
            .and_then(Value::as_object)
            .is_some_and(Map::is_empty)
    {
        doc.shift_remove("modelSettings");
    }

    // This provider created the file and nothing of the user's is left: the file goes.
    if ledger.created_file && doc.is_empty() {
        remove_file_if_there(&settings)?;
        drop_files(paths, provider.id)?;
        drop_pristine(paths)?;
        return Ok(RemoveOutcome::Removed { exact: true, lines });
    }

    // What is left equals the pristine copy: its own bytes come back, formatting included,
    // however many other writers touched the file in between.
    if let Some((bytes, pristine)) = pristine_doc(paths)?
        && doc == pristine
    {
        restore_bytes(&settings, &bytes)?;
        drop_files(paths, provider.id)?;
        drop_pristine(paths)?;
        return Ok(RemoveOutcome::Removed { exact: true, lines });
    }

    // A result equal to the backup gets the backup's own bytes, unusual formatting included.
    if let Ok((Some(original), backup_doc)) = load_doc(&backup)
        && doc == backup_doc
    {
        restore_bytes(&settings, &original)?;
        drop_files(paths, provider.id)?;
        drop_pristine(paths)?;
        return Ok(RemoveOutcome::Removed { exact: true, lines });
    }

    write_doc(&settings, &doc, current.as_deref())?;
    drop_files(paths, provider.id)?;
    drop_pristine(paths)?;
    Ok(RemoveOutcome::Removed {
        exact: false,
        lines,
    })
}

/// The report lines a ledger's removal produces: the /model ids when options went, the
/// availableModels ids when any did.
fn ledger_lines(ledger: &ModelLedger) -> Vec<String> {
    let mut lines = Vec::new();
    if !ledger.options_added.is_empty() {
        lines.push(format!(
            "took {} out of /model",
            ledger.options_added.join(" and ")
        ));
    }
    if !ledger.available_appended.is_empty() {
        lines.push(format!(
            "took {} out of availableModels",
            ledger.available_appended.join(" and ")
        ));
    }
    lines
}

/// `<state_dir>/providers/<provider>-ledger.json`.
pub fn ledger_path(paths: &Paths, provider: &str) -> PathBuf {
    paths
        .state_dir
        .join("providers")
        .join(format!("{provider}-ledger.json"))
}

/// `settings.json.<provider>-backup`, beside settings.json.
pub fn backup_path(paths: &Paths, provider: &str) -> PathBuf {
    let mut path = settings_path(paths).into_os_string();
    path.push(format!(".{provider}-backup"));
    PathBuf::from(path)
}

/// A shape refusal against settings.json.
fn shape(path: PathBuf, what: impl Into<String>) -> SettingsError {
    SettingsError::Shape {
        path,
        what: what.into(),
    }
}

fn read_ledger(path: &Path) -> Result<Option<ModelLedger>, SettingsError> {
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

fn write_ledger(path: &Path, ledger: &ModelLedger) -> Result<(), SettingsError> {
    let mut json = serde_json::to_string_pretty(ledger).map_err(|err| SettingsError::Json {
        path: path.to_path_buf(),
        message: err.to_string(),
    })?;
    json.push('\n');
    write_atomic(path, json.as_bytes(), 0o600).map_err(|source| SettingsError::Io {
        path: path.to_path_buf(),
        source,
    })
}

/// Writes `bytes` over `path` atomically, the file's current mode kept.
fn restore_bytes(path: &Path, bytes: &[u8]) -> Result<(), SettingsError> {
    let mode = match fs::metadata(path) {
        Ok(meta) => meta.permissions().mode() & 0o777,
        Err(_) => 0o600,
    };
    write_atomic(path, bytes, mode).map_err(|source| SettingsError::Io {
        path: path.to_path_buf(),
        source,
    })
}

/// The ledger and the backup go once the settings are back.
fn drop_files(paths: &Paths, provider: &str) -> Result<(), SettingsError> {
    remove_file_if_there(&ledger_path(paths, provider))?;
    remove_file_if_there(&backup_path(paths, provider))?;
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
