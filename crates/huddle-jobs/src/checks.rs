//! The scope and forbid checks over a job's changed paths.

use crate::files;
use crate::git::ChangeSet;
use crate::glob::matches_any;
use crate::model::Access;
use serde::{Deserialize, Serialize};
use std::io;
use std::path::{Path, PathBuf};

/// What a job is asked to prove: the setup and gate commands, and the globs its changed paths
/// must live inside.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Checks {
    pub setup: Vec<String>,
    pub gates: Vec<String>,
    pub scope: Vec<String>,
    pub forbid: Vec<String>,
}

impl Checks {
    pub fn is_empty(&self) -> bool {
        self.setup.is_empty()
            && self.gates.is_empty()
            && self.scope.is_empty()
            && self.forbid.is_empty()
    }
}

/// The one-word verdict of a finished attempt: evidence for the review, never a state change.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Verdict {
    Pass,
    GateFail,
    ScopeViolation,
}

impl Verdict {
    pub fn as_str(self) -> &'static str {
        match self {
            Verdict::Pass => "pass",
            Verdict::GateFail => "gate_fail",
            Verdict::ScopeViolation => "scope_violation",
        }
    }
}

/// One gate command's outcome. A gate killed by a signal has no exit code.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GateResult {
    pub run: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i32>,
    pub timed_out: bool,
    pub duration_ms: u64,
    pub tail: String,
}

impl GateResult {
    pub fn failed(&self) -> bool {
        self.timed_out || self.exit_code != Some(0)
    }
}

/// The scope check's findings: which changed paths sit outside the brief.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScopeResult {
    pub out_of_scope: Vec<String>,
    pub forbidden: Vec<String>,
    pub repo_changed: bool,
}

impl ScopeResult {
    pub fn violated(&self) -> bool {
        self.repo_changed || !self.out_of_scope.is_empty() || !self.forbidden.is_empty()
    }
}

/// What one attempt proved: the verdict, the scope findings, the changed paths, every gate's
/// outcome and the paths the gates touched.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Verification {
    pub verdict: Verdict,
    pub scope: ScopeResult,
    pub changed: Vec<String>,
    pub gates: Vec<GateResult>,
    pub gate_touched: Vec<String>,
}

/// Added, modified, deleted and untracked, sorted and de-duplicated.
pub fn changed_paths(changes: &ChangeSet) -> Vec<String> {
    let mut paths: Vec<String> = changes
        .added
        .iter()
        .chain(&changes.modified)
        .chain(&changes.deleted)
        .chain(&changes.untracked)
        .cloned()
        .collect();
    paths.sort();
    paths.dedup();
    paths
}

/// Write access: a path matching `forbid` is forbidden (and never also out of scope); else, when
/// `scope` is not empty, a path matching no scope glob is out of scope. Readonly and exec: both
/// lists empty, `repo_changed` true when any path changed.
pub fn check_scope(checks: &Checks, access: Access, paths: &[String]) -> ScopeResult {
    if access != Access::Write {
        return ScopeResult {
            repo_changed: !paths.is_empty(),
            ..ScopeResult::default()
        };
    }
    let mut out = ScopeResult::default();
    for path in paths {
        if matches_any(&checks.forbid, path) {
            out.forbidden.push(path.clone());
        } else if !checks.scope.is_empty() && !matches_any(&checks.scope, path) {
            out.out_of_scope.push(path.clone());
        }
    }
    out
}

/// A violated scope beats a failed gate; no failure is a pass.
pub fn derive_verdict(scope: &ScopeResult, gates: &[GateResult]) -> Verdict {
    if scope.violated() {
        Verdict::ScopeViolation
    } else if gates.iter().any(GateResult::failed) {
        Verdict::GateFail
    } else {
        Verdict::Pass
    }
}

pub fn checks_path(job_dir: &Path) -> PathBuf {
    job_dir.join("checks.toml")
}

pub fn verification_path(job_dir: &Path, attempt: u32) -> PathBuf {
    job_dir.join(format!("verification-{attempt}.toml"))
}

/// Missing file: `Checks::default()`. Unreadable or corrupt: Err.
pub fn read_checks(job_dir: &Path) -> io::Result<Checks> {
    Ok(files::read(&checks_path(job_dir))?.unwrap_or_default())
}

pub fn write_checks(job_dir: &Path, checks: &Checks) -> io::Result<()> {
    files::write(&checks_path(job_dir), checks)
}

pub fn write_verification(job_dir: &Path, attempt: u32, v: &Verification) -> io::Result<()> {
    files::write(&verification_path(job_dir, attempt), v)
}

/// Missing file: Ok(None). Corrupt: Err.
pub fn read_verification(job_dir: &Path, attempt: u32) -> io::Result<Option<Verification>> {
    files::read(&verification_path(job_dir, attempt))
}
