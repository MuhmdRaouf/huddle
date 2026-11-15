//! Read-only git queries: the worktrees of each project, their uncommitted counts and last
//! commit time. Blocking; callers run it on a blocking thread.

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::process::Command;

use crate::model::{Worktree, WorktreeKind};
use crate::repo::Repos;

const AGENT_DIR: &str = "/.agents/worktrees/";

/// The git spawns, handed over so tests count the calls with a fake.
type RunGit<'a> = &'a mut dyn FnMut(&Path, &[&str]) -> Option<String>;

/// Every worktree of the repo around each cwd, once each. A cwd outside git adds nothing.
pub fn worktrees(cwds: &[PathBuf]) -> Vec<Worktree> {
    worktrees_with(cwds, &mut |dir, args| git(dir, args))
}

/// `worktrees` with the git spawns handed over: one `worktree list` per repository,
/// however many cwds work in it.
fn worktrees_with(cwds: &[PathBuf], run: RunGit<'_>) -> Vec<Worktree> {
    let mut rows: Vec<Worktree> = Vec::new();
    let mut repos = Repos::default();
    let mut listed: HashSet<String> = HashSet::new();
    for cwd in cwds {
        let Some(repo) = repos.of(&cwd.to_string_lossy()) else {
            continue;
        };
        if !listed.insert(repo.key.clone()) {
            continue;
        }
        let Some(listing) = run(cwd, &["worktree", "list", "--porcelain"]) else {
            continue;
        };
        let entries = parse_listing(&listing);
        let Some(main) = entries.first() else {
            continue;
        };
        let project = file_name(&main.path);
        let repo_path = main.path.clone();
        for entry in &entries {
            if rows.iter().any(|row| row.path == entry.path) {
                continue;
            }
            rows.push(describe(entry, &project, &repo_path, run));
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

fn describe(entry: &Entry, project: &str, repo: &str, run: RunGit<'_>) -> Worktree {
    let path = Path::new(&entry.path);
    let kind = if entry.path.contains(AGENT_DIR) {
        WorktreeKind::Agent
    } else {
        WorktreeKind::Checkout
    };
    let changes = run(path, &["status", "--porcelain"])
        .map(|out| u32::try_from(out.lines().count()).unwrap_or(u32::MAX));
    let last_commit_ms = run(path, &["log", "-1", "--format=%ct"])
        .and_then(|out| out.trim().parse::<u64>().ok())
        .map(|seconds| seconds * 1_000);
    Worktree {
        path: entry.path.clone(),
        project: project.to_string(),
        repo: repo.to_string(),
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
    use std::fs;

    use super::*;
    use huddle_testkit::temp_home::TempHome;

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

    /// A git fake that answers `worktree list` the way real git does and counts the calls.
    struct CountingGit {
        lists: u32,
    }

    impl CountingGit {
        fn run(&mut self, dir: &Path, args: &[&str]) -> Option<String> {
            match args {
                ["worktree", "list", "--porcelain"] => {
                    self.lists += 1;
                    let shown = dir.canonicalize().ok()?;
                    Some(format!(
                        "worktree {}\nHEAD 172ae7b4f00d\nbranch refs/heads/main\n\n",
                        shown.display()
                    ))
                }
                ["status", "--porcelain"] => Some(String::new()),
                ["log", "-1", "--format=%ct"] => Some("1700000000\n".to_string()),
                _ => None,
            }
        }
    }

    #[test]
    fn l29_thirty_sessions_in_one_repo_run_one_worktree_list() {
        let home = TempHome::new();
        let repo = home.make_repo().expect("repo");
        let mut cwds = vec![repo.clone()];
        for n in 0..29 {
            let dir = repo.join(format!("pkg{n}"));
            fs::create_dir(&dir).expect("session folder");
            cwds.push(dir);
        }

        let mut git = CountingGit { lists: 0 };
        let rows = worktrees_with(&cwds, &mut |dir, args| git.run(dir, args));
        assert_eq!(git.lists, 1, "{} cwds in one repo", cwds.len());
        assert_eq!(rows.len(), 1, "one repo: {rows:?}");

        // A second repo joins the same batch: still one list per repository.
        let other_home = TempHome::new();
        let other = other_home.make_repo().expect("second repo");
        cwds.push(other.clone());
        let nested = other.join("pkg");
        fs::create_dir(&nested).expect("second repo folder");
        cwds.push(nested);

        let mut git = CountingGit { lists: 0 };
        let rows = worktrees_with(&cwds, &mut |dir, args| git.run(dir, args));
        assert_eq!(git.lists, 2, "{} cwds in two repos", cwds.len());
        assert_eq!(rows.len(), 2, "two repos: {rows:?}");
        let repo_key = repo.canonicalize().expect("canonical repo");
        let other_key = other.canonicalize().expect("canonical second repo");
        assert_eq!(rows[0].repo, repo_key.to_string_lossy());
        assert_eq!(rows[1].repo, other_key.to_string_lossy());
    }
}
