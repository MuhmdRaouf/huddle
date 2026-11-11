//! `usage_for` over a fake `~/.claude/projects` tree.

use std::fs::{self, File};
use std::path::Path;
use std::time::{Duration, UNIX_EPOCH};

use huddle_sessions::usage_for;
use huddle_testkit::temp_home::TempHome;

const SINCE_MS: u64 = 1_800_000_000_000;

fn assistant(request_id: &str, output: u64) -> String {
    format!(
        "{{\"type\":\"assistant\",\"requestId\":\"{request_id}\",\"timestamp\":\"2026-11-11T10:00:00.000Z\",\"cwd\":\"/w/huddle\",\"message\":{{\"model\":\"m\",\"stop_reason\":\"end_turn\",\"content\":[{{\"type\":\"text\",\"text\":\"secret words\"}}],\"usage\":{{\"input_tokens\":5,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":3,\"output_tokens\":{output}}}}}}}\n"
    )
}

fn write(path: &Path, text: &str, modified_ms: u64) {
    fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
    fs::write(path, text).expect("write");
    File::options()
        .write(true)
        .open(path)
        .and_then(|file| file.set_modified(UNIX_EPOCH + Duration::from_millis(modified_ms)))
        .expect("mtime");
}

fn ids(list: &[&str]) -> Vec<String> {
    list.iter().map(|id| id.to_string()).collect()
}

#[test]
fn t5_usage_for_finds_ids_in_main_and_subagent_logs() {
    let home = TempHome::new();
    let project = home.root().join("projects/-w-huddle");
    write(
        &project.join("s1.jsonl"),
        &assistant("req_main", 11),
        SINCE_MS + 10,
    );
    write(
        &project.join("s1/subagents/agent-a.jsonl"),
        &assistant("req_sub", 22),
        SINCE_MS + 10,
    );
    write(
        &project.join("s1/subagents/agent-b.jsonl"),
        &assistant("req_other", 33),
        SINCE_MS + 10,
    );

    let mut found = usage_for(home.root(), &ids(&["req_main", "req_sub"]), SINCE_MS);
    found.sort_by(|a, b| a.request_id.cmp(&b.request_id));

    assert_eq!(found.len(), 2);
    assert_eq!(found[0].request_id, "req_main");
    assert_eq!(found[0].session, "s1");
    assert_eq!(found[0].agent_id, "main");
    assert_eq!(found[0].project.as_deref(), Some("huddle"));
    assert_eq!(found[0].usage.output, 11);
    assert_eq!(found[0].usage.cache_read, 7);
    assert_eq!(found[0].stop.as_deref(), Some("end_turn"));
    assert_eq!(found[1].request_id, "req_sub");
    assert_eq!(found[1].agent_id, "agent-a");
    assert_eq!(found[1].usage.output, 22);
}

#[test]
fn t5_usage_for_ignores_logs_older_than_since() {
    let home = TempHome::new();
    let project = home.root().join("projects/-w-huddle");
    write(
        &project.join("old.jsonl"),
        &assistant("req_old", 1),
        SINCE_MS - 1,
    );
    write(
        &project.join("old/subagents/agent-a.jsonl"),
        &assistant("req_old_sub", 1),
        SINCE_MS - 1,
    );

    let found = usage_for(home.root(), &ids(&["req_old", "req_old_sub"]), SINCE_MS);

    assert!(found.is_empty(), "{found:?}");
}

#[test]
fn t5_usage_for_with_no_ids_or_no_folder_is_empty() {
    let home = TempHome::new();
    assert!(usage_for(home.root(), &ids(&["x"]), 0).is_empty());
    assert!(usage_for(home.root(), &[], 0).is_empty());
}
