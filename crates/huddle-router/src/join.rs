//! The join between what the router saw and what Claude Code's session logs say: a session
//! with a request in flight is working, a worktree names the session in it, and a plugin row
//! gains its lane's numbers. Requests are credited earlier, by `LiveState::credit`. Pure: the
//! snapshot goes in, the same snapshot comes out joined.

use huddle_sessions::SessionState;

use crate::live::Snapshot;

/// Joins requests, sessions, worktrees and plugin rows in place.
pub fn apply(snapshot: &mut Snapshot) {
    mark_working(snapshot);
    name_worktree_users(snapshot);
    credit_plugins(snapshot);
}

fn mark_working(snapshot: &mut Snapshot) {
    for session in &mut snapshot.sessions {
        let busy = snapshot
            .in_flight
            .iter()
            .any(|row| row.session.as_deref() == Some(session.id.as_str()));
        if busy {
            session.state = SessionState::Working;
            session.waiting_since_ms = None;
        }
    }
}

/// A worktree whose path is a session's `cwd` is used by that session.
fn name_worktree_users(snapshot: &mut Snapshot) {
    for tree in &mut snapshot.worktrees {
        if tree.used_by.is_some() {
            continue;
        }
        let user = snapshot
            .sessions
            .iter()
            .find(|session| session.cwd.as_deref() == Some(tree.path.as_str()));
        if let Some(session) = user {
            let short: String = session.id.chars().take(8).collect();
            tree.used_by = Some(if session.title.is_empty() {
                short
            } else {
                format!("{short} · {}", session.title)
            });
        }
    }
}

/// Each plugin row gains `working` (its lane's last answer was a 200; null before any request)
/// and `requests` (the lane's count).
fn credit_plugins(snapshot: &mut Snapshot) {
    let Some(rows) = snapshot
        .plugins
        .get_mut("rows")
        .and_then(serde_json::Value::as_array_mut)
    else {
        return;
    };
    for row in rows {
        let Some(object) = row.as_object_mut() else {
            continue;
        };
        let lane = object
            .get("name")
            .and_then(serde_json::Value::as_str)
            .and_then(|name| snapshot.lanes.iter().find(|lane| lane.id == name));
        let working = lane
            .and_then(|lane| lane.last)
            .map(|last| last.status == 200);
        let requests = lane.map_or(0, |lane| lane.requests);
        object.insert("working".into(), working.into());
        object.insert("requests".into(), requests.into());
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::live::{InFlightRow, LaneLast, LaneRow, LiveState, SnapshotInputs};
    use huddle_sessions::{Session, Worktree, WorktreeKind};
    use serde_json::json;

    fn session(id: &str, cwd: &str) -> Session {
        Session {
            id: id.into(),
            title: "Fix the thing".into(),
            project: None,
            repo: None,
            worktree: None,
            cwd: Some(cwd.into()),
            branch: None,
            model: None,
            state: SessionState::Waiting,
            started_ms: None,
            last_ms: None,
            waiting_since_ms: Some(5),
            context_tokens: None,
            context_limit: 200_000,
            cache_ttl_ms: 300_000,
            last_request_ms: None,
            agents: Vec::new(),
            request_ids: Vec::new(),
        }
    }

    fn lane(id: &str, requests: u64, last: Option<u16>) -> LaneRow {
        LaneRow {
            id: id.into(),
            name: id.into(),
            enabled: true,
            models: String::new(),
            setup: None,
            key: None,
            requests,
            errors: 0,
            ttft_p50_ms: None,
            ttft_p95_ms: None,
            ttft_p50: None,
            last: last.map(|status| LaneLast { status, at_ms: 1 }),
            tokens_in: 0,
            tokens_out: 0,
        }
    }

    fn snapshot() -> Snapshot {
        let live = LiveState::new(0);
        let mut snapshot = live.snapshot(0, &SnapshotInputs::default());
        snapshot.sessions = vec![session("s1", "/p/huddle")];
        snapshot
    }

    #[test]
    fn a_session_with_a_request_in_flight_is_working() {
        let mut snap = snapshot();
        snap.sessions.push(session("s2", "/p/other"));
        snap.in_flight = vec![InFlightRow {
            id: 1,
            started_ms: 0,
            last_byte_ms: 0,
            session: Some("s1".into()),
            model: "m".into(),
            lane: "anthropic".into(),
            agent: None,
        }];
        apply(&mut snap);
        assert_eq!(snap.sessions[0].state, SessionState::Working);
        assert_eq!(snap.sessions[0].waiting_since_ms, None);
        assert_eq!(snap.sessions[1].state, SessionState::Waiting);
    }

    #[test]
    fn a_worktree_at_a_sessions_cwd_names_it() {
        let mut snap = snapshot();
        let tree = |path: &str| Worktree {
            path: path.into(),
            project: "huddle".into(),
            repo: "/p/huddle".into(),
            branch: None,
            head: None,
            kind: WorktreeKind::Checkout,
            used_by: None,
            changes: None,
            last_commit_ms: None,
        };
        snap.worktrees = vec![tree("/p/huddle"), tree("/p/idle")];
        apply(&mut snap);
        assert_eq!(
            snap.worktrees[0].used_by.as_deref(),
            Some("s1 · Fix the thing")
        );
        assert_eq!(snap.worktrees[1].used_by, None);
    }

    #[test]
    fn plugin_rows_gain_working_and_requests_from_their_lane() {
        let mut snap = snapshot();
        snap.lanes = vec![
            lane("zai", 12, Some(200)),
            lane("kimi", 3, Some(529)),
            lane("qwen", 0, None),
        ];
        snap.plugins = json!({"state": "ready", "rows": [
            {"name": "zai"}, {"name": "kimi"}, {"name": "qwen"}, {"name": "huddle"}
        ], "checks": []});
        apply(&mut snap);
        let rows = &snap.plugins["rows"];
        assert_eq!(
            (&rows[0]["working"], &rows[0]["requests"]),
            (&json!(true), &json!(12))
        );
        assert_eq!(rows[1]["working"], json!(false));
        assert_eq!(rows[2]["working"], json!(null));
        assert_eq!(
            (&rows[3]["working"], &rows[3]["requests"]),
            (&json!(null), &json!(0))
        );
    }

    #[test]
    fn a_plugins_value_without_rows_is_left_alone() {
        let mut snap = snapshot();
        snap.plugins = json!({"state": "checking"});
        apply(&mut snap);
        assert_eq!(snap.plugins, json!({"state": "checking"}));
    }
}
