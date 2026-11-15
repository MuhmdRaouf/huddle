//! K2: `/state?recent=0` answers the snapshot without the recent rows; the counts a page derives
//! from them come from the server in both forms.

use http_body_util::BodyExt;
use huddle_router::dashboard::PAGE;
use huddle_router::dashboard::{Dashboard, DashboardInputs};
use huddle_router::live::{Finish, LiveState, RequestStart};
use serde_json::Value;
use std::sync::Arc;

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn dashboard_with_rows(statuses: &[(u16, &str, Option<&str>)]) -> Dashboard {
    let live = Arc::new(LiveState::new(0));
    let now = now_ms();
    for (i, (status, session, error)) in statuses.iter().enumerate() {
        let at = now - 1000 + i as u64;
        let id = live.start(RequestStart {
            session: Some((*session).to_owned()),
            model: "glm-5.3".into(),
            lane: "zai".into(),
            now_ms: at,
        });
        live.finish(
            id,
            Finish {
                status: *status,
                error: error.map(str::to_owned),
                now_ms: at + 5,
                ..Finish::default()
            },
        );
    }
    let enabled: Arc<dyn Fn() -> Vec<String> + Send + Sync> = Arc::new(|| vec!["zai".to_owned()]);
    Dashboard::new(live, DashboardInputs::default(), enabled)
}

fn zai(form: &Value) -> &Value {
    form["lanes"]
        .as_array()
        .and_then(|lanes| lanes.iter().find(|l| l["id"] == "zai"))
        .expect("zai lane")
}

async fn json_of(body: huddle_router::server::RespBody) -> Value {
    let body = body.collect().await.expect("body").to_bytes();
    serde_json::from_slice(&body).expect("json")
}

const ROWS: [(u16, &str, Option<&str>); 5] = [
    (200, "s1", None),
    (499, "s1", None),
    (529, "s2", Some("overloaded")),
    (200, "s2", None),
    (499, "s2", None),
];

#[tokio::test]
async fn k2_state_without_recent_has_no_rows() {
    let dashboard = dashboard_with_rows(&ROWS);
    let mut full = json_of(dashboard.state_answer().into_body()).await;
    let mut slim = json_of(dashboard.state_slim_answer().into_body()).await;
    assert!(slim.get("recent").is_none(), "slim form carries no recent");
    assert_eq!(full["recent"].as_array().map(Vec::len), Some(5));
    for value in [&mut full, &mut slim] {
        let object = value.as_object_mut().expect("object");
        object.remove("recent");
        object.remove("now_ms");
    }
    assert_eq!(full, slim);
}

#[tokio::test]
async fn k2_state_default_is_unchanged() {
    let dashboard = dashboard_with_rows(&ROWS);
    let full = json_of(dashboard.state_answer().into_body()).await;
    let row = &full["recent"][0];
    for key in [
        "id", "at_ms", "session", "model", "lane", "status", "dur_ms",
    ] {
        assert!(row.get(key).is_some(), "{key}");
    }
    for key in ["cancelled_15m", "failed_15m", "by_session"] {
        assert!(full["recent_counts"].get(key).is_some(), "{key}");
    }
    assert!(zai(&full).get("quota_out").is_some());
    assert!(zai(&full)["causes"].is_array());
}

#[tokio::test]
async fn k2_client_left_count_matches_recent() {
    let dashboard = dashboard_with_rows(&ROWS);
    let full = json_of(dashboard.state_answer().into_body()).await;
    let slim = json_of(dashboard.state_slim_answer().into_body()).await;
    let rows = full["recent"].as_array().expect("recent");
    let left = rows.iter().filter(|r| r["status"] == 499).count();
    let failed = rows.iter().filter(|r| r["status"] != 200).count();
    for form in [&full, &slim] {
        assert_eq!(form["recent_counts"]["cancelled_15m"], left);
        assert_eq!(form["recent_counts"]["failed_15m"], failed);
        let s2 = &form["recent_counts"]["by_session"]["s2"];
        assert_eq!(s2["requests"], 3);
        assert_eq!(s2["errors"], 1);
        assert_eq!(zai(form)["causes"][0]["cause"], "overloaded");
        assert_eq!(zai(form)["causes"][0]["count"], 1);
    }
}

#[test]
fn k2_page_polls_slim_off_requests_and_errors() {
    assert!(
        PAGE.contains("const RECENT_PAGES=["),
        "the pages that show recent rows are named"
    );
    let list = PAGE
        .split("const RECENT_PAGES=[")
        .nth(1)
        .and_then(|rest| rest.split(']').next())
        .expect("list");
    for page in ["requests", "errors"] {
        assert!(list.contains(&format!("'{page}'")), "{page} polls full");
    }
    for page in [
        "overview",
        "models",
        "providers",
        "sessions",
        "projects",
        "usage",
    ] {
        assert!(!list.contains(&format!("'{page}'")), "{page} polls slim");
    }
    assert!(PAGE.contains("?recent=0"), "the slim poll url");
    assert!(
        PAGE.contains("function stateUrl()"),
        "the url is chosen at each poll"
    );
}

#[tokio::test]
async fn k2_slim_form_with_200_rows_is_under_25_kb() {
    let rows: Vec<(u16, &str, Option<&str>)> = (0..200)
        .map(|i| (if i % 7 == 0 { 529 } else { 200 }, "s1", None))
        .collect();
    let dashboard = dashboard_with_rows(&rows);
    let size = |response: http_body_util::combinators::BoxBody<_, _>| async move {
        response
            .collect()
            .await
            .map(|b| b.to_bytes().len())
            .unwrap_or(0)
    };
    let timed = |slim: bool| {
        let start = std::time::Instant::now();
        for _ in 0..50 {
            let _ = if slim {
                dashboard.state_slim_answer()
            } else {
                dashboard.state_answer()
            };
        }
        start.elapsed()
    };
    let (full_time, slim_time) = (timed(false), timed(true));
    let full = size(dashboard.state_answer().into_body()).await;
    let slim = size(dashboard.state_slim_answer().into_body()).await;
    eprintln!("k2 bytes full={full} slim={slim}; 50 calls full={full_time:?} slim={slim_time:?}");
    assert!(slim < 25_000, "{slim}");
    assert!(slim < full);
}

/// A dashboard whose inputs hold one session, one worktree and a plugin reading, and no feed.
fn dashboard_with_machine() -> Dashboard {
    use huddle_router::dashboard::Machine;
    use huddle_sessions::{Session, SessionState, Worktree, WorktreeKind};
    let session = Session {
        id: "s1".into(),
        title: String::new(),
        project: None,
        repo: None,
        worktree: None,
        cwd: Some("/p/huddle".into()),
        branch: None,
        model: None,
        state: SessionState::YourTurn,
        started_ms: None,
        last_ms: None,
        state_since_ms: None,
        open_tasks: 0,
        question_tool: None,
        context_tokens: None,
        context_limit: 200_000,
        cache_ttl_ms: 300_000,
        last_request_ms: None,
        agents: Vec::new(),
        request_ids: Vec::new(),
    };
    let tree = Worktree {
        path: "/p/huddle".into(),
        project: "huddle".into(),
        repo: "/p/huddle".into(),
        branch: Some("main".into()),
        head: None,
        kind: WorktreeKind::Checkout,
        used_by: None,
        changes: None,
        last_commit_ms: None,
    };
    let machine = Arc::new(Machine {
        sessions: vec![session],
        worktrees: vec![tree],
    });
    let inputs = DashboardInputs {
        sessions: Arc::new(move || Arc::clone(&machine)),
        plugins: Arc::new(|| serde_json::json!({ "state": "ready", "rows": [], "checks": [] })),
        ..DashboardInputs::default()
    };
    Dashboard::new(Arc::new(LiveState::new(0)), inputs, Arc::new(Vec::new))
}

#[tokio::test]
async fn l28_a_state_poll_returns_sessions_worktrees_and_plugins() {
    let dashboard = dashboard_with_machine();
    for slim in [false, true] {
        let answer = if slim {
            dashboard.state_slim_answer()
        } else {
            dashboard.state_answer()
        };
        let state = json_of(answer.into_body()).await;
        assert_eq!(
            state["sessions"].as_array().map(Vec::len),
            Some(1),
            "slim={slim}"
        );
        assert_eq!(
            state["worktrees"].as_array().map(Vec::len),
            Some(1),
            "slim={slim}"
        );
        assert_eq!(state["plugins"]["state"], "ready", "slim={slim}");
    }
}
