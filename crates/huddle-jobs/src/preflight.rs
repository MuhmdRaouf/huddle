//! The checks a run passes before a job exists: the harness is known, supports the access and
//! has its binary. The run word and the MCP tool share them.

use std::fmt;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::check::check_entry;
use huddle_harness::user_entries::all_entries;

#[derive(Debug, PartialEq, Eq)]
pub enum PreflightError {
    /// The harness table could not be read.
    Table(String),
    Unknown(String),
    Access {
        harness: String,
        access: String,
        supported: Vec<String>,
    },
    NotReady {
        harness: String,
        detail: String,
    },
}

impl fmt::Display for PreflightError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            PreflightError::Table(error) => write!(f, "{error}"),
            PreflightError::Unknown(harness) => {
                write!(
                    f,
                    "no harness named \"{harness}\": /huddle:setup lists them"
                )
            }
            PreflightError::Access {
                harness,
                access,
                supported,
            } => write!(
                f,
                "{harness} cannot run with {access} access: it supports {}",
                supported.join(", ")
            ),
            PreflightError::NotReady { harness, detail } => write!(
                f,
                "{harness} is not ready: {detail}: /huddle:setup {harness} shows what to fix"
            ),
        }
    }
}

/// Passes when `harness` can run with `access` (`readonly`, `exec` or `write`) here and now.
/// The binary is the only readiness gate: a version probe can fail on a harness that still
/// works a turn, and the attempt then carries the harness's own error.
pub fn check_run(
    paths: &Paths,
    env: &Env,
    harness: &str,
    access: &str,
) -> Result<(), PreflightError> {
    let entries = all_entries(&paths.config_dir.join("harnesses.toml"))
        .map_err(|error| PreflightError::Table(error.to_string()))?;
    let entry = entries
        .iter()
        .find(|entry| entry.id == harness)
        .ok_or_else(|| PreflightError::Unknown(harness.to_string()))?;
    let supported: Vec<String> = entry
        .run
        .accepted_access()
        .iter()
        .map(|level| level.as_str().to_string())
        .collect();
    if !supported.iter().any(|level| level == access) {
        return Err(PreflightError::Access {
            harness: harness.to_string(),
            access: access.to_string(),
            supported,
        });
    }
    let report = check_entry(entry, env, None);
    match report
        .checks
        .iter()
        .find(|line| !line.ok && line.name == "binary")
    {
        Some(line) => Err(PreflightError::NotReady {
            harness: harness.to_string(),
            detail: line.detail.clone(),
        }),
        None => Ok(()),
    }
}
