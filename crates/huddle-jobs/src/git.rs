//! huddle-jobs: git.

use std::fmt;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{SystemTime, UNIX_EPOCH};

/// Every way a git step can fail. Expected failures are values; the caller decides.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum GitError {
    NotARepo { path: String },
    BadRef { reference: String },
    Conflict { paths: Vec<String> },
    Dirty { paths: Vec<String> },
    WrongBranch { expected: String, actual: String },
    Failed { command: String, stderr: String },
}

impl fmt::Display for GitError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            GitError::NotARepo { path } => write!(f, "not a git repository: {path}"),
            GitError::BadRef { reference } => write!(f, "unknown git ref: {reference}"),
            GitError::Conflict { paths } => write!(f, "conflict in {}", paths.join(", ")),
            GitError::Dirty { paths } => {
                write!(f, "uncommitted changes in the way: {}", paths.join(", "))
            }
            GitError::WrongBranch { expected, actual } => write!(
                f,
                "the job was cut from {expected}, but {actual} is checked out: switch back, then accept"
            ),
            GitError::Failed { command, stderr } => write!(f, "{command}: {stderr}"),
        }
    }
}

impl std::error::Error for GitError {}

fn not_a_repo(path: &Path) -> GitError {
    GitError::NotARepo {
        path: path.to_string_lossy().into_owned(),
    }
}

/// One git run's outcome: ok with stdout, or the exit code (none when git never started) and stderr.
pub struct GitOutput {
    pub ok: bool,
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
}

/// Location variables would point git at another repository, index or work tree than the cwd
/// passed explicitly (set, for example, when running inside a git hook).
const LOCATION_VARS: [&str; 8] = [
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_INDEX_FILE",
    "GIT_COMMON_DIR",
    "GIT_OBJECT_DIRECTORY",
    "GIT_ALTERNATE_OBJECT_DIRECTORIES",
    "GIT_NAMESPACE",
    "GIT_PREFIX",
];

/// A git command for `cwd` with the sanitized environment. Public so tests can inspect it.
pub fn git_command(cwd: &Path, args: &[&str], extra_env: &[(&str, &str)]) -> Command {
    let mut command = Command::new("git");
    command.arg("-c").arg("core.quotepath=off").args(args);
    command.current_dir(cwd);
    for name in LOCATION_VARS {
        command.env_remove(name);
    }
    // English messages (classification reads stderr), never a prompt, never an editor.
    command.env("LC_ALL", "C");
    command.env("LANGUAGE", "");
    command.env("GIT_TERMINAL_PROMPT", "0");
    command.env("GIT_EDITOR", "true");
    for (name, value) in extra_env {
        command.env(name, value);
    }
    command
}

/// Builds the error for a run that exited non-zero. "Commit with nothing to commit" explains
/// itself on stdout only, so stderr falls back to stdout.
fn failure(out: &GitOutput, cwd: &Path, args: &[&str]) -> GitError {
    if out
        .stderr
        .to_ascii_lowercase()
        .contains("not a git repository")
    {
        return not_a_repo(cwd);
    }
    let detail = if out.stderr.is_empty() {
        out.stdout.clone()
    } else {
        out.stderr.clone()
    };
    GitError::Failed {
        command: format!("git {}", args.join(" ")),
        stderr: detail.trim().to_string(),
    }
}

/// Runs git; stdin (when given) is written then closed. A process that cannot start is NotARepo
/// when cwd does not exist, else Failed { command: "git <args>", stderr: error text }.
pub fn run_raw(
    cwd: &Path,
    args: &[&str],
    stdin: Option<&str>,
    extra_env: &[(&str, &str)],
) -> Result<GitOutput, GitError> {
    let mut command = git_command(cwd, args, extra_env);
    command.stdout(Stdio::piped()).stderr(Stdio::piped());
    command.stdin(if stdin.is_some() {
        Stdio::piped()
    } else {
        Stdio::null()
    });
    let mut child = match command.spawn() {
        Ok(child) => child,
        Err(error) => {
            if !cwd.exists() {
                return Err(not_a_repo(cwd));
            }
            return Err(GitError::Failed {
                command: format!("git {}", args.join(" ")),
                stderr: error.to_string(),
            });
        }
    };
    // Dropping the pipe closes stdin: git never waits on it.
    if let (Some(message), Some(mut pipe)) = (stdin, child.stdin.take())
        && let Err(error) = pipe.write_all(message.as_bytes())
    {
        return Err(GitError::Failed {
            command: format!("git {}", args.join(" ")),
            stderr: error.to_string(),
        });
    }
    let output = child.wait_with_output().map_err(|error| GitError::Failed {
        command: format!("git {}", args.join(" ")),
        stderr: error.to_string(),
    })?;
    Ok(GitOutput {
        ok: output.status.success(),
        code: output.status.code(),
        stdout: String::from_utf8_lossy(&output.stdout).into_owned(),
        stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
    })
}

/// run_raw, but a non-zero exit is an Err: NotARepo when stderr contains "not a git repository"
/// (ASCII case-insensitive), else Failed with command "git <args>" and stderr (stdout when stderr
/// is empty).
pub fn run(cwd: &Path, args: &[&str]) -> Result<String, GitError> {
    let out = run_raw(cwd, args, None, &[])?;
    if out.ok {
        Ok(out.stdout)
    } else {
        Err(failure(&out, cwd, args))
    }
}

fn run_void(cwd: &Path, args: &[&str]) -> Result<(), GitError> {
    run(cwd, args).map(|_| ())
}

/// The repository's top level from anywhere inside it; any failure is not a repository.
pub fn root(cwd: &Path) -> Result<PathBuf, GitError> {
    match run(cwd, &["rev-parse", "--show-toplevel"]) {
        Ok(top) => Ok(PathBuf::from(top.trim())),
        Err(_) => Err(not_a_repo(cwd)),
    }
}

/// The reference's commit sha; anything but a missing repository is an unknown ref.
pub fn resolve(repo: &Path, reference: &str) -> Result<String, GitError> {
    let sha = reference.to_owned() + "^{commit}";
    let args = ["rev-parse", "--verify", "--quiet", "--end-of-options", &sha];
    match run(repo, &args) {
        Ok(sha) => Ok(sha.trim().to_string()),
        Err(GitError::NotARepo { .. }) => Err(not_a_repo(repo)),
        Err(_) => Err(GitError::BadRef {
            reference: reference.to_string(),
        }),
    }
}

/// The sha HEAD points at.
pub fn head_sha(dir: &Path) -> Result<String, GitError> {
    let sha = run(dir, &["rev-parse", "--verify", "HEAD"])?;
    Ok(sha.trim().to_string())
}

/// The branch checked out in repo; None when HEAD is detached.
pub fn current_branch(repo: &Path) -> Result<Option<String>, GitError> {
    let args = ["symbolic-ref", "--quiet", "--short", "HEAD"];
    let out = run_raw(repo, &args, None, &[])?;
    if out.ok {
        return Ok(Some(out.stdout.trim().to_string()));
    }
    if out.code == Some(1) {
        return Ok(None);
    }
    Err(failure(&out, repo, &args))
}

/// Refuses dir unless it is the top of its own repository. Git walks up from a folder with no
/// repository of its own to the repository around it, so a write there would land in that other
/// repository. A mismatch and a failed lookup both answer NotARepo.
pub fn require_top(dir: &Path) -> Result<(), GitError> {
    let top = run(dir, &["rev-parse", "--show-toplevel"]).map_err(|_| not_a_repo(dir))?;
    let real_top = Path::new(top.trim())
        .canonicalize()
        .map_err(|_| not_a_repo(dir))?;
    let real_dir = dir.canonicalize().map_err(|_| not_a_repo(dir))?;
    if real_top != real_dir {
        return Err(not_a_repo(dir));
    }
    Ok(())
}

/// Which tracked paths changed against `base`, and which paths are untracked. Each list sorted.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ChangeSet {
    pub added: Vec<String>,
    pub modified: Vec<String>,
    pub deleted: Vec<String>,
    pub untracked: Vec<String>,
}

/// `ls-files --others --exclude-standard -z` for untracked, `diff --name-status -z --no-renames
/// --end-of-options <base> --` for the rest. Each list sorted. Status A goes to added, M to
/// modified, D to deleted.
pub fn changes(dir: &Path, base: &str) -> Result<ChangeSet, GitError> {
    // ls-files first: outside a repository `git diff` silently falls back to --no-index and fails
    // confusingly.
    let untracked = run(dir, &["ls-files", "--others", "--exclude-standard", "-z"])?;
    let tracked = run(
        dir,
        &[
            "diff",
            "--name-status",
            "-z",
            "--no-renames",
            "--end-of-options",
            base,
            "--",
        ],
    )?;
    let mut set = ChangeSet::default();
    for record in split_nul(&tracked).chunks(2) {
        let (Some(status), Some(path)) = (record.first(), record.get(1)) else {
            continue;
        };
        match status.as_str() {
            "A" => set.added.push(path.clone()),
            "D" => set.deleted.push(path.clone()),
            // Without rename detection every other status (M, T, U) edits a path on both sides.
            _ => set.modified.push(path.clone()),
        }
    }
    set.added.sort();
    set.modified.sort();
    set.deleted.sort();
    set.untracked = split_nul(&untracked);
    set.untracked.sort();
    Ok(set)
}

/// A diff against the working state, or its per-file stat.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DiffMode {
    Stat,
    Patch,
}

/// Fails like any git run when an index snapshot step fails.
fn snapshot_failed(step: &str, error: impl fmt::Display) -> GitError {
    GitError::Failed {
        command: step.to_string(),
        stderr: error.to_string(),
    }
}

/// The tree of dir's working state as `add -A` sees it (tracked + untracked, minus ignored),
/// staged in a private copy of the index. The copy lives next to the real index, its mtime kept
/// so a racily clean entry is still re-read; no readable index starts from an empty one. The
/// real index is never touched.
fn snapshot_tree(dir: &Path) -> Result<String, GitError> {
    let located = run(dir, &["rev-parse", "--git-path", "index"])?;
    let located = located.trim();
    let index = if Path::new(located).is_absolute() {
        PathBuf::from(located)
    } else {
        dir.join(located)
    };
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_nanos())
        .unwrap_or_default();
    let copy = index.parent().unwrap_or(Path::new("")).join(format!(
        "index.huddle-{}-{}",
        std::process::id(),
        nanos
    ));
    if index.exists() {
        let mtime = fs::metadata(&index)
            .and_then(|meta| meta.modified())
            .map_err(|error| snapshot_failed("snapshot index", error))?;
        fs::copy(&index, &copy).map_err(|error| snapshot_failed("snapshot index", error))?;
        fs::File::options()
            .write(true)
            .open(&copy)
            .and_then(|file| file.set_modified(mtime))
            .map_err(|error| snapshot_failed("snapshot index", error))?;
    }
    let copy_name = copy.to_string_lossy().into_owned();
    let extra = [("GIT_INDEX_FILE", copy_name.as_str())];
    let staged = || -> Result<String, GitError> {
        let args = ["add", "-A"];
        let out = run_raw(dir, &args, None, &extra)?;
        if !out.ok {
            return Err(failure(&out, dir, &args));
        }
        let args = ["write-tree"];
        let out = run_raw(dir, &args, None, &extra)?;
        if !out.ok {
            return Err(failure(&out, dir, &args));
        }
        Ok(out.stdout.trim().to_string())
    };
    let tree = staged();
    let _ = fs::remove_file(&copy);
    tree
}

/// Diff of the working state against `base`: a tree snapshot built in a private copy of the
/// index, then `diff --no-color --no-ext-diff [--stat=200] --end-of-options <base> <tree> --`.
/// Untracked files appear. The real index is never touched.
pub fn diff(dir: &Path, base: &str, mode: DiffMode) -> Result<String, GitError> {
    let tree = snapshot_tree(dir)?;
    let mut args = vec!["diff", "--no-color", "--no-ext-diff"];
    if mode == DiffMode::Stat {
        args.push("--stat=200");
    }
    args.extend(["--end-of-options", base, tree.as_str(), "--"]);
    run(dir, &args)
}

/// Paths from `status --porcelain=v1 -z --untracked-files=all --no-renames` that are dirty or
/// untracked, sorted, de-duplicated. Each record is `XY path`.
pub fn dirty_paths(repo: &Path) -> Result<Vec<String>, GitError> {
    let status = run(
        repo,
        &[
            "status",
            "--porcelain=v1",
            "-z",
            "--untracked-files=all",
            "--no-renames",
        ],
    )?;
    let mut paths: Vec<String> = Vec::new();
    for record in split_nul(&status) {
        let bytes = record.as_bytes();
        if bytes.len() < 4 || (bytes[0] == b' ' && bytes[1] == b' ') {
            continue;
        }
        paths.push(record[3..].to_string());
    }
    paths.sort();
    paths.dedup();
    Ok(paths)
}

/// `add -A`, then `commit --quiet --cleanup=whitespace --file=-` with the message on stdin. Hooks
/// run. Returns HEAD sha.
pub fn commit_all(dir: &Path, message: &str) -> Result<String, GitError> {
    run_void(dir, &["add", "-A"])?;
    // --cleanup=whitespace keeps `#` lines (markdown headings) in the message.
    let commit = ["commit", "--quiet", "--cleanup=whitespace", "--file=-"];
    let out = run_raw(dir, &commit, Some(message), &[])?;
    if !out.ok {
        return Err(failure(&out, dir, &commit));
    }
    head_sha(dir)
}

/// NUL-split text, dropping one trailing terminator; empty text is an empty list.
pub fn split_nul(text: &str) -> Vec<String> {
    let mut records: Vec<String> = text.split('\0').map(str::to_string).collect();
    if records.last().is_some_and(String::is_empty) {
        records.pop();
    }
    records
}
