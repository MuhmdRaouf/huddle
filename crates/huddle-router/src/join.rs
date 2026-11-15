//! The join between what the router saw and what Claude Code's session logs say: a session
//! with a request in flight is working, a worktree names the session in it, and a plugin row
//! gains its lane's numbers. Requests are credited earlier, by `LiveState::credit`. Pure: the
//! reading or the snapshot goes in, the same one comes out joined.

use crate::live::{Machine, Snapshot};

/// Joins what a sessions reading says on its own, once per reading: each worktree names the
/// session in it, and the request ids, which the page never receives, are dropped.
pub fn reading(machine: &mut Machine) {
    for session in &mut machine.sessions {
        session.request_ids = Vec::new();
    }
    name_worktree_users(machine);
}

/// Joins requests, sessions and plugin rows in place. The shared reading is never copied;
/// the busy session ids ride beside it.
pub fn apply(snapshot: &mut Snapshot) {
    mark_working(snapshot);
    credit_plugins(snapshot);
}

fn mark_working(snapshot: &mut Snapshot) {
    let busy: Vec<String> = snapshot
        .in_flight
        .iter()
        .filter_map(|row| row.session.clone())
        .collect();
    if busy.is_empty() {
        return;
    }
    let working: Vec<String> = snapshot
        .machine
        .reading
        .sessions
        .iter()
        .filter(|session| busy.iter().any(|id| id == &session.id))
        .map(|session| session.id.clone())
        .collect();
    snapshot.machine.working = working;
}

/// A worktree whose path is a session's `cwd` is used by that session.
fn name_worktree_users(machine: &mut Machine) {
    for tree in &mut machine.worktrees {
        if tree.used_by.is_some() {
            continue;
        }
        let user = machine
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
pub fn credit_plugins(snapshot: &mut Snapshot) {
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
    use std::sync::Arc;

    use super::*;
    use crate::live::{InFlightRow, LaneLast, LaneRow, LiveState, Machine, SnapshotInputs};
    use huddle_sessions::{Session, SessionState, Worktree, WorktreeKind};
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
            state: SessionState::YourTurn,
            started_ms: None,
            last_ms: None,
            state_since_ms: Some(5),
            open_tasks: 0,
            question_tool: None,
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
            quota_out: false,
            causes: Vec::new(),
        }
    }

    fn snapshot_with(machine: Machine) -> Snapshot {
        let live = LiveState::new(0);
        let inputs = SnapshotInputs {
            machine: Arc::new(machine),
            ..SnapshotInputs::default()
        };
        live.snapshot(0, &inputs)
    }

    fn snapshot() -> Snapshot {
        snapshot_with(Machine {
            sessions: vec![session("s1", "/p/huddle"), session("s2", "/p/other")],
            worktrees: Vec::new(),
        })
    }

    fn in_flight(id: &str) -> InFlightRow {
        InFlightRow {
            id: 1,
            started_ms: 0,
            last_byte_ms: 0,
            session: Some(id.into()),
            model: "m".into(),
            lane: "anthropic".into(),
            agent: None,
        }
    }

    fn tree(path: &str) -> Worktree {
        Worktree {
            path: path.into(),
            project: "huddle".into(),
            repo: "/p/huddle".into(),
            branch: None,
            head: None,
            kind: WorktreeKind::Checkout,
            used_by: None,
            changes: None,
            last_commit_ms: None,
        }
    }

    #[test]
    fn a_session_with_a_request_in_flight_is_working() {
        let mut snap = snapshot();
        snap.in_flight = vec![in_flight("s1")];
        apply(&mut snap);
        let json = serde_json::to_value(&snap).expect("snapshot is serializable");
        assert_eq!(json["sessions"][0]["state"], "working");
        assert_eq!(
            json["sessions"][0]["state_since_ms"],
            serde_json::Value::Null
        );
        assert_eq!(json["sessions"][1]["state"], "your_turn");
        // The shared reading keeps what the log says.
        assert_eq!(
            snap.machine.reading.sessions[0].state,
            SessionState::YourTurn
        );
    }

    #[test]
    fn with_nothing_in_flight_the_snapshot_keeps_the_shared_reading() {
        let mut snap = snapshot();
        let reading = Arc::clone(&snap.machine.reading);
        apply(&mut snap);
        assert!(Arc::ptr_eq(&reading, &snap.machine.reading));
    }

    #[test]
    fn l42_a_joined_snapshot_with_a_busy_session_still_shares_the_reading() {
        let mut snap = snapshot();
        let reading = Arc::clone(&snap.machine.reading);
        let count_before = Arc::strong_count(&reading);
        snap.in_flight = vec![in_flight("s1")];
        apply(&mut snap);
        assert!(Arc::ptr_eq(&snap.machine.reading, &reading));
        assert_eq!(Arc::strong_count(&reading), count_before);
    }

    #[test]
    fn l42_the_busy_session_is_written_as_working_and_the_others_are_not() {
        let mut snap = snapshot();
        let reading = Arc::clone(&snap.machine.reading);
        snap.in_flight = vec![in_flight("s1")];
        apply(&mut snap);
        let json = serde_json::to_value(&snap).expect("snapshot is serializable");
        assert_eq!(json["sessions"][0]["state"], "working");
        assert_eq!(
            json["sessions"][0]["state_since_ms"],
            serde_json::Value::Null
        );
        assert_eq!(
            json["sessions"][1],
            serde_json::to_value(&reading.sessions[1]).expect("session is serializable")
        );
        // The shared reading keeps what the log says.
        assert_eq!(reading.sessions[0].state, SessionState::YourTurn);
    }

    #[test]
    fn l42_the_joined_json_is_what_the_old_join_wrote() {
        let mut snap = snapshot_with(Machine {
            sessions: vec![
                session("s1", "/p/huddle"),
                session("s2", "/p/other"),
                session("s3", "/p/third"),
            ],
            worktrees: vec![tree("/p/huddle"), tree("/p/other")],
        });
        snap.in_flight = vec![in_flight("s1")];
        apply(&mut snap);
        // The same machine, joined by hand.
        let mut hand = (*snap.machine.reading).clone();
        hand.sessions[0].state = SessionState::Working;
        hand.sessions[0].state_since_ms = None;
        let joined = serde_json::to_value(&snap).expect("snapshot is serializable");
        let manual = serde_json::to_value(&hand).expect("machine is serializable");
        assert_eq!(joined["sessions"], manual["sessions"]);
        assert_eq!(joined["worktrees"], manual["worktrees"]);
    }

    #[test]
    fn a_worktree_at_a_sessions_cwd_names_it() {
        let mut machine = Machine {
            sessions: vec![session("s1", "/p/huddle")],
            worktrees: vec![tree("/p/huddle"), tree("/p/idle")],
        };
        reading(&mut machine);
        assert_eq!(
            machine.worktrees[0].used_by.as_deref(),
            Some("s1 · Fix the thing")
        );
        assert_eq!(machine.worktrees[1].used_by, None);
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
