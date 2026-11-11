//! `scan` over a fake `~/.claude/projects` tree copied from `tests/fixtures/claude`.

use std::fs::{self, File};
use std::io;
use std::path::Path;
use std::time::{Duration, UNIX_EPOCH};

use huddle_sessions::{AgentState, Limits, Session, SessionState, parse_iso_ms, scan};
use huddle_testkit::temp_home::TempHome;

const HOUR_MS: u64 = 3_600_000;

fn now_ms() -> u64 {
    parse_iso_ms("2026-11-11T12:00:00.000Z").expect("fixed instant parses")
}

fn copy_tree(from: &Path, to: &Path) -> io::Result<()> {
    fs::create_dir_all(to)?;
    for entry in fs::read_dir(from)? {
        let entry = entry?;
        let target = to.join(entry.file_name());
        if entry.file_type()?.is_dir() {
            copy_tree(&entry.path(), &target)?;
        } else {
            fs::copy(entry.path(), target)?;
        }
    }
    Ok(())
}

fn set_age(path: &Path, age_ms: u64) {
    let at = UNIX_EPOCH + Duration::from_millis(now_ms() - age_ms);
    File::options()
        .write(true)
        .open(path)
        .and_then(|file| file.set_modified(at))
        .expect("set mtime");
}

/// The fixture tree inside a temp home, with modification times that match the fixed `now`.
fn claude_dir(home: &TempHome) -> std::path::PathBuf {
    let claude = home.root().join("claude-fixture");
    let source = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/claude");
    copy_tree(&source, &claude).expect("copy fixtures");
    let project = claude.join("projects/-Users-x-huddle");
    set_age(&project.join("s-working.jsonl"), 3_000);
    set_age(&project.join("s-waiting.jsonl"), 10 * 60_000);
    set_age(&project.join("s-idle.jsonl"), 3 * HOUR_MS);
    set_age(&project.join("s-old.jsonl"), 30 * HOUR_MS);
    claude
}

fn scanned() -> Vec<Session> {
    let home = TempHome::new();
    scan(&claude_dir(&home), now_ms(), Limits::default())
}

fn by_id<'a>(sessions: &'a [Session], id: &str) -> &'a Session {
    sessions
        .iter()
        .find(|s| s.id == id)
        .expect("session listed")
}

#[test]
fn d1_lists_logs_modified_in_the_last_day_newest_first() {
    let ids: Vec<String> = scanned().into_iter().map(|s| s.id).collect();
    assert_eq!(ids, ["s-working", "s-waiting", "s-idle"]);
}

#[test]
fn d1_keeps_only_the_newest_sessions_up_to_the_limit() {
    let home = TempHome::new();
    let limits = Limits {
        max_sessions: 2,
        ..Limits::default()
    };
    let ids: Vec<String> = scan(&claude_dir(&home), now_ms(), limits)
        .into_iter()
        .map(|s| s.id)
        .collect();
    assert_eq!(ids, ["s-working", "s-waiting"]);
}

#[test]
fn d1_a_missing_claude_dir_lists_nothing() {
    let home = TempHome::new();
    let missing = home.root().join("nope");
    assert!(scan(&missing, now_ms(), Limits::default()).is_empty());
}

#[test]
fn d1_waiting_session_carries_its_identity_and_timing() {
    let sessions = scanned();
    let s = by_id(&sessions, "s-waiting");
    assert_eq!(s.title, "Fix the router");
    assert_eq!(s.project.as_deref(), Some("huddle"));
    assert_eq!(s.cwd.as_deref(), Some("/Users/x/huddle"));
    assert_eq!(s.branch.as_deref(), Some("main"));
    assert_eq!(s.model.as_deref(), Some("claude-opus-5-5"));
    assert_eq!(s.state, SessionState::Waiting);
    let ended = parse_iso_ms("2026-11-11T11:50:00.000Z");
    assert_eq!(s.waiting_since_ms, ended);
    assert_eq!(s.last_ms, ended);
    assert_eq!(s.started_ms, parse_iso_ms("2026-11-11T11:00:00.000Z"));
    assert_eq!(s.last_request_ms, ended);
}

#[test]
fn d1_context_and_cache_come_from_the_last_usage() {
    let sessions = scanned();
    let s = by_id(&sessions, "s-waiting");
    assert_eq!(s.context_tokens, Some(3 + 200 + 2000));
    assert_eq!(s.context_limit, 200_000);
    assert_eq!(s.cache_ttl_ms, 3_600_000);
    let w = by_id(&sessions, "s-working");
    assert_eq!(w.cache_ttl_ms, 300_000);
    assert_eq!(w.context_limit, 1_000_000);
}

#[test]
fn d1_working_idle_and_the_ai_title_fallback() {
    let sessions = scanned();
    let w = by_id(&sessions, "s-working");
    assert_eq!(w.state, SessionState::Working);
    assert_eq!(w.waiting_since_ms, None);
    assert_eq!(w.title, "Only an AI title");
    assert_eq!(w.branch.as_deref(), Some("dev"));
    let i = by_id(&sessions, "s-idle");
    assert_eq!(i.state, SessionState::Idle);
    assert_eq!(i.waiting_since_ms, None);
    assert_eq!(i.title, "");
}

#[test]
fn d1_subagents_come_from_their_logs_and_meta_files() {
    let sessions = scanned();
    let s = by_id(&sessions, "s-waiting");
    let ids: Vec<&str> = s.agents.iter().map(|a| a.id.as_str()).collect();
    assert_eq!(ids, ["main", "agent-aaa", "agent-bbb"]);
    let main = &s.agents[0];
    assert_eq!(
        (main.kind.as_str(), main.state),
        ("main", AgentState::Waiting)
    );
    let done = &s.agents[1];
    assert_eq!(done.kind, "sonnet-5.5");
    assert_eq!(done.description, "Build ticket D1");
    assert_eq!(done.model.as_deref(), Some("claude-sonnet-5-5"));
    assert_eq!(done.state, AgentState::Done);
    assert_eq!(done.started_ms, parse_iso_ms("2026-11-11T11:20:00.000Z"));
    assert_eq!(done.ended_ms, parse_iso_ms("2026-11-11T11:30:00.000Z"));
    assert_eq!(done.worktree, None);
    let idle = &s.agents[2];
    assert_eq!(
        (idle.kind.as_str(), idle.description.as_str()),
        ("Explore", "Map the router")
    );
    assert_eq!(idle.state, AgentState::Waiting);
    assert_eq!(idle.ended_ms, None);
}

#[test]
fn d1_request_ids_keep_the_last_usage_of_each_request() {
    let sessions = scanned();
    let tags = &by_id(&sessions, "s-waiting").request_ids;
    let seen: Vec<(&str, &str, u64, u64, u64, u64)> = tags
        .iter()
        .map(|t| {
            let u = &t.usage;
            (
                t.request_id.as_str(),
                t.agent_id.as_str(),
                u.input,
                u.cache_read,
                u.cache_write,
                u.output,
            )
        })
        .collect();
    assert_eq!(
        seen,
        [
            ("req_m1", "main", 2, 1000, 100, 10),
            ("req_m2", "main", 3, 2000, 200, 40),
            ("req_a1", "agent-aaa", 2, 1000, 100, 10),
            ("req_b1", "agent-bbb", 2, 1000, 100, 10),
        ]
    );
}

#[test]
fn d1_a_small_tail_cap_drops_the_cut_line_and_keeps_the_end() {
    let home = TempHome::new();
    let limits = Limits {
        tail_bytes: 700,
        ..Limits::default()
    };
    let sessions = scan(&claude_dir(&home), now_ms(), limits);
    let s = by_id(&sessions, "s-waiting");
    assert_eq!(s.state, SessionState::Waiting);
    assert_eq!(
        s.request_ids.last().map(|t| t.request_id.as_str()),
        Some("req_b1")
    );
}

#[test]
fn d1_no_message_text_or_tool_input_reaches_the_output() {
    let json = serde_json::to_string(&scanned()).expect("serialize");
    for secret in ["SECRET", "PROMPT TEXT", "tool_use"] {
        assert!(!json.contains(secret), "{secret} leaked");
    }
    assert!(
        !json.contains("request_ids"),
        "request tags stay inside the daemon"
    );
}
