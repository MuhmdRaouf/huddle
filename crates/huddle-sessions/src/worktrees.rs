//! Read-only git queries: the worktrees of each project, their uncommitted counts and last
//! commit time. Blocking; callers run it on a blocking thread.

use std::path::{Path, PathBuf};
use std::process::Command;

use crate::model::{Worktree, WorktreeKind};

const AGENT_DIR: &str = "/.agents/worktrees/";

/// Every worktree of the repo around each cwd, once each. A cwd outside git adds nothing.
pub fn worktrees(cwds: &[PathBuf]) -> Vec<Worktree> {
    let mut rows: Vec<Worktree> = Vec::new();
    for cwd in cwds {
        let Some(listing) = git(cwd, &["worktree", "list", "--porcelain"]) else {
            continue;
        };
        let entries = parse_listing(&listing);
        let Some(main) = entries.first() else {
            continue;
        };
        let project = file_name(&main.path);
        for entry in &entries {
            if rows.iter().any(|row| row.path == entry.path) {
                continue;
            }
            rows.push(describe(entry, &project));
        }
    }
    rows
}

#[derive(Debug, PartialEq, Eq)]
struct Entry {
    path: String,
    head: Option<String>,
    branch: Option<String>,
}

/// Parses `git worktree list --porcelain`: blocks of `key value` lines split by blank lines.
/// Unknown keys are skipped, and a bare repository's block is dropped.
fn parse_listing(text: &str) -> Vec<Entry> {
    let mut entries = Vec::new();
    for block in text.split("\n\n") {
        let mut entry = Entry {
            path: String::new(),
            head: None,
            branch: None,
        };
        let mut bare = false;
        for line in block.lines() {
            let (key, value) = line.split_once(' ').unwrap_or((line, ""));
            match key {
                "worktree" => entry.path = value.to_string(),
                "HEAD" => entry.head = Some(value.chars().take(8).collect()),
                "branch" => {
                    entry.branch = Some(
                        value
                            .strip_prefix("refs/heads/")
                            .unwrap_or(value)
                            .to_string(),
                    );
                }
                "bare" => bare = true,
                _ => {}
            }
        }
        if !entry.path.is_empty() && !bare {
            entries.push(entry);
        }
    }
    entries
}

fn describe(entry: &Entry, project: &str) -> Worktree {
    let path = Path::new(&entry.path);
    let kind = if entry.path.contains(AGENT_DIR) {
        WorktreeKind::Agent
    } else {
        WorktreeKind::Checkout
    };
    let changes = git(path, &["status", "--porcelain"])
        .map(|out| u32::try_from(out.lines().count()).unwrap_or(u32::MAX));
    let last_commit_ms = git(path, &["log", "-1", "--format=%ct"])
        .and_then(|out| out.trim().parse::<u64>().ok())
        .map(|seconds| seconds * 1_000);
    Worktree {
        path: entry.path.clone(),
        project: project.to_string(),
        branch: entry.branch.clone(),
        head: entry.head.clone(),
        kind,
        used_by: None,
        changes,
        last_commit_ms,
    }
}

fn git(dir: &Path, args: &[&str]) -> Option<String> {
    let output = Command::new("git")
        .arg("--no-optional-locks")
        .arg("-C")
        .arg(dir)
        .args(args)
        .env("GIT_TERMINAL_PROMPT", "0")
        .output()
        .ok()?;
    output
        .status
        .success()
        .then(|| String::from_utf8_lossy(&output.stdout).into_owned())
}

fn file_name(path: &str) -> String {
    Path::new(path)
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn listing_skips_unknown_keys_bare_blocks_and_detached_heads() {
        let text = "worktree /p/repo\nHEAD 172ae7b4f00d\nbranch refs/heads/main\nfuture-key x\n\n\
                    worktree /p/bare\nbare\n\n\
                    worktree /p/repo/.agents/worktrees/t\nHEAD abcdef0123456789\ndetached\n\n";
        let entries = parse_listing(text);
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[0].branch.as_deref(), Some("main"));
        assert_eq!(entries[0].head.as_deref(), Some("172ae7b4"));
        assert_eq!(
            (entries[1].path.as_str(), entries[1].branch.as_deref()),
            ("/p/repo/.agents/worktrees/t", None)
        );
    }
}
