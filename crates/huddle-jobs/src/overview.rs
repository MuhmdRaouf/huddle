//! huddle-jobs: overview.

use crate::git::{self, DiffMode};
use crate::id::parse_iso_ms;
use crate::liveness::{driver_record_path, job_alive, read_record};
use crate::model::{Job, JobState};
use crate::store::JobStore;
use huddle_config::paths::Paths;
use huddle_procs::live::process_alive;
use serde::Serialize;
use std::path::Path;

/// One live job as the dashboard shows it. `state` is a job state's snake_case name, or
/// `dead` for an active job whose driver is gone.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct JobRow {
    pub id: String,
    pub harness: String,
    pub repo: String,
    pub title: String,
    pub state: &'static str,
    pub started_ms: u64,
    /// When the latest attempt ended; `None` while it runs.
    pub ended_ms: Option<u64>,
    /// Files, added lines and removed lines of the job's folder against the job's base; `None`
    /// when the job has no readable diff.
    pub files: Option<u32>,
    pub add: Option<u64>,
    pub del: Option<u64>,
    /// One line on why the job is stuck or failed.
    pub why: Option<String>,
}

/// Queued, running, dead and awaiting-review jobs of every repo under `paths.jobs_root`,
/// newest first within a repo. Reads only: a dead driver is reported, never swept.
pub fn overview(paths: &Paths, now_ms: u64) -> Vec<JobRow> {
    let Ok(entries) = std::fs::read_dir(&paths.jobs_root) else {
        return Vec::new();
    };
    let mut repo_dirs: Vec<_> = entries
        .filter_map(Result::ok)
        .map(|entry| entry.path())
        .filter(|path| path.is_dir())
        .collect();
    repo_dirs.sort();
    repo_dirs
        .iter()
        .map(|dir| JobStore::at_root(dir.clone()))
        .flat_map(|store| rows_of(&store, now_ms))
        .collect()
}

fn rows_of(store: &JobStore, now_ms: u64) -> Vec<JobRow> {
    let Ok(jobs) = store.list() else {
        return Vec::new();
    };
    jobs.iter()
        .filter_map(|job| row_of(store, job, now_ms))
        .collect()
}

fn row_of(store: &JobStore, job: &Job, now_ms: u64) -> Option<JobRow> {
    let state = match job.state {
        JobState::Queued | JobState::Running if !driver_alive(store, job, now_ms) => "dead",
        JobState::Queued | JobState::Running | JobState::AwaitingReview => job.state.as_str(),
        _ => return None,
    };
    let started = job
        .attempts
        .last()
        .map_or(job.created_at.as_str(), |a| a.started_at.as_str());
    let (files, add, del) = match job.state {
        JobState::Running | JobState::AwaitingReview => diff_size(job),
        _ => (None, None, None),
    };
    let why = job
        .failure
        .clone()
        .or_else(|| (state == "dead").then(|| "the driver is gone".to_string()));
    Some(JobRow {
        id: job.id.clone(),
        harness: job.harness.clone(),
        repo: repo_name(&job.repo_root),
        title: job.title.clone(),
        state,
        started_ms: parse_iso_ms(started).unwrap_or(0),
        ended_ms: job
            .attempts
            .last()
            .and_then(|attempt| attempt.ended_at.as_deref())
            .and_then(parse_iso_ms),
        files,
        add,
        del,
        why,
    })
}

/// The folder's change size against the base, from the diff's stat summary.
fn diff_size(job: &Job) -> (Option<u32>, Option<u64>, Option<u64>) {
    let stat = git::diff(Path::new(&job.dir), &job.base_sha, DiffMode::Stat);
    match stat.ok().as_deref().and_then(parse_summary) {
        Some((files, add, del)) => (Some(files), Some(add), Some(del)),
        None => (None, None, None),
    }
}

/// Reads ` 2 files changed, 3 insertions(+), 1 deletion(-)`. An empty diff is `(0, 0, 0)`.
fn parse_summary(stat: &str) -> Option<(u32, u64, u64)> {
    let Some(line) = stat.lines().last() else {
        return Some((0, 0, 0));
    };
    if !line.contains("changed") {
        return None;
    }
    let count_before = |word: &str| {
        line.split(',')
            .find(|part| part.contains(word))
            .and_then(|part| part.split_whitespace().next())
            .and_then(|n| n.parse::<u64>().ok())
            .unwrap_or(0)
    };
    Some((
        u32::try_from(count_before("file")).unwrap_or(u32::MAX),
        count_before("insertion"),
        count_before("deletion"),
    ))
}

/// The sweep's liveness check without its writes.
fn driver_alive(store: &JobStore, job: &Job, now_ms: u64) -> bool {
    let record = read_record(&driver_record_path(&store.job_dir(&job.id)));
    let record_alive = record
        .as_ref()
        .is_some_and(|r| process_alive(r.pid, &r.start_token));
    let updated_at_ms = parse_iso_ms(&job.updated_at).unwrap_or(0);
    job_alive(
        job.state,
        record_alive,
        record.is_some(),
        updated_at_ms,
        now_ms,
    )
}

fn repo_name(repo_root: &str) -> String {
    Path::new(repo_root)
        .file_name()
        .map_or_else(String::new, |name| name.to_string_lossy().into_owned())
}

#[cfg(test)]
mod tests {
    use super::parse_summary;

    #[test]
    fn the_summary_line_gives_files_added_and_removed() {
        let stat =
            " a.txt | 2 +-\n b.txt | 2 ++\n 2 files changed, 3 insertions(+), 1 deletion(-)\n";
        assert_eq!(parse_summary(stat), Some((2, 3, 1)));
        assert_eq!(
            parse_summary(" 1 file changed, 4 deletions(-)\n"),
            Some((1, 0, 4))
        );
        assert_eq!(parse_summary(""), Some((0, 0, 0)));
        assert_eq!(parse_summary("fatal: nope"), None);
    }
}
