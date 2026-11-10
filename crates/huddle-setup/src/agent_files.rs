//! Agent files for the Claude model families: one managed file per family
//! in the `agents` folder under the Claude config directory.

use std::collections::BTreeMap;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use huddle_config::paths::Paths;

/// The marker line that says a file is huddle's to manage.
const MANAGED_BY: &[u8] = b"managed-by: huddle";

/// A temp file older than this is leftover work and gets removed.
const STALE_TEMP_AGE: Duration = Duration::from_secs(60);

/// The newest id of each Claude family, order fable, opus, sonnet, haiku.
pub const CLAUDE_MODELS: &[(&str, &str)] = &[
    ("fable", "claude-fable-5-1"),
    ("opus", "claude-opus-5-5"),
    ("sonnet", "claude-sonnet-5-5"),
    ("haiku", "claude-haiku-5-5"),
];

/// What working with the agent files can refuse.
#[derive(Debug)]
pub enum AgentFilesError {
    Io {
        path: PathBuf,
        source: std::io::Error,
    },
}

impl std::fmt::Display for AgentFilesError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            AgentFilesError::Io { path, source } => write!(f, "{}: {}", path.display(), source),
        }
    }
}

impl std::error::Error for AgentFilesError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            AgentFilesError::Io { source, .. } => Some(source),
        }
    }
}

/// Writes the wanted files and removes a managed file no model wants. Skips a file the user owns.
pub fn sync(paths: &Paths) -> Result<(), AgentFilesError> {
    let dir = paths.claude_dir.join("agents");
    ensure_dir(&dir)?;

    let mut wanted = BTreeMap::new();
    for (_family, id) in CLAUDE_MODELS {
        let (family, ver) = model_parts(id);
        let stem = format!("{family}-{ver}");
        let description = format!("Claude {} {ver}", capitalize(&family));
        let content = file_content(&stem, &description, id);
        wanted.insert(format!("{stem}.md"), content);
    }

    sweep_stale_temps(&dir)?;
    for (name, content) in &wanted {
        write_wanted(&dir, name, content)?;
    }
    for (name, path) in managed_files(&dir)? {
        if wanted.contains_key(&name) {
            continue;
        }
        delete_file(&path)?;
    }
    Ok(())
}

/// Removes every managed file. Leaves the folder and user files.
pub fn remove_managed(paths: &Paths) -> Result<(), AgentFilesError> {
    let dir = paths.claude_dir.join("agents");
    match std::fs::metadata(&dir) {
        Ok(_) => {}
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(source) => return Err(AgentFilesError::Io { path: dir, source }),
    }
    for (_name, path) in managed_files(&dir)? {
        delete_file(&path)?;
    }
    Ok(())
}

fn ensure_dir(dir: &Path) -> Result<(), AgentFilesError> {
    std::fs::create_dir_all(dir).map_err(|source| AgentFilesError::Io {
        path: dir.to_path_buf(),
        source,
    })
}

fn dir_entries(dir: &Path) -> Result<Vec<(String, PathBuf)>, AgentFilesError> {
    let mut named = Vec::new();
    let entries = std::fs::read_dir(dir).map_err(|source| AgentFilesError::Io {
        path: dir.to_path_buf(),
        source,
    })?;
    for entry in entries {
        let entry = match entry {
            Ok(entry) => entry,
            Err(_) => continue,
        };
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        named.push((name.to_string(), dir.join(name)));
    }
    Ok(named)
}

/// The managed `.md` files in the directory. Unreadable entries are skipped.
fn managed_files(dir: &Path) -> Result<Vec<(String, PathBuf)>, AgentFilesError> {
    let mut managed = Vec::new();
    for (name, path) in dir_entries(dir)? {
        if !name.ends_with(".md") {
            continue;
        }
        if let Ok(bytes) = std::fs::read(&path)
            && is_managed(&bytes)
        {
            managed.push((name, path));
        }
    }
    Ok(managed)
}

fn write_wanted(dir: &Path, name: &str, content: &str) -> Result<(), AgentFilesError> {
    let path = dir.join(name);
    let wanted = content.as_bytes();
    match std::fs::read(&path) {
        Ok(existing) if existing == wanted => Ok(()),
        Ok(existing) if existing.is_empty() || is_managed(&existing) => {
            write_atomic(dir, name, content)
        }
        Ok(_) => Ok(()),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => write_atomic(dir, name, content),
        Err(_) => Ok(()),
    }
}

fn write_atomic(dir: &Path, name: &str, content: &str) -> Result<(), AgentFilesError> {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_nanos())
        .unwrap_or(0);
    let temp = dir.join(format!("{name}.tmp-{}-{nanos}", std::process::id()));
    let mut file = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o644)
        .open(&temp)
        .map_err(|source| AgentFilesError::Io {
            path: temp.clone(),
            source,
        })?;
    if let Err(source) = file.write_all(content.as_bytes()) {
        return Err(AgentFilesError::Io { path: temp, source });
    }
    drop(file);
    let path = dir.join(name);
    std::fs::rename(&temp, &path).map_err(|source| AgentFilesError::Io { path, source })
}

fn sweep_stale_temps(dir: &Path) -> Result<(), AgentFilesError> {
    for (name, path) in dir_entries(dir)? {
        if !name.contains(".md.tmp-") {
            continue;
        }
        let stale = std::fs::metadata(&path)
            .ok()
            .and_then(|meta| meta.modified().ok())
            .and_then(|modified| modified.elapsed().ok())
            .is_some_and(|age| age > STALE_TEMP_AGE);
        if stale {
            delete_file(&path)?;
        }
    }
    Ok(())
}

fn delete_file(path: &Path) -> Result<(), AgentFilesError> {
    std::fs::remove_file(path).map_err(|source| AgentFilesError::Io {
        path: path.to_path_buf(),
        source,
    })
}

fn is_managed(bytes: &[u8]) -> bool {
    bytes
        .split(|byte| *byte == b'\n')
        .any(|line| line.strip_suffix(b"\r").unwrap_or(line) == MANAGED_BY)
}

/// The family and the dotted version of a model id: `claude-opus-5-5` gives
/// `("opus", "5.5")`.
fn model_parts(id: &str) -> (String, String) {
    let rest = id.strip_prefix("claude-").unwrap_or(id);
    match rest.split_once('-') {
        Some((family, tail)) => (family.to_string(), tail.replace('-', ".")),
        None => (rest.to_string(), String::new()),
    }
}

fn capitalize(word: &str) -> String {
    let mut chars = word.chars();
    match chars.next() {
        Some(first) => first.to_ascii_uppercase().to_string() + chars.as_str(),
        None => String::new(),
    }
}

fn file_content(stem: &str, description: &str, id: &str) -> String {
    format!(
        "---\nname: {stem}\ndescription: {description}\nmodel: {id}\nmanaged-by: huddle\n---\n\nDo the task in your prompt and report what you did.\n"
    )
}
