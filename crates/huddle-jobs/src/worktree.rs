//! huddle-jobs: worktree.

use crate::git::{self, GitError};
use std::path::Path;

/// The job's own branch name: "huddle/<id>".
pub fn job_branch(id: &str) -> String {
    format!("huddle/{id}")
}

/// Checks out a new branch at `base_sha` in a linked worktree at `path`.
pub fn add_write(repo: &Path, path: &Path, branch: &str, base_sha: &str) -> Result<(), GitError> {
    git::require_top(repo)?;
    let place = path.to_string_lossy().into_owned();
    git::run(
        repo,
        &[
            "worktree", "add", "--quiet", "-b", branch, "--", &place, base_sha,
        ],
    )
    .map(|_| ())
}

/// Checks `sha` out at `path` with no branch. A prune first: a stale registration with the
/// directory already gone must not fail the add.
pub fn add_detached(repo: &Path, path: &Path, sha: &str) -> Result<(), GitError> {
    git::require_top(repo)?;
    git::run(repo, &["worktree", "prune"]).map(|_| ())?;
    let place = path.to_string_lossy().into_owned();
    git::run(
        repo,
        &["worktree", "add", "--quiet", "--detach", "--", &place, sha],
    )
    .map(|_| ())
}

/// Removes a linked worktree and, when named, its branch. When the remove fails and the path is
/// already gone, only a stale registration is left and a prune settles it; when the path
/// remains, the failure is real. A second call is a no-op success.
pub fn remove(repo: &Path, path: &Path, branch: Option<&str>) -> Result<(), GitError> {
    let place = path.to_string_lossy().into_owned();
    if let Err(error) = git::run(
        repo,
        &["worktree", "remove", "--force", "--force", "--", &place],
    ) {
        if path.exists() {
            return Err(error);
        }
        git::run(repo, &["worktree", "prune"]).map(|_| ())?;
    }
    if let Some(branch) = branch {
        let reference = format!("refs/heads/{branch}");
        if git::run(repo, &["rev-parse", "--verify", "--quiet", &reference]).is_ok() {
            git::run(repo, &["branch", "-D", "--", branch]).map(|_| ())?;
        }
    }
    Ok(())
}
