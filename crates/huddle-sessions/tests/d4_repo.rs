//! A session's project is its git repository, found without running git.

use std::fs;
use std::path::Path;

use huddle_sessions::{Limits, Session, parse_iso_ms, scan};
use huddle_testkit::temp_home::TempHome;

fn now_ms() -> u64 {
    parse_iso_ms("2026-11-11T12:00:00.000Z").expect("fixed instant parses")
}

/// A claude dir holding one session log whose working directory is `cwd`.
fn claude_with_session(home: &TempHome, id: &str, cwd: &Path) -> std::path::PathBuf {
    let claude = home.root().join(format!("claude-{id}"));
    let dir = claude.join("projects/-p");
    fs::create_dir_all(&dir).expect("projects dir");
    let line = format!(
        r#"{{"type":"assistant","timestamp":"2026-11-11T11:59:57.000Z","cwd":"{}","sessionId":"{id}","requestId":"r","message":{{"model":"m","stop_reason":"end_turn","usage":{{"input_tokens":1,"output_tokens":1}},"content":[]}}}}"#,
        cwd.to_str().expect("utf8 path")
    );
    fs::write(dir.join(format!("{id}.jsonl")), format!("{line}\n")).expect("log");
    claude
}

fn only(claude: &Path) -> Session {
    let limits = Limits {
        max_age_ms: u64::MAX / 4,
        ..Limits::default()
    };
    let mut sessions = scan(claude, now_ms(), limits);
    assert_eq!(sessions.len(), 1, "{sessions:?}");
    sessions.remove(0)
}

#[test]
fn d4_a_worktree_session_belongs_to_its_repo() {
    let home = TempHome::new();
    let repo = home.make_repo().expect("repo");
    let tree = repo.join(".agents/worktrees/feat-x");
    let target = tree.to_str().expect("utf8 path");
    home.git(&repo, &["worktree", "add", "-q", "-b", "feat/x", target])
        .expect("worktree add");
    let root = repo.canonicalize().expect("canonical repo");
    let tree = tree.canonicalize().expect("canonical tree");

    let claude = claude_with_session(&home, "s-tree", &tree);
    let session = only(&claude);
    assert_eq!(session.project.as_deref(), Some("repo"));
    assert_eq!(session.repo.as_deref(), root.to_str());
    assert_eq!(session.worktree.as_deref(), tree.to_str());

    let sub = tree.join("crates");
    fs::create_dir(&sub).expect("subfolder");
    let deeper = claude_with_session(&home, "s-sub", &sub);
    let session = only(&deeper);
    assert_eq!(session.repo.as_deref(), root.to_str());
    assert_eq!(session.worktree.as_deref(), tree.to_str());
}

#[test]
fn d4_a_plain_folder_is_its_own_project() {
    let home = TempHome::new();
    let plain = home.root().join("notes");
    fs::create_dir(&plain).expect("plain dir");
    let plain = plain.canonicalize().expect("canonical");
    let claude = claude_with_session(&home, "s-plain", &plain);
    let session = only(&claude);
    assert_eq!(session.project.as_deref(), Some("notes"));
    assert_eq!(session.repo.as_deref(), plain.to_str());
    assert_eq!(session.worktree.as_deref(), plain.to_str());
}
