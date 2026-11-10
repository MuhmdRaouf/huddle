//! huddle-jobs: store.

use crate::id::repo_slug;
use crate::model::Job;
use huddle_config::atomic::write_atomic;
use huddle_config::paths::Paths;
use huddle_procs::lock::{LockError, wait_lock};
use std::fmt;
use std::fs::{self, DirBuilder};
use std::io::ErrorKind;
use std::os::unix::fs::DirBuilderExt;
use std::path::{Path, PathBuf};
use std::time::Duration;

/// The job record file.
const RECORD: &str = "job.json";
/// The write lock beside the record.
const RECORD_LOCK: &str = "job.json.lock";
/// Record writes hold the lock only for read-compare-rename; waiting longer means something is
/// wrong.
const LOCK_TIMEOUT: Duration = Duration::from_secs(10);

/// Finished jobs kept per repository; the rest are pruned.
pub const KEEP_TERMINAL_JOBS: usize = 50;

/// The one way each store call can fail.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum StoreError {
    NotFound {
        id: String,
    },
    Ambiguous {
        prefix: String,
        matches: Vec<String>,
    },
    VersionConflict {
        id: String,
        expected: u64,
        actual: u64,
    },
    Io {
        message: String,
    },
}

impl fmt::Display for StoreError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            StoreError::NotFound { id } => {
                write!(f, "no job matches \"{id}\" (/huddle:status lists them)")
            }
            StoreError::Ambiguous { prefix, matches } => write!(
                f,
                "job id \"{prefix}\" matches {}: use a longer job id",
                matches.join(", ")
            ),
            StoreError::VersionConflict { id, .. } => {
                write!(f, "job {id} changed meanwhile; run the command again")
            }
            StoreError::Io { message } => write!(f, "state store: {message}"),
        }
    }
}

impl std::error::Error for StoreError {}

pub struct JobStore {
    root: PathBuf,
}

impl JobStore {
    /// root = <paths.jobs_root>/<repo_slug(repo_root)>.
    pub fn for_repo(paths: &Paths, repo_root: &Path) -> JobStore {
        JobStore {
            root: paths.jobs_root.join(repo_slug(repo_root)),
        }
    }

    /// The store a job dir lives in, and that job's id (for the detached driver).
    pub fn for_job_dir(job_dir: &Path) -> Option<(JobStore, String)> {
        let root = job_dir.parent()?.to_path_buf();
        let id = job_dir.file_name()?.to_string_lossy().into_owned();
        Some((JobStore { root }, id))
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    /// <root>/<id>
    pub fn job_dir(&self, id: &str) -> PathBuf {
        self.root.join(id)
    }

    /// <root>/<id>/worktree
    pub fn worktree_path(&self, id: &str) -> PathBuf {
        self.job_dir(id).join("worktree")
    }

    /// job.version must be 0; stores version 1. A repeat of an id is Io.
    pub fn create(&self, job: &Job) -> Result<Job, StoreError> {
        if !is_safe_id(&job.id) {
            return Err(StoreError::NotFound { id: job.id.clone() });
        }
        if job.version != 0 {
            return Err(StoreError::Io {
                message: format!("job {} is not new: version {}", job.id, job.version),
            });
        }
        mkdir_private(&self.root)?;
        let dir = self.job_dir(&job.id);
        mkdir_private(&dir)?;
        if dir.join(RECORD).exists() {
            return Err(StoreError::Io {
                message: format!("job {} already exists", job.id),
            });
        }
        let mut stored = job.clone();
        stored.version = 1;
        self.write_record(&stored)?;
        Ok(stored)
    }

    /// The stored job; a missing record is NotFound, unreadable or malformed is Io.
    pub fn get(&self, id: &str) -> Result<Job, StoreError> {
        if !is_safe_id(id) {
            return Err(StoreError::NotFound { id: id.to_string() });
        }
        self.read_record(id)
    }

    /// Exact id, else the one job whose id starts with the text, else NotFound; two or more
    /// matches is Ambiguous, names sorted.
    pub fn find(&self, id_or_prefix: &str) -> Result<Job, StoreError> {
        if !is_safe_id(id_or_prefix) {
            return Err(StoreError::NotFound {
                id: id_or_prefix.to_string(),
            });
        }
        match self.get(id_or_prefix) {
            Ok(job) => return Ok(job),
            Err(StoreError::NotFound { .. }) => {}
            Err(other) => return Err(other),
        }
        let matches: Vec<String> = self
            .job_ids()?
            .into_iter()
            .filter(|id| id.starts_with(id_or_prefix))
            .collect();
        match matches.len() {
            0 => Err(StoreError::NotFound {
                id: id_or_prefix.to_string(),
            }),
            1 => self.get(&matches[0]),
            _ => Err(StoreError::Ambiguous {
                prefix: id_or_prefix.to_string(),
                matches,
            }),
        }
    }

    /// Writes only when the stored version equals job.version; returns the job with version + 1.
    pub fn update(&self, job: &Job) -> Result<Job, StoreError> {
        if !is_safe_id(&job.id) {
            return Err(StoreError::NotFound { id: job.id.clone() });
        }
        let dir = self.job_dir(&job.id);
        let _lock = wait_lock(&dir.join(RECORD_LOCK), LOCK_TIMEOUT).map_err(lock_error)?;
        let current = self.read_record(&job.id)?;
        if current.version != job.version {
            return Err(StoreError::VersionConflict {
                id: job.id.clone(),
                expected: job.version,
                actual: current.version,
            });
        }
        let mut stored = job.clone();
        stored.version = job.version + 1;
        self.write_record(&stored)?;
        Ok(stored)
    }

    /// Newest first: created_at descending, then id descending. Unreadable or corrupt entries
    /// are skipped.
    pub fn list(&self) -> Result<Vec<Job>, StoreError> {
        let mut jobs = Vec::new();
        for id in self.job_ids()? {
            if let Ok(job) = self.read_record(&id) {
                jobs.push(job);
            }
        }
        jobs.sort_by(|a, b| {
            b.created_at
                .cmp(&a.created_at)
                .then_with(|| b.id.cmp(&a.id))
        });
        Ok(jobs)
    }

    /// Removes the job dirs of the oldest terminal jobs beyond `keep`, skipping any whose
    /// worktree still exists. Returns removed ids.
    pub fn prune(&self, keep: usize) -> Result<Vec<String>, StoreError> {
        let mut terminal: Vec<Job> = self
            .list()?
            .into_iter()
            .filter(|job| job.state.is_terminal())
            .collect();
        terminal.sort_by(|a, b| {
            a.created_at
                .cmp(&b.created_at)
                .then_with(|| a.id.cmp(&b.id))
        });
        let mut removed = Vec::new();
        for job in terminal.iter().take(terminal.len().saturating_sub(keep)) {
            if !job.worktree.is_empty() && Path::new(&job.worktree).exists() {
                continue;
            }
            let dir = self.job_dir(&job.id);
            fs::remove_dir_all(&dir).map_err(|err| StoreError::Io {
                message: format!("{}: {}", dir.display(), err),
            })?;
            removed.push(job.id.clone());
        }
        Ok(removed)
    }

    /// One job's record; a missing file is NotFound, any other read or parse failure is Io with
    /// its message.
    fn read_record(&self, id: &str) -> Result<Job, StoreError> {
        let path = self.job_dir(id).join(RECORD);
        let text = match fs::read_to_string(&path) {
            Ok(text) => text,
            Err(err) if err.kind() == ErrorKind::NotFound => {
                return Err(StoreError::NotFound { id: id.to_string() });
            }
            Err(err) => {
                return Err(StoreError::Io {
                    message: format!("{}: {}", path.display(), err),
                });
            }
        };
        serde_json::from_str(&text).map_err(|err| StoreError::Io {
            message: format!("{}: {}", path.display(), err),
        })
    }

    /// job.json: two-space JSON plus a newline, 0600, atomic.
    fn write_record(&self, job: &Job) -> Result<(), StoreError> {
        let mut text = serde_json::to_string_pretty(job).map_err(|err| StoreError::Io {
            message: err.to_string(),
        })?;
        text.push('\n');
        write_atomic(&self.job_dir(&job.id).join(RECORD), text.as_bytes(), 0o600).map_err(|err| {
            StoreError::Io {
                message: err.to_string(),
            }
        })
    }

    /// Every directory name under the root fit to be a job id, sorted. A missing root has no
    /// jobs.
    fn job_ids(&self) -> Result<Vec<String>, StoreError> {
        let entries = match fs::read_dir(&self.root) {
            Ok(entries) => entries,
            Err(err) if err.kind() == ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => {
                return Err(StoreError::Io {
                    message: format!("{}: {}", self.root.display(), err),
                });
            }
        };
        let mut ids = Vec::new();
        for entry in entries {
            let entry = entry.map_err(|err| StoreError::Io {
                message: err.to_string(),
            })?;
            let name = entry.file_name().to_string_lossy().into_owned();
            if is_safe_id(&name) {
                ids.push(name);
            }
        }
        ids.sort();
        Ok(ids)
    }
}

/// Ids become directory names: first char alphanumeric, then only name-safe characters
/// (`^[A-Za-z0-9][A-Za-z0-9._-]*$`).
fn is_safe_id(id: &str) -> bool {
    let mut chars = id.chars();
    match chars.next() {
        Some(first) if first.is_ascii_alphanumeric() => {}
        _ => return false,
    }
    chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-'))
}

/// Creates the directory with mode 0700; an existing directory is fine.
fn mkdir_private(path: &Path) -> Result<(), StoreError> {
    DirBuilder::new()
        .mode(0o700)
        .recursive(true)
        .create(path)
        .map_err(|err| StoreError::Io {
            message: format!("{}: {}", path.display(), err),
        })
}

/// A lock held past the deadline is Io, like any other filesystem failure.
fn lock_error(err: LockError) -> StoreError {
    let message = match err {
        LockError::Held(_) => format!("{RECORD_LOCK} still held after 10s"),
        LockError::Io { path, source } => format!("{}: {}", path.display(), source),
    };
    StoreError::Io { message }
}
