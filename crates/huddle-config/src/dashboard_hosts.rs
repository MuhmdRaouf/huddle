//! The dashboard hosts in `config.toml`: the names besides the loopback that may reach the
//! dashboard's read-only paths. The commands rewrite the whole file, sorted and deduplicated.

use std::fmt;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use serde::Deserialize;

use crate::atomic::write_atomic;
use crate::paths::Paths;

const MANAGED_LINE: &str =
    "# Managed by huddle: huddle add-dashboard-host / remove-dashboard-host rewrite this file.";

/// What an add or a remove did, with the normalized host.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Change {
    Added(String),
    Removed(String),
    Unchanged(String),
}

/// What reading or writing the hosts can refuse.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DashboardHostsError {
    /// The value is not a bare host name.
    BadHost { host: String },
    /// Any read error other than a missing file: `<file>: <message>`.
    Read { file: String, message: String },
    /// Malformed file, or a host in it that is not a bare name: `<file>: <detail>`.
    Invalid { file: String, detail: String },
    /// Writing the file failed: `<file>: <message>`.
    Write { file: String, message: String },
}

impl fmt::Display for DashboardHostsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            DashboardHostsError::BadHost { host } => write!(
                f,
                "huddle: {host:?} is not a bare host name (letters, digits, dots and hyphens only)"
            ),
            DashboardHostsError::Read { file, message }
            | DashboardHostsError::Write { file, message } => write!(f, "{file}: {message}"),
            DashboardHostsError::Invalid { file, detail } => {
                write!(f, "{file}: not valid config ({detail})")
            }
        }
    }
}

impl std::error::Error for DashboardHostsError {}

#[derive(Deserialize, Default)]
struct ConfigFile {
    #[serde(default)]
    dashboard: DashboardSection,
}

#[derive(Deserialize, Default)]
struct DashboardSection {
    #[serde(default)]
    hosts: Vec<String>,
}

/// `<config_dir>/config.toml`.
pub fn config_path(paths: &Paths) -> PathBuf {
    paths.config_dir.join("config.toml")
}

/// The lowercase form of a bare host name: letters, digits, dots and hyphens, no empty label.
/// A scheme, path, port, wildcard, space or empty value is refused.
pub fn normalize(raw: &str) -> Result<String, DashboardHostsError> {
    let host = raw.to_ascii_lowercase();
    let chars_ok = host
        .bytes()
        .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'.' || b == b'-');
    let edges_ok = !host.starts_with(['.', '-']) && !host.ends_with(['.', '-']);
    if host.is_empty() || !chars_ok || !edges_ok || host.contains("..") {
        return Err(DashboardHostsError::BadHost {
            host: raw.to_string(),
        });
    }
    Ok(host)
}

/// The listed hosts, sorted and deduplicated. A missing file lists none.
pub fn read(path: &Path) -> Result<Vec<String>, DashboardHostsError> {
    let file = path.display().to_string();
    let text = match fs::read_to_string(path) {
        Ok(text) => text,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(err) => {
            return Err(DashboardHostsError::Read {
                file,
                message: err.to_string(),
            });
        }
    };
    let parsed: ConfigFile = toml::from_str(&text).map_err(|err| DashboardHostsError::Invalid {
        file: file.clone(),
        detail: err.message().to_string(),
    })?;
    let mut hosts = Vec::new();
    for raw in &parsed.dashboard.hosts {
        let host = normalize(raw).map_err(|_| DashboardHostsError::Invalid {
            file: file.clone(),
            detail: format!("{raw:?} is not a bare host name"),
        })?;
        hosts.push(host);
    }
    hosts.sort();
    hosts.dedup();
    Ok(hosts)
}

/// Lists `raw`; a host already listed leaves the file as it is.
pub fn add(path: &Path, raw: &str) -> Result<Change, DashboardHostsError> {
    let host = normalize(raw)?;
    let mut hosts = read(path)?;
    if hosts.contains(&host) {
        return Ok(Change::Unchanged(host));
    }
    hosts.push(host.clone());
    hosts.sort();
    write(path, &hosts)?;
    Ok(Change::Added(host))
}

/// Unlists `raw`; a host that is not listed leaves the file as it is.
pub fn remove(path: &Path, raw: &str) -> Result<Change, DashboardHostsError> {
    let host = normalize(raw)?;
    let mut hosts = read(path)?;
    if !hosts.contains(&host) {
        return Ok(Change::Unchanged(host));
    }
    hosts.retain(|listed| *listed != host);
    write(path, &hosts)?;
    Ok(Change::Removed(host))
}

/// The hosts hold only `[a-z0-9.-]`, so they need no TOML escaping.
fn write(path: &Path, hosts: &[String]) -> Result<(), DashboardHostsError> {
    let quoted: Vec<String> = hosts.iter().map(|host| format!("\"{host}\"")).collect();
    let text = format!(
        "{MANAGED_LINE}\n[dashboard]\nhosts = [{}]\n",
        quoted.join(", ")
    );
    write_atomic(path, text.as_bytes(), 0o644).map_err(|err| DashboardHostsError::Write {
        file: path.display().to_string(),
        message: err.to_string(),
    })
}
