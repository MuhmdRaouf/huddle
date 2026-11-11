//! Claude Code's settings.json: the proxy-env patch and its ledger. `env_file` plans the
//! changes; this module does the file work and records what was added.

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::fs::{self, File};
use std::io::{self, Write as _};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

use huddle_config::paths::Paths;
use serde_json::{Map, Value};

use crate::env_file::{EnvPlan, EnvView, RouteMode, RouterTarget, plan_env};
use crate::ledger_file;

/// The env keys this patch reads; a present one that is not a string refuses the patch.
const ENV_KEYS: [&str; 6] = [
    "HTTPS_PROXY",
    "https_proxy",
    "NODE_EXTRA_CA_CERTS",
    "NO_PROXY",
    "ANTHROPIC_BASE_URL",
    "ENABLE_TOOL_SEARCH",
];

/// Why a settings patch did not happen; nothing is written on any of these.
#[derive(Debug)]
pub enum SettingsError {
    Io {
        path: PathBuf,
        source: std::io::Error,
    },
    Json {
        path: PathBuf,
        message: String,
    },
    Toml {
        path: PathBuf,
        message: String,
    },
    Shape {
        path: PathBuf,
        what: String,
    },
    Changed {
        path: PathBuf,
    },
}

impl std::fmt::Display for SettingsError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            SettingsError::Io { path, source } => write!(f, "{}: {source}", path.display()),
            SettingsError::Json { path, message } => {
                write!(f, "{}: not valid JSON ({message})", path.display())
            }
            SettingsError::Toml { path, message } => {
                write!(f, "{}: not valid TOML ({message})", path.display())
            }
            SettingsError::Shape { path, what } => write!(f, "{}: {what}", path.display()),
            SettingsError::Changed { path } => write!(
                f,
                "{}: changed while huddle edited it; nothing changed",
                path.display()
            ),
        }
    }
}
impl std::error::Error for SettingsError {}

/// What one `apply_proxy` did.
pub struct PatchOutcome {
    pub changed: bool,
    pub mode: RouteMode,
    pub lines: Vec<String>,
}

/// What the patches added, so an undo can take it back out.
#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Ledger {
    pub created_file: bool,
    pub created_env: bool,
    /// key -> value before huddle wrote it; None means the key was absent. TOML has no null,
    /// so the map is stored as a JSON string.
    #[serde(with = "crate::ledger_file::json_string")]
    pub before: BTreeMap<String, Option<String>>,
    pub after_sha256: String,
}

pub fn settings_path(paths: &Paths) -> PathBuf {
    paths.claude_dir.join("settings.json")
}

pub fn backup_path(paths: &Paths) -> PathBuf {
    let mut path = settings_path(paths).into_os_string();
    path.push(".huddle-backup");
    PathBuf::from(path)
}

pub fn ledger_path(paths: &Paths) -> PathBuf {
    paths.state_dir.join("settings-ledger.toml")
}

/// `settings.json.pristine` in the huddle dir: settings.json as the user had it before
/// huddle's first write, whatever wrote the file since.
pub fn pristine_path(paths: &Paths) -> PathBuf {
    paths.state_dir.join("settings.json.pristine")
}

/// Saves the untouched settings.json bytes once, before huddle's first write. A settings.json
/// that does not exist saves nothing.
pub fn save_pristine(paths: &Paths, original: Option<&[u8]>) -> Result<(), SettingsError> {
    if pristine_path(paths).exists() {
        return Ok(());
    }
    let Some(bytes) = original else {
        return Ok(());
    };
    write_atomic(&pristine_path(paths), bytes, 0o600)
}
/// The pristine copy: its bytes and its document.
pub type PristineDoc = (Vec<u8>, Map<String, Value>);

/// The pristine copy's bytes and document, None when there is none.
pub fn pristine_doc(paths: &Paths) -> Result<Option<PristineDoc>, SettingsError> {
    if !pristine_path(paths).exists() {
        return Ok(None);
    }
    let (bytes, doc) = load_doc(&pristine_path(paths))?;
    let Some(bytes) = bytes else {
        return Ok(None);
    };
    Ok(Some((bytes, doc)))
}

/// Whether any ledger still stands: the install ledger or any provider's.
fn any_ledger(paths: &Paths) -> bool {
    if ledger_path(paths).exists() {
        return true;
    }
    let Ok(entries) = fs::read_dir(paths.state_dir.join("providers")) else {
        return false;
    };
    entries.filter_map(Result::ok).any(|entry| {
        entry
            .file_name()
            .to_string_lossy()
            .ends_with("-ledger.toml")
    })
}

/// Deletes the pristine copy once no ledger remains to give it a purpose.
pub fn drop_pristine(paths: &Paths) -> Result<(), SettingsError> {
    if any_ledger(paths) {
        return Ok(());
    }
    remove_file_if_there(&pristine_path(paths))
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

pub fn sha256_hex(bytes: &[u8]) -> String {
    let digest = ring::digest::digest(&ring::digest::SHA256, bytes);
    let mut hex = String::with_capacity(digest.as_ref().len() * 2);
    for byte in digest.as_ref() {
        let _ = write!(hex, "{byte:02x}");
    }
    hex
}

/// One settings.json read: the file's bytes (None when missing) and its top-level object.
type LoadedDoc = (Option<Vec<u8>>, Map<String, Value>);

/// Returns the file's bytes (None when missing) and its top-level object. Anything but a clean
/// read of a valid, right-shaped object refuses.
pub fn load_doc(path: &Path) -> Result<LoadedDoc, SettingsError> {
    let bytes = match fs::read(path) {
        Ok(bytes) => Some(bytes),
        Err(source) if source.kind() == io::ErrorKind::NotFound => None,
        Err(source) => {
            return Err(SettingsError::Io {
                path: path.to_path_buf(),
                source,
            });
        }
    };
    let Some(bytes) = bytes else {
        return Ok((None, Map::new()));
    };
    let value: Value = serde_json::from_slice(&bytes).map_err(|err| SettingsError::Json {
        path: path.to_path_buf(),
        message: err.to_string(),
    })?;
    let Value::Object(doc) = value else {
        return Err(SettingsError::Shape {
            path: path.to_path_buf(),
            what: "top level must be an object".to_string(),
        });
    };
    if let Some(env) = doc.get("env") {
        if !env.is_object() {
            return Err(SettingsError::Shape {
                path: path.to_path_buf(),
                what: "env must be an object; nothing changed".to_string(),
            });
        }
        for key in ENV_KEYS {
            if let Some(value) = env.get(key)
                && !value.is_string()
            {
                return Err(SettingsError::Shape {
                    path: path.to_path_buf(),
                    what: format!("{key} must be a string"),
                });
            }
        }
    }
    Ok((Some(bytes), doc))
}

/// Serializes with 2-space indent and a trailing newline, re-reads the file and refuses with
/// Changed when its bytes differ from `original`, then writes atomically. Mode kept; 0600 for a
/// new file.
pub fn write_doc(
    path: &Path,
    doc: &Map<String, Value>,
    original: Option<&[u8]>,
) -> Result<Vec<u8>, SettingsError> {
    let mut text = serde_json::to_string_pretty(doc).map_err(|err| SettingsError::Json {
        path: path.to_path_buf(),
        message: err.to_string(),
    })?;
    text.push('\n');
    let bytes = text.into_bytes();

    let real = canonical(path);
    let current = match fs::read(&real) {
        Ok(current) => Some(current),
        Err(source) if source.kind() == io::ErrorKind::NotFound => None,
        Err(source) => {
            return Err(SettingsError::Io {
                path: path.to_path_buf(),
                source,
            });
        }
    };
    let unchanged = match (original, current.as_deref()) {
        (None, None) => true,
        (Some(before), Some(now)) => before == now,
        _ => false,
    };
    if !unchanged {
        return Err(SettingsError::Changed {
            path: path.to_path_buf(),
        });
    }
    let mode = match fs::metadata(&real) {
        Ok(meta) => meta.permissions().mode() & 0o777,
        Err(_) => 0o600,
    };
    write_atomic(&real, &bytes, mode)?;
    Ok(bytes)
}

/// Patches the proxy env into Claude Code's settings.json and records the ledger. Idempotent:
/// a run with nothing to change writes nothing. A file that moved under the edit is redone once
/// on the file as it now stands.
pub fn apply_proxy(paths: &Paths, target: &RouterTarget) -> Result<PatchOutcome, SettingsError> {
    match apply_once(paths, target) {
        Err(SettingsError::Changed { .. }) => apply_once(paths, target),
        outcome => outcome,
    }
}

fn apply_once(paths: &Paths, target: &RouterTarget) -> Result<PatchOutcome, SettingsError> {
    let settings = settings_path(paths);
    let (original, mut doc) = load_doc(&settings)?;
    let view = env_view_of(&doc);
    let plan = plan_env(&view, target);
    if plan.set.is_empty() && plan.remove.is_empty() {
        return Ok(PatchOutcome {
            changed: false,
            mode: plan.mode,
            lines: plan.lines,
        });
    }

    let created_file = original.is_none();
    let created_env = doc.get("env").is_none();
    let before = before_values(&doc, &plan);
    save_pristine(paths, original.as_deref())?;

    if created_env {
        doc.insert("env".to_string(), Value::Object(Map::new()));
    }
    let Some(env) = doc.get_mut("env").and_then(Value::as_object_mut) else {
        return Err(SettingsError::Shape {
            path: settings.clone(),
            what: "env must be an object; nothing changed".to_string(),
        });
    };
    for key in &plan.remove {
        env.shift_remove(key);
    }
    for (key, value) in &plan.set {
        env.insert(key.clone(), Value::String(value.clone()));
    }

    let ledger_file = ledger_path(paths);
    let existing = read_ledger(&ledger_file)?;
    let mut ledger = match existing {
        Some(ledger) => ledger,
        None => {
            if let Some(bytes) = original.as_deref() {
                write_atomic(&backup_path(paths), bytes, 0o600)?;
            }
            Ledger {
                created_file,
                created_env,
                before: BTreeMap::new(),
                after_sha256: String::new(),
            }
        }
    };
    for (key, value) in before {
        ledger.before.entry(key).or_insert(value);
    }

    let bytes = write_doc(&settings, &doc, original.as_deref())?;
    ledger.after_sha256 = sha256_hex(&bytes);
    write_ledger(&ledger_file, &ledger)?;
    Ok(PatchOutcome {
        changed: true,
        mode: plan.mode,
        lines: plan.lines,
    })
}

/// The `env` values the plan decides on: the lowercase proxy when it is a non-empty string,
/// else the uppercase one.
fn env_view_of(doc: &Map<String, Value>) -> EnvView {
    let string_of = |key: &str| {
        doc.get("env")
            .and_then(|env| env.get(key))
            .and_then(Value::as_str)
            .map(str::to_string)
    };
    let https_proxy = match string_of("https_proxy") {
        Some(value) if !value.is_empty() => Some(value),
        _ => string_of("HTTPS_PROXY"),
    };
    EnvView {
        https_proxy,
        node_ca: string_of("NODE_EXTRA_CA_CERTS"),
        no_proxy: string_of("NO_PROXY"),
        base_url: string_of("ANTHROPIC_BASE_URL"),
        tool_search: string_of("ENABLE_TOOL_SEARCH"),
    }
}

/// The value of each key this run touches, read before anything is written.
fn before_values(doc: &Map<String, Value>, plan: &EnvPlan) -> Vec<(String, Option<String>)> {
    let env = doc.get("env").and_then(Value::as_object);
    plan.remove
        .iter()
        .chain(plan.set.iter().map(|(key, _)| key))
        .map(|key| {
            let value = env
                .and_then(|env| env.get(key))
                .and_then(Value::as_str)
                .map(str::to_string);
            (key.clone(), value)
        })
        .collect()
}

fn read_ledger(path: &Path) -> Result<Option<Ledger>, SettingsError> {
    match fs::read(path) {
        Ok(bytes) => ledger_file::parse(path, &bytes).map(Some),
        Err(source) if source.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(source) => Err(SettingsError::Io {
            path: path.to_path_buf(),
            source,
        }),
    }
}

fn write_ledger(path: &Path, ledger: &Ledger) -> Result<(), SettingsError> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|source| SettingsError::Io {
            path: parent.to_path_buf(),
            source,
        })?;
    }
    let text = ledger_file::render(path, ledger)?;
    write_atomic(path, text.as_bytes(), 0o600)
}

/// The path with symlinks resolved: a symlinked settings.json is written through its target.
fn canonical(path: &Path) -> PathBuf {
    fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
}

/// Writes `bytes` to `path` with `mode` through a temp file beside it, renamed over the target:
/// a reader sees the old file or the new one. A failure before the rename removes the temp file.
fn write_atomic(path: &Path, bytes: &[u8], mode: u32) -> Result<(), SettingsError> {
    let real = canonical(path);
    if let Some(parent) = real.parent() {
        fs::create_dir_all(parent).map_err(|source| SettingsError::Io {
            path: parent.to_path_buf(),
            source,
        })?;
    }
    let temp = PathBuf::from(format!(
        "{}.huddle-tmp.{}",
        real.display(),
        std::process::id()
    ));
    let land = || -> Result<(), SettingsError> {
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
        fs::rename(&temp, &real).map_err(|source| SettingsError::Io {
            path: real.clone(),
            source,
        })
    };
    if let Err(err) = land() {
        let _ = fs::remove_file(&temp);
        return Err(err);
    }
    Ok(())
}
