//! huddle-jobs: overview.

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
    Some(JobRow {
        id: job.id.clone(),
        harness: job.harness.clone(),
        repo: repo_name(&job.repo_root),
        title: job.title.clone(),
        state,
        started_ms: parse_iso_ms(started).unwrap_or(0),
    })
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
