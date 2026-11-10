//! User harness entries: any other CLI, loaded from `harnesses.json` in the huddle home.
//!
//! Every user entry is a text-wire, write-only harness: stdout is the report, there is no session
//! to resume, and the access map holds only `write`, so `readonly` and `exec` are refused.

use std::collections::{BTreeMap, HashSet};
use std::fs;
use std::path::Path;

use serde::Deserialize;

use crate::entries::builtin;
use crate::table::{
    Access, AccessSpec, Binary, Caps, Check, Entry, Launcher, Prompt, ReportKind, Run, Session,
    SessionOwner, Transport, Wire,
};

/// One mebibyte: the default stdout line cap.
const MIB: u64 = 1_048_576;

/// Why user entries could not be loaded.
#[derive(Clone, Debug, PartialEq)]
pub enum UserEntriesError {
    Read { path: String, message: String },
    Parse { path: String, message: String },
    BadId { id: String },
    ClashesWithBuiltin { id: String },
    Duplicate { id: String },
}

impl std::fmt::Display for UserEntriesError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            UserEntriesError::Read { path, message } => write!(f, "cannot read {path}: {message}"),
            UserEntriesError::Parse { path, message } => {
                write!(f, "{path} is not valid: {message}")
            }
            UserEntriesError::BadId { id } => write!(
                f,
                "harness id {id} must be lowercase letters, digits and dashes, \
                 starting with a letter or digit"
            ),
            UserEntriesError::ClashesWithBuiltin { id } => {
                write!(f, "harness id {id} is a builtin; pick another")
            }
            UserEntriesError::Duplicate { id } => write!(f, "harness id {id} appears twice"),
        }
    }
}

impl std::error::Error for UserEntriesError {}

/// The file's top level: `{"harnesses": [...]}`. A missing `harnesses` member means no entries.
#[derive(Deserialize)]
struct RawFile {
    #[serde(default)]
    harnesses: Vec<RawEntry>,
}

/// One user row. Unknown members are ignored.
#[derive(Deserialize)]
struct RawEntry {
    id: String,
    #[serde(default)]
    label: Option<String>,
    command: String,
    #[serde(default)]
    args: Vec<String>,
    #[serde(default)]
    prompt: Option<String>,
    #[serde(default)]
    prompt_flag: Option<String>,
    #[serde(default)]
    version_args: Option<Vec<String>>,
}

/// Loads the user entries. A missing file is no entries, not an error.
pub fn load(path: &Path) -> Result<Vec<Entry>, UserEntriesError> {
    let shown = path.display().to_string();
    let text = match fs::read_to_string(path) {
        Ok(text) => text,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(err) => {
            return Err(UserEntriesError::Read {
                path: shown,
                message: err.to_string(),
            });
        }
    };
    let file: RawFile = serde_json::from_str(&text).map_err(|err| UserEntriesError::Parse {
        path: shown.clone(),
        message: err.to_string(),
    })?;
    let table = builtin();
    let mut entries = Vec::with_capacity(file.harnesses.len());
    let mut seen: HashSet<String> = HashSet::new();
    for raw in file.harnesses {
        if raw.command.is_empty() {
            return Err(UserEntriesError::Parse {
                path: shown,
                message: "command is empty".to_string(),
            });
        }
        let transport = match raw.prompt.as_deref() {
            None | Some("argv") => Transport::Argv,
            Some("stdin") => Transport::Stdin,
            Some(word) => {
                return Err(UserEntriesError::Parse {
                    path: shown,
                    message: format!(r#"prompt must be "argv" or "stdin", not "{word}""#),
                });
            }
        };
        if !is_good_id(&raw.id) {
            return Err(UserEntriesError::BadId { id: raw.id });
        }
        if table.iter().any(|entry| entry.id == raw.id) {
            return Err(UserEntriesError::ClashesWithBuiltin { id: raw.id });
        }
        if !seen.insert(raw.id.clone()) {
            return Err(UserEntriesError::Duplicate { id: raw.id });
        }
        entries.push(to_entry(raw, transport));
    }
    Ok(entries)
}

/// The builtin entries, then the user entries.
pub fn all_entries(path: &Path) -> Result<Vec<Entry>, UserEntriesError> {
    let mut all = builtin();
    all.extend(load(path)?);
    Ok(all)
}

/// Lowercase ASCII letters, digits and dashes, starting with a letter or digit.
fn is_good_id(id: &str) -> bool {
    let mut chars = id.chars();
    matches!(chars.next(), Some(first) if first.is_ascii_lowercase() || first.is_ascii_digit())
        && chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
}

/// One user row as a table entry.
fn to_entry(raw: RawEntry, transport: Transport) -> Entry {
    let label = raw.label.unwrap_or_else(|| raw.id.clone());
    let install_hint = format!(
        "Install {label} and make sure `{}` is on PATH.",
        raw.command
    );
    let login_hint = format!("Run `{}` once and log in.", raw.command);
    Entry {
        id: raw.id,
        label,
        binary: Binary {
            names: vec![raw.command],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: Check {
            version: raw
                .version_args
                .unwrap_or_else(|| vec!["--version".to_string()]),
            install_hint,
            login_hint,
            verified: false,
        },
        run: Run {
            args: raw.args,
            cwd_flag: None,
            prompt: Prompt {
                transport,
                flag: raw.prompt_flag,
                argv_text: None,
            },
            effort: None,
            access: BTreeMap::from([(
                Access::Write,
                AccessSpec {
                    args: vec![],
                    env: vec![],
                },
            )]),
            after_access: vec![],
            session: Session {
                owner: SessionOwner::None,
                new: vec![],
                resume: vec![],
                missing: vec![],
            },
            line_cap: MIB,
            reap_group: true,
        },
        wire: Wire::Text,
        caps: Caps {
            report: ReportKind::FreeText,
            notes: vec!["Report: free text. Resume: no. Access: write only.".to_string()],
        },
    }
}
