//! huddle-jobs: accept.

use crate::checks::{self, Checks};
use crate::git::{self, GitError};
use crate::id::{iso_ms, now_ms};
use crate::model::{Decision, Job, JobState, wrong_state_text};
use crate::store::{JobStore, StoreError};
use crate::verify;
use crate::worktree;
use std::fmt;
use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

/// How an accept ended.
#[derive(Debug, PartialEq, Eq)]
pub struct AcceptOutcome {
    pub job: Job,
    /// The landed commit, or None when the job changed nothing.
    pub commit: Option<String>,
    /// "huddle job <id> is accepted, but its worktree could not be removed: <git error>", when
    /// that happened.
    pub cleanup_failed: Option<String>,
}

/// The one way an accept can fail.
#[derive(Debug, PartialEq, Eq)]
pub enum AcceptError {
    WrongState { id: String, state: JobState },
    Store(StoreError),
    Git(GitError),
}

impl fmt::Display for AcceptError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            AcceptError::WrongState { id, state } => {
                write!(
                    f,
                    "{}",
                    wrong_state_text(id, *state, &[JobState::AwaitingReview])
                )
            }
            AcceptError::Store(error) => write!(f, "{error}"),
            AcceptError::Git(error) => write!(f, "{error}"),
        }
    }
}

impl std::error::Error for AcceptError {}

impl From<StoreError> for AcceptError {
    fn from(error: StoreError) -> Self {
        AcceptError::Store(error)
    }
}

impl From<GitError> for AcceptError {
    fn from(error: GitError) -> Self {
        AcceptError::Git(error)
    }
}

/// How one landing round ended.
enum Round {
    /// Nothing to land, or the commit is on the repository's branch.
    Done(Option<String>),
    /// The tip moved under the round: it runs again.
    Moved,
}

/// How a fast-forward ended.
enum FastForward {
    Landed,
    Moved,
}

/// The branch tip moving between the rebase and the merge is rare; a third try is not worth
/// waiting for.
const MAX_ROUNDS: u32 = 3;

/// Lands a job awaiting review: the worktree's change is committed on the job branch, rebased
/// onto the repository's tip when it moved, verified (the job's setup and gates run again on
/// the exact commit, in a fresh detached checkout), and fast-forwarded onto the repository's
/// branch; then the decision is recorded and the worktree removed. `id` is a full id or a
/// unique prefix.
pub fn accept(store: &JobStore, id: &str) -> Result<AcceptOutcome, AcceptError> {
    let mut job = store.find(id)?;
    if job.state != JobState::AwaitingReview {
        return Err(AcceptError::WrongState {
            id: job.id.clone(),
            state: job.state,
        });
    }
    refuse_moved_branch(&job)?;
    let checks = checks::read_checks(&store.job_dir(&job.id)).map_err(|error| {
        AcceptError::Git(GitError::Failed {
            command: "read checks".into(),
            stderr: error.to_string(),
        })
    })?;
    commit_pending(&job)?;
    let commit = land_rounds(store, &job, &checks)?;
    let now = iso_ms(now_ms());
    job.state = JobState::Accepted;
    job.decision = Some(Decision {
        kind: JobState::Accepted,
        at: now.clone(),
        commit: commit.clone(),
    });
    job.updated_at = now;
    let job = store.update(&job)?;
    let cleanup_failed = cleanup(&job);
    Ok(AcceptOutcome {
        job,
        commit,
        cleanup_failed,
    })
}

/// As accept, but the change goes to the repository's working tree, uncommitted. commit is None.
pub fn accept_no_commit(store: &JobStore, id: &str) -> Result<AcceptOutcome, AcceptError> {
    let mut job = store.find(id)?;
    if job.state != JobState::AwaitingReview {
        return Err(AcceptError::WrongState {
            id: job.id.clone(),
            state: job.state,
        });
    }
    refuse_moved_branch(&job)?;
    apply_to_working_tree(&job)?;
    let now = iso_ms(now_ms());
    job.state = JobState::Accepted;
    job.decision = Some(Decision {
        kind: JobState::Accepted,
        at: now.clone(),
        commit: None,
    });
    job.updated_at = now;
    let job = store.update(&job)?;
    let cleanup_failed = cleanup(&job);
    Ok(AcceptOutcome {
        job,
        commit: None,
        cleanup_failed,
    })
}

/// Applies the worktree's change set against the job's base to the repository's working tree,
/// uncommitted. The 3-way merge runs in a private copy of the repository's index, so a conflict
/// leaves the repository untouched; the merged files are written with `checkout-index` from that
/// copy, so the real index (what the user has staged) never changes. A file the user has changed
/// or created in the repository refuses the change: nothing is applied. Nothing to apply: the
/// repository stays as it is.
fn apply_to_working_tree(job: &Job) -> Result<(), AcceptError> {
    let worktree = Path::new(&job.worktree);
    let repo = Path::new(&job.repo_root);
    let tree = snapshot_tree(worktree)?;
    let named = git::run(
        worktree,
        &[
            "diff",
            "--name-status",
            "-z",
            "--no-renames",
            "--end-of-options",
            &job.base_sha,
            &tree,
            "--",
        ],
    )?;
    let mut written: Vec<String> = Vec::new();
    let mut deleted: Vec<String> = Vec::new();
    for record in git::split_nul(&named).chunks(2) {
        let (Some(status), Some(path)) = (record.first(), record.get(1)) else {
            continue;
        };
        if status == "D" {
            deleted.push(path.clone());
        } else {
            written.push(path.clone());
        }
    }
    written.sort();
    deleted.sort();
    if written.is_empty() && deleted.is_empty() {
        return Ok(());
    }
    let dirty = git::dirty_paths(repo)?;
    let mut overlap: Vec<String> = written
        .iter()
        .chain(deleted.iter())
        .filter(|path| dirty.binary_search(*path).is_ok())
        .cloned()
        .collect();
    overlap.sort();
    if !overlap.is_empty() {
        return Err(AcceptError::Git(GitError::Dirty { paths: overlap }));
    }
    let patch = git::run(
        worktree,
        &[
            "diff",
            "--binary",
            "--full-index",
            "--no-color",
            "--no-ext-diff",
            "--no-renames",
            "--end-of-options",
            &job.base_sha,
            &tree,
            "--",
        ],
    )?;
    let index = private_index(repo)?;
    let index_name = index.to_string_lossy().into_owned();
    let env = [("GIT_INDEX_FILE", index_name.as_str())];
    let merged = || -> Result<(), AcceptError> {
        let args = ["apply", "--cached", "--3way", "--whitespace=nowarn"];
        let out = git::run_raw(repo, &args, Some(&patch), &env)?;
        if !out.ok {
            // A conflict leaves stage entries in the private index; their paths say what could
            // not merge. Anything else is a failed apply.
            let unmerged = git::run_raw(repo, &["ls-files", "--unmerged", "-z"], None, &env)?;
            let mut paths: Vec<String> = Vec::new();
            if unmerged.ok {
                for record in git::split_nul(&unmerged.stdout) {
                    if let Some((_, path)) = record.split_once('\t') {
                        paths.push(path.to_string());
                    }
                }
                paths.sort();
                paths.dedup();
            }
            if !paths.is_empty() {
                return Err(AcceptError::Git(GitError::Conflict { paths }));
            }
            return Err(AcceptError::Git(git_failed(&args, &out)));
        }
        // The merged files come out of the private index; the repository's own index never sees
        // them.
        if !written.is_empty() {
            let args = ["checkout-index", "--force", "-z", "--stdin"];
            let paths = written.join("\0") + "\0";
            let out = git::run_raw(repo, &args, Some(&paths), &env)?;
            if !out.ok {
                return Err(AcceptError::Git(git_failed(&args, &out)));
            }
        }
        Ok(())
    };
    let applied = merged();
    let _ = fs::remove_file(&index);
    applied?;
    for path in &deleted {
        // The file may already be gone; the directories it leaves empty go too.
        let _ = fs::remove_file(repo.join(path));
        remove_empty_parents(repo, path);
    }
    Ok(())
}

/// The tree of `dir`'s working state as `add -A` sees it (tracked + untracked, minus ignored),
/// staged in a private copy of the index. The real index is never touched.
fn snapshot_tree(dir: &Path) -> Result<String, GitError> {
    let index = private_index(dir)?;
    let index_name = index.to_string_lossy().into_owned();
    let env = [("GIT_INDEX_FILE", index_name.as_str())];
    let staged = || -> Result<String, GitError> {
        let args = ["add", "-A"];
        let out = git::run_raw(dir, &args, None, &env)?;
        if !out.ok {
            return Err(git_failed(&args, &out));
        }
        let args = ["write-tree"];
        let out = git::run_raw(dir, &args, None, &env)?;
        if !out.ok {
            return Err(git_failed(&args, &out));
        }
        Ok(out.stdout.trim().to_string())
    };
    let tree = staged();
    let _ = fs::remove_file(&index);
    tree
}

/// A private copy of the index git resolves from `dir`, created next to the real index as
/// `index.huddle-<pid>-<nanos>`. The copy keeps the real index's mtime so a racily clean entry is
/// still re-read; no readable index starts from an empty one.
fn private_index(dir: &Path) -> Result<PathBuf, GitError> {
    let located = git::run(dir, &["rev-parse", "--git-path", "index"])?;
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
            .map_err(|error| index_failed("snapshot index", error))?;
        fs::copy(&index, &copy).map_err(|error| index_failed("snapshot index", error))?;
        fs::File::options()
            .write(true)
            .open(&copy)
            .and_then(|file| file.set_modified(mtime))
            .map_err(|error| index_failed("snapshot index", error))?;
    }
    Ok(copy)
}

/// Fails like any git run when a private index step fails.
fn index_failed(step: &str, error: impl fmt::Display) -> GitError {
    GitError::Failed {
        command: step.to_string(),
        stderr: error.to_string(),
    }
}

/// Removes the directories a deleted path leaves empty, up to but not including the repository
/// root. A directory that still holds something stays.
fn remove_empty_parents(repo: &Path, path: &str) {
    let mut parent = Path::new(path).parent();
    while let Some(dir) = parent {
        if dir.as_os_str().is_empty() {
            break;
        }
        if fs::remove_dir(repo.join(dir)).is_err() {
            break;
        }
        parent = dir.parent();
    }
}

/// Refuses unless the repository's checked-out branch is still the one the job was cut from; a
/// landing on another branch would surprise the user.
fn refuse_moved_branch(job: &Job) -> Result<(), AcceptError> {
    let Some(expected) = job.base_branch.as_deref() else {
        return Ok(());
    };
    let actual = match git::current_branch(Path::new(&job.repo_root))? {
        Some(branch) => branch,
        None => "a detached HEAD".to_string(),
    };
    if actual != expected {
        return Err(AcceptError::Git(GitError::WrongBranch {
            expected: expected.to_string(),
            actual,
        }));
    }
    Ok(())
}

/// Commits whatever the worktree holds that is not yet committed. The message is the task title
/// and, when the last attempt reported one, its summary. Nothing else signs the work.
fn commit_pending(job: &Job) -> Result<(), AcceptError> {
    let dir = Path::new(&job.worktree);
    let changes = git::changes(dir, "HEAD")?;
    if changes.added.is_empty()
        && changes.modified.is_empty()
        && changes.deleted.is_empty()
        && changes.untracked.is_empty()
    {
        return Ok(());
    }
    let mut message = job.title.clone();
    if let Some(summary) = job
        .last_attempt()
        .and_then(|attempt| attempt.report.as_ref())
        .map(|report| report.summary.trim())
        .filter(|summary| !summary.is_empty())
    {
        message.push_str("\n\n");
        message.push_str(summary);
    }
    message.push('\n');
    git::commit_all(dir, &message)?;
    Ok(())
}

/// Lands the job's commit on the repository's branch, re-reading the tip every round; a tip
/// that moved asks for the next round. `verified` holds the sha whose setup and gates passed,
/// so a rebase that changes the head verifies again and an unchanged head does not.
fn land_rounds(
    store: &JobStore,
    job: &Job,
    checks: &Checks,
) -> Result<Option<String>, AcceptError> {
    let mut verified = None;
    for _ in 0..MAX_ROUNDS {
        if let Round::Done(commit) = land_round(store, job, checks, &mut verified)? {
            return Ok(commit);
        }
    }
    Err(AcceptError::Git(GitError::Failed {
        command: "git merge --ff-only".into(),
        stderr: format!("the branch tip moved {MAX_ROUNDS} times while landing; try again"),
    }))
}

/// One round: settle the job branch against the tip, verify the head, then fast-forward.
fn land_round(
    store: &JobStore,
    job: &Job,
    checks: &Checks,
    verified: &mut Option<String>,
) -> Result<Round, AcceptError> {
    let dir = Path::new(&job.worktree);
    let repo = Path::new(&job.repo_root);
    let mut head = git::head_sha(dir)?;
    if head == job.base_sha {
        return Ok(Round::Done(None));
    }
    let tip = git::head_sha(repo)?;
    if head == tip {
        // A previous accept already landed the commit and only failed to record it.
        return Ok(Round::Done(Some(head)));
    }
    if !is_ancestor(dir, &tip, &head)? {
        rebase_onto(dir, &tip)?;
        head = git::head_sha(dir)?;
    }
    if (!checks.setup.is_empty() || !checks.gates.is_empty())
        && verified.as_deref() != Some(head.as_str())
    {
        verify_commit(store, job, checks, &head)?;
        *verified = Some(head.clone());
    }
    match fast_forward(repo, &head, &tip)? {
        FastForward::Landed => Ok(Round::Done(Some(head))),
        FastForward::Moved => Ok(Round::Moved),
    }
}

/// Runs setup and gates on `sha` in `<job dir>/verify`, always removing the checkout afterwards.
fn verify_commit(store: &JobStore, job: &Job, checks: &Checks, sha: &str) -> Result<(), GitError> {
    let repo = Path::new(&job.repo_root);
    let checkout = store.job_dir(&job.id).join("verify");
    let _ = worktree::remove(repo, &checkout, None); // a leftover from a crash
    worktree::add_detached(repo, &checkout, sha)?;
    let env = huddle_harness::env::harness_env(&huddle_config::env::Env::from_process(), &[]);
    let failed = verify::run_commands(checks, &checkout, &env)
        .into_iter()
        .find(|gate| gate.failed());
    let _ = worktree::remove(repo, &checkout, None);
    match failed {
        None => Ok(()),
        Some(gate) => Err(GitError::Failed {
            command: "verification in a fresh checkout".into(),
            stderr: format!(
                "{} failed: nothing was applied\n{}",
                gate.run,
                gate.tail.trim()
            ),
        }),
    }
}

/// Whether `tip` is already inside `head`'s history: exit 0 true, exit 1 false, anything else
/// a failure.
fn is_ancestor(dir: &Path, tip: &str, head: &str) -> Result<bool, AcceptError> {
    let args = ["merge-base", "--is-ancestor", "--end-of-options", tip, head];
    let out = git::run_raw(dir, &args, None, &[])?;
    if out.ok {
        return Ok(true);
    }
    // "Not an ancestor" is git's quiet exit 1; a bad ref or a broken repository exits 128.
    if out.code == Some(1) {
        return Ok(false);
    }
    Err(git_failed(&args, &out).into())
}

/// Replays the job branch onto `tip`. A conflict aborts the rebase — none stays in progress —
/// and names every unmerged path; any other failure carries its own git error.
fn rebase_onto(dir: &Path, tip: &str) -> Result<(), AcceptError> {
    let args = ["rebase", "--quiet", "--end-of-options", tip];
    if let Err(error) = git::run(dir, &args) {
        if !rebase_in_progress(dir)? {
            return Err(error.into());
        }
        let paths = unmerged_paths(dir)?;
        git::run(dir, &["rebase", "--abort"])?;
        return Err(AcceptError::Git(GitError::Conflict { paths }));
    }
    Ok(())
}

/// A stopped rebase leaves `rebase-merge` (merge backend) or `rebase-apply` (apply backend) in
/// the git dir.
fn rebase_in_progress(dir: &Path) -> Result<bool, AcceptError> {
    for name in ["rebase-merge", "rebase-apply"] {
        let located = git::run(dir, &["rev-parse", "--git-path", name])?;
        let located = located.trim();
        let path = if Path::new(located).is_absolute() {
            PathBuf::from(located)
        } else {
            dir.join(located)
        };
        if path.is_dir() {
            return Ok(true);
        }
    }
    Ok(false)
}

/// The paths a stopped rebase could not merge, sorted.
fn unmerged_paths(dir: &Path) -> Result<Vec<String>, AcceptError> {
    let out = git::run(dir, &["diff", "--name-only", "--diff-filter=U", "-z"])?;
    let mut paths = git::split_nul(&out);
    paths.sort();
    Ok(paths)
}

/// Fast-forwards the repository's branch onto `sha`, only while the tip is still the one this
/// round saw. A dirty file the change touches refuses the landing; nothing is written and the
/// job stays as it was.
fn fast_forward(repo: &Path, sha: &str, expected_tip: &str) -> Result<FastForward, AcceptError> {
    if git::head_sha(repo)? != expected_tip {
        return Ok(FastForward::Moved);
    }
    let touched = git::split_nul(&git::run(
        repo,
        &[
            "diff",
            "--name-only",
            "-z",
            "--no-renames",
            "--end-of-options",
            expected_tip,
            sha,
            "--",
        ],
    )?);
    let dirty = git::dirty_paths(repo)?;
    let mut overlap: Vec<String> = touched
        .into_iter()
        .filter(|path| dirty.binary_search(path).is_ok())
        .collect();
    overlap.sort();
    if !overlap.is_empty() {
        return Err(AcceptError::Git(GitError::Dirty { paths: overlap }));
    }
    let args = ["merge", "--quiet", "--ff-only", "--end-of-options", sha];
    let out = git::run_raw(repo, &args, None, &[])?;
    if out.ok {
        return Ok(FastForward::Landed);
    }
    // The merge raced with a commit that moved the tip; anything else is a real failure.
    if git::head_sha(repo)? != expected_tip {
        return Ok(FastForward::Moved);
    }
    Err(git_failed(&args, &out).into())
}

/// The Failed error for a run that exited non-zero; "commit with nothing to commit" explains
/// itself on stdout only, so stderr falls back to stdout.
fn git_failed(args: &[&str], out: &git::GitOutput) -> GitError {
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

/// Removes the worktree and its branch once the decision is stored; a failure is reported, the
/// acceptance stands.
fn cleanup(job: &Job) -> Option<String> {
    let repo = Path::new(&job.repo_root);
    match worktree::remove(repo, Path::new(&job.worktree), job.branch.as_deref()) {
        Ok(()) => None,
        Err(error) => Some(format!(
            "huddle job {} is accepted, but its worktree could not be removed: {error}",
            job.id
        )),
    }
}
