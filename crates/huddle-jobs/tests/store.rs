mod support;

use huddle_jobs::id::{iso_ms, new_job_id, new_session_id, parse_iso_ms, repo_slug};
use huddle_jobs::model::*;
use huddle_jobs::store::{JobStore, StoreError};
use std::os::unix::fs::PermissionsExt;
use support::{rig, sample_job};

fn mode(path: &std::path::Path) -> u32 {
    std::fs::metadata(path).unwrap().permissions().mode() & 0o777
}

#[test]
fn job_ids_are_a_utc_date_and_six_crockford_chars() {
    let id = new_job_id(1_700_000_000_000).unwrap();
    assert_eq!(id.len(), 13, "{id}");
    assert!(id.starts_with("231114-"), "{id}");
    assert!(
        id[7..]
            .chars()
            .all(|c| "0123456789abcdefghjkmnpqrstvwxyz".contains(c)),
        "{id}"
    );
    assert_ne!(new_job_id(0).unwrap(), new_job_id(0).unwrap());
}

#[test]
fn session_ids_are_uuid_v4() {
    let id = new_session_id().unwrap();
    assert_eq!(id.len(), 36);
    let bytes = id.as_bytes();
    assert_eq!(bytes[14] as char, '4');
    assert!("89ab".contains(bytes[19] as char), "{id}");
    assert_eq!(bytes[8], b'-');
}

#[test]
fn iso_timestamps_round_trip() {
    assert_eq!(iso_ms(1_700_000_000_000), "2023-11-14T22:13:20.000Z");
    assert_eq!(iso_ms(0), "1970-01-01T00:00:00.000Z");
    assert_eq!(
        parse_iso_ms("2023-11-14T22:13:20.000Z"),
        Some(1_700_000_000_000)
    );
    assert_eq!(
        parse_iso_ms(&iso_ms(1_800_000_123_456)),
        Some(1_800_000_123_456)
    );
    assert_eq!(parse_iso_ms("not a date"), None);
    assert_eq!(parse_iso_ms("2023-13-01T00:00:00.000Z"), None);
}

#[test]
fn a_repo_slug_is_a_safe_stable_name() {
    let r = rig();
    let slug = repo_slug(&r.repo);
    assert!(slug.starts_with("repo-"), "{slug}");
    assert!(slug.len() > "repo-".len() + 15);
    assert!(
        slug.chars()
            .all(|c| c.is_ascii_alphanumeric() || "-._".contains(c)),
        "{slug}"
    );
    assert_eq!(repo_slug(&r.repo), slug, "stable across calls");
}

#[test]
fn create_stores_version_one_in_a_private_directory() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = sample_job(&r, "j1", Access::Write, JobState::Queued);
    let saved = store.create(&job).unwrap();
    assert_eq!(saved.version, 1);
    assert_eq!(store.get("j1").unwrap(), saved);
    let dir = store.job_dir("j1");
    assert_eq!(mode(&dir), 0o700, "the job dir is private");
    assert_eq!(mode(&dir.join("job.toml")), 0o600, "the record is private");
    let text = std::fs::read_to_string(dir.join("job.toml")).unwrap();
    assert!(text.starts_with("id = \"j1\""), "{text}");
}

#[test]
fn a_repeat_id_is_refused() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let job = sample_job(&r, "j1", Access::Write, JobState::Queued);
    store.create(&job).unwrap();
    assert!(matches!(store.create(&job), Err(StoreError::Io { .. })));
}

#[test]
fn an_unsafe_id_is_never_used_as_a_path() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    assert_eq!(
        store.get("../evil"),
        Err(StoreError::NotFound {
            id: "../evil".into()
        })
    );
    assert_eq!(
        store.find("../evil"),
        Err(StoreError::NotFound {
            id: "../evil".into()
        })
    );
}

#[test]
fn find_takes_an_exact_id_or_a_unique_prefix_and_names_ambiguity() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    store
        .create(&sample_job(
            &r,
            "261109-aaa111",
            Access::Write,
            JobState::Queued,
        ))
        .unwrap();
    store
        .create(&sample_job(
            &r,
            "261109-aab222",
            Access::Write,
            JobState::Queued,
        ))
        .unwrap();
    store
        .create(&sample_job(
            &r,
            "261110-zzz999",
            Access::Write,
            JobState::Queued,
        ))
        .unwrap();
    assert_eq!(store.find("261109-aaa111").unwrap().id, "261109-aaa111");
    assert_eq!(store.find("261110").unwrap().id, "261110-zzz999");
    let err = store.find("261109-aa").unwrap_err();
    assert_eq!(
        err,
        StoreError::Ambiguous {
            prefix: "261109-aa".into(),
            matches: vec!["261109-aaa111".into(), "261109-aab222".into()]
        }
    );
    assert_eq!(
        err.to_string(),
        "job id \"261109-aa\" matches 261109-aaa111, 261109-aab222: use a longer job id"
    );
    assert_eq!(
        store.find("nope"),
        Err(StoreError::NotFound { id: "nope".into() })
    );
    assert_eq!(
        StoreError::NotFound { id: "nope".into() }.to_string(),
        "no job matches \"nope\" (/huddle:status lists them)"
    );
}

#[test]
fn update_bumps_the_version_and_refuses_a_stale_one() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut job = store
        .create(&sample_job(&r, "j2", Access::Write, JobState::Queued))
        .unwrap();
    job.state = JobState::Running;
    let updated = store.update(&job).unwrap();
    assert_eq!(updated.version, 2);
    let stale = job;
    assert_eq!(
        store.update(&stale),
        Err(StoreError::VersionConflict {
            id: "j2".into(),
            expected: 1,
            actual: 2
        })
    );
}

#[test]
fn parallel_updates_never_lose_a_write() {
    let r = std::sync::Arc::new(rig());
    let store = std::sync::Arc::new(JobStore::for_repo(&r.paths, &r.repo));
    store
        .create(&sample_job(&r, "j3", Access::Write, JobState::Queued))
        .unwrap();
    let mut handles = vec![];
    for _ in 0..8 {
        let store = store.clone();
        handles.push(std::thread::spawn(move || {
            loop {
                let job = store.get("j3").unwrap();
                match store.update(&job) {
                    Ok(_) => break,
                    Err(StoreError::VersionConflict { .. }) => continue,
                    Err(other) => panic!("{other:?}"),
                }
            }
        }));
    }
    for handle in handles {
        handle.join().unwrap();
    }
    assert_eq!(store.get("j3").unwrap().version, 9);
}

#[test]
fn list_is_newest_first_and_skips_a_corrupt_record() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    let mut old = sample_job(&r, "old", Access::Write, JobState::Queued);
    old.created_at = "2026-11-09T09:00:00.000Z".into();
    let mut new = sample_job(&r, "new", Access::Write, JobState::Queued);
    new.created_at = "2026-11-09T11:00:00.000Z".into();
    store.create(&old).unwrap();
    store.create(&new).unwrap();
    store
        .create(&sample_job(&r, "bad", Access::Write, JobState::Queued))
        .unwrap();
    std::fs::write(store.job_dir("bad").join("job.toml"), "id = [torn").unwrap();
    let ids: Vec<String> = store.list().unwrap().into_iter().map(|j| j.id).collect();
    assert_eq!(ids, vec!["new".to_string(), "old".to_string()]);
}

#[test]
fn prune_removes_the_oldest_terminal_jobs_beyond_the_cap_and_keeps_active_ones() {
    let r = rig();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    for n in 0..5 {
        let mut job = sample_job(&r, &format!("done-{n}"), Access::Write, JobState::Accepted);
        job.created_at = format!("2026-11-09T10:0{n}:00.000Z");
        store.create(&job).unwrap();
    }
    let mut running = sample_job(&r, "running", Access::Write, JobState::Running);
    running.created_at = "2026-11-08T00:00:00.000Z".into();
    store.create(&running).unwrap();
    let removed = store.prune(2).unwrap();
    assert_eq!(removed.len(), 3, "{removed:?}");
    assert!(
        store.get("running").is_ok(),
        "a running job is never pruned"
    );
    assert!(store.get("done-4").is_ok());
    assert!(store.get("done-3").is_ok());
    assert!(store.get("done-0").is_err());
}
