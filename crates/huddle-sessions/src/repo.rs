//! Finds the git repository a working directory belongs to by reading `.git`, never by running git.

use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};

/// The repository around a working directory and the checkout the directory sits in.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Repo {
    /// The repository folder's name.
    pub name: String,
    /// The repository folder's path: the project's key.
    pub key: String,
    /// The root of the checkout holding the directory; the key itself for a main checkout.
    pub worktree: String,
}

/// Resolves each working directory once.
#[derive(Default)]
pub(crate) struct Repos {
    found: HashMap<String, Repo>,
}

impl Repos {
    pub fn of(&mut self, cwd: &str) -> Option<Repo> {
        if let Some(repo) = self.found.get(cwd) {
            return Some(repo.clone());
        }
        let repo = resolve(Path::new(cwd))?;
        self.found.insert(cwd.to_string(), repo.clone());
        Some(repo)
    }
}

/// A folder without a `.git` above it is its own project.
fn resolve(cwd: &Path) -> Option<Repo> {
    let cwd = cwd.canonicalize().unwrap_or_else(|_| cwd.to_path_buf());
    let checkout = cwd
        .ancestors()
        .find(|dir| dir.join(".git").exists())
        .unwrap_or(&cwd);
    let root = main_folder(checkout).unwrap_or_else(|| checkout.to_path_buf());
    Some(Repo {
        name: root.file_name()?.to_string_lossy().into_owned(),
        key: root.to_str()?.to_string(),
        worktree: checkout.to_str()?.to_string(),
    })
}

/// The main worktree's folder when `checkout` is a linked worktree: its `.git` file names
/// `<common>/worktrees/<name>`, whose `commondir` file points back at `<common>`.
fn main_folder(checkout: &Path) -> Option<PathBuf> {
    let text = fs::read_to_string(checkout.join(".git")).ok()?;
    let gitdir = text.trim().strip_prefix("gitdir:")?.trim();
    let gitdir = checkout.join(gitdir);
    let common = fs::read_to_string(gitdir.join("commondir")).ok()?;
    let common = gitdir.join(common.trim()).canonicalize().ok()?;
    if common.file_name()? == ".git" {
        Some(common.parent()?.to_path_buf())
    } else {
        Some(common)
    }
}
