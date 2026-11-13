mod support;

use huddle_jobs::id::iso_ms;
use huddle_jobs::liveness::{ProcessRecord, driver_record_path, write_record};
use huddle_jobs::model::{Access, Attempt, AttemptKind, JobState};
use huddle_jobs::overview::{JobRow, finished, overview};
use huddle_jobs::store::JobStore;
use std::path::{Path, PathBuf};
use std::process::Command;
use support::{Rig, rig, sample_job};

const NOW: u64 = 1_800_000_000_000;

fn own_record() -> ProcessRecord {
    let pid = std::process::id();
    ProcessRecord {
        pid,
        start_token: huddle_procs::live::start_token(pid).expect("token"),
    }
}

fn dead_record() -> ProcessRecord {
    let mut child = Command::new("/usr/bin/true").spawn().expect("spawn");
    let pid = child.id();
    child.wait().expect("wait");
    ProcessRecord {
        pid,
        start_token: "gone".into(),
    }
}

fn attempt(started_ms: u64) -> Attempt {
    Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "go".into(),
        feedback: None,
        session_id: None,
        started_at: iso_ms(started_ms),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    }
}

/// Stores a job in the repo's store; `driver` writes a driver.toml when given.
fn add(
    r: &Rig,
    repo: &Path,
    id: &str,
    state: JobState,
    title: &str,
    driver: Option<ProcessRecord>,
) -> PathBuf {
    let mut job = sample_job(r, id, Access::Readonly, state);
    job.repo_root = repo.to_string_lossy().into_owned();
    job.title = title.into();
    job.created_at = iso_ms(NOW - 120_000);
    job.updated_at = iso_ms(NOW - 1_000);
    if state != JobState::Queued {
        job.attempts = vec![attempt(NOW - 60_000)];
    }
    let store = JobStore::for_repo(&r.paths, repo);
    store.create(&job).expect("create");
    let dir = store.job_dir(id);
    if let Some(record) = driver {
        write_record(&driver_record_path(&dir), &record).expect("record");
    }
    dir
}

fn snapshot(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    let mut files = Vec::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(dir) = stack.pop() {
        for entry in std::fs::read_dir(&dir).expect("read dir") {
            let path = entry.expect("entry").path();
            if path.is_dir() {
                stack.push(path);
            } else {
                let bytes = std::fs::read(&path).expect("read");
                files.push((path, bytes));
            }
        }
    }
    files.sort();
    files
}

fn row<'a>(rows: &'a [JobRow], id: &str) -> &'a JobRow {
    rows.iter().find(|r| r.id == id).expect("row")
}

#[test]
fn live_jobs_across_repos_are_listed_and_nothing_is_written() {
    let r = rig();
    let other = PathBuf::from("/work/other-repo");
    add(&r, &r.repo, "j-queued", JobState::Queued, "queue me", None);
    add(
        &r,
        &r.repo,
        "j-run",
        JobState::Running,
        "run me",
        Some(own_record()),
    );
    add(
        &r,
        &other,
        "j-dead",
        JobState::Running,
        "dead one",
        Some(dead_record()),
    );
    add(
        &r,
        &other,
        "j-review",
        JobState::AwaitingReview,
        "review me",
        None,
    );
    add(&r, &other, "j-done", JobState::Completed, "done", None);
    let before = snapshot(&r.paths.jobs_root);

    let rows = overview(&r.paths, NOW);

    assert_eq!(rows.len(), 4);
    let repo_name = r.repo.file_name().unwrap().to_string_lossy().into_owned();
    let queued = row(&rows, "j-queued");
    assert_eq!(queued.state, "queued");
    assert_eq!(queued.repo, repo_name);
    assert_eq!(queued.harness, "opencode");
    assert_eq!(queued.title, "queue me");
    assert_eq!(queued.started_ms, NOW - 120_000);
    assert_eq!(row(&rows, "j-run").state, "running");
    assert_eq!(row(&rows, "j-run").started_ms, NOW - 60_000);
    assert_eq!(row(&rows, "j-dead").state, "dead");
    assert_eq!(row(&rows, "j-dead").repo, "other-repo");
    assert_eq!(row(&rows, "j-review").state, "awaiting_review");
    assert_eq!(snapshot(&r.paths.jobs_root), before);
}

#[test]
fn a_missing_jobs_root_is_an_empty_overview() {
    let r = rig();
    assert!(overview(&r.paths, NOW).is_empty());
}

#[test]
fn a_row_serialises_with_the_contract_field_names() {
    let row = JobRow {
        id: "j-1".into(),
        harness: "opencode".into(),
        repo: "huddle".into(),
        title: "t".into(),
        state: "running",
        started_ms: 5,
        ended_ms: None,
        files: None,
        add: None,
        del: None,
        why: None,
    };
    assert_eq!(
        serde_json::to_string(&row).unwrap(),
        r#"{"id":"j-1","harness":"opencode","repo":"huddle","title":"t","state":"running","started_ms":5,"ended_ms":null,"files":null,"add":null,"del":null,"why":null}"#
    );
}

#[test]
fn d4_a_job_row_carries_its_end_time_diff_size_and_why() {
    let r = rig();
    let mut job = sample_job(&r, "j-rev", Access::Write, JobState::AwaitingReview);
    job.created_at = iso_ms(NOW - 120_000);
    job.updated_at = iso_ms(NOW - 1_000);
    let mut ended = attempt(NOW - 60_000);
    ended.ended_at = Some(iso_ms(NOW - 30_000));
    job.attempts = vec![ended];
    job.failure = Some("tests are red".into());
    let tree = r.repo.clone();
    support::write(&tree, "a.txt", "two\n");
    support::write(&tree, "b.txt", "x\ny\n");
    JobStore::for_repo(&r.paths, &r.repo)
        .create(&job)
        .expect("create");

    let rows = overview(&r.paths, NOW);

    let row = row(&rows, "j-rev");
    assert_eq!(row.ended_ms, Some(NOW - 30_000));
    assert_eq!((row.files, row.add, row.del), (Some(2), Some(3), Some(1)));
    assert_eq!(row.why.as_deref(), Some("tests are red"));
}

#[test]
fn d4_a_running_job_has_no_end_time_and_a_dead_one_says_why() {
    let r = rig();
    add(
        &r,
        &r.repo,
        "j-run",
        JobState::Running,
        "run",
        Some(own_record()),
    );
    add(
        &r,
        &r.repo,
        "j-dead",
        JobState::Running,
        "dead",
        Some(dead_record()),
    );

    let rows = overview(&r.paths, NOW);

    assert_eq!(row(&rows, "j-run").ended_ms, None);
    assert_eq!(row(&rows, "j-run").why, None);
    assert_eq!(
        row(&rows, "j-dead").why.as_deref(),
        Some("the driver is gone")
    );
    assert_eq!(
        row(&rows, "j-run").files,
        Some(0),
        "an untouched folder is an empty diff"
    );
}

#[test]
fn d12_finished_jobs_are_listed_inside_the_window() {
    let r = rig();
    add(
        &r,
        &r.repo,
        "j-run",
        JobState::Running,
        "run",
        Some(own_record()),
    );
    add(&r, &r.repo, "j-acc", JobState::Accepted, "accepted", None);
    add(&r, &r.repo, "j-fail", JobState::Failed, "failed", None);
    add(&r, &r.repo, "j-can", JobState::Cancelled, "cancelled", None);
    add(&r, &r.repo, "j-ok", JobState::Completed, "completed", None);
    add(&r, &r.repo, "j-old", JobState::Accepted, "old", None);
    let old = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = old.get("j-old").expect("read");
    job.updated_at = iso_ms(NOW - 40 * 86_400_000);
    job.attempts[0].started_at = iso_ms(NOW - 40 * 86_400_000);
    job.attempts[0].ended_at = Some(iso_ms(NOW - 40 * 86_400_000));
    old.update(&job).expect("update");

    let rows = finished(&r.paths, NOW - 30 * 86_400_000);

    let mut ids: Vec<&str> = rows.iter().map(|r| r.id.as_str()).collect();
    ids.sort();
    assert_eq!(ids, ["j-acc", "j-can", "j-fail", "j-ok"]);
    assert_eq!(row(&rows, "j-acc").state, "accepted");
    assert_eq!(row(&rows, "j-can").state, "cancelled");
    assert_eq!(row(&rows, "j-ok").state, "completed");
}
