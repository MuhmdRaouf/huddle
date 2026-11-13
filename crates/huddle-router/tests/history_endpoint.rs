use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use huddle_catalog::prices::{TokenCounts, cost_usd};
use huddle_history::{History, Row};
use huddle_router::dashboard::{Dashboard, DashboardInputs};
use huddle_router::live::LiveState;
use huddle_router::server::{
    EntryCtx, Handler, ServerLimits, anthropic_error, bind, entry_handler, serve,
};
use huddle_testkit::temp_home::TempHome;
use hyper::body::Incoming;
use hyper::{Request, StatusCode};
use serde_json::Value;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

const HOST: &str = "hub.raouf.me";
const MIN: u64 = 60_000;

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

struct Rig {
    port: u16,
    opened: Arc<AtomicUsize>,
    _home: TempHome,
}

fn row(at_ms: u64, model: &str, lane: &str, status: u16) -> Row {
    Row {
        at_ms,
        model: model.into(),
        lane: lane.into(),
        status,
        dur_ms: 50,
        ..Row::default()
    }
}

/// A claude row (2M in, 1M of it cache read, 100k out) and a glm row (1M in, 1M out) in
/// the last minutes, plus an old and an unknown-usage row.
fn seed(db: &Path) {
    let t = now();
    let mut claude = row(t - 2 * MIN, "claude-opus-5-5", "anthropic", 200);
    claude.session = Some("s1".into());
    claude.project = Some("/work/a b".into());
    claude.tokens_in = Some(2_000_000);
    claude.cache_read = Some(1_000_000);
    claude.cache_write = Some(0);
    claude.tokens_out = Some(100_000);
    claude.ttft_ms = Some(300);
    let mut glm = row(t - MIN, "glm-5.3", "zai", 200);
    glm.session = Some("s2".into());
    glm.tokens_in = Some(1_000_000);
    glm.cache_read = Some(0);
    glm.cache_write = Some(0);
    glm.tokens_out = Some(1_000_000);
    let failed = row(t - 3 * MIN, "glm-5.3", "zai", 500);
    let old = row(t - 3 * 86_400_000, "glm-5.3", "zai", 200);
    History::open(db)
        .expect("open")
        .insert(&[claude, glm, failed, old])
        .expect("insert");
}

async fn rig(with_db: bool) -> Rig {
    let home = TempHome::new();
    let db = home.root().join("huddle.db");
    if with_db {
        seed(&db);
    }
    let opened = Arc::new(AtomicUsize::new(0));
    let inputs = DashboardInputs {
        history: {
            let (db, opened) = (db.clone(), opened.clone());
            Arc::new(move || {
                opened.fetch_add(1, Ordering::SeqCst);
                db.exists().then(|| History::open(&db).ok()).flatten()
            })
        },
        ..DashboardInputs::default()
    };
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let providers: Arc<dyn Fn() -> Vec<String> + Send + Sync> = Arc::new(Vec::new);
    let dashboard = Dashboard::new(Arc::new(LiveState::new(0)), inputs, providers.clone());
    let refuse: Handler = Arc::new(|_req: Request<Incoming>| {
        Box::pin(async move { anthropic_error(StatusCode::IM_A_TEAPOT, "x", "forwarded") })
    });
    let handler = entry_handler(Arc::new(EntryCtx {
        port: Some(port),
        connect: refuse.clone(),
        forward: refuse,
        providers,
        dashboard: Some(Arc::new(dashboard)),
        dashboard_hosts: Arc::new(|| vec![HOST.to_string()]),
    }));
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        port,
        opened,
        _home: home,
    }
}

async fn get(port: u16, path: &str, host: &str) -> (String, String) {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let host = if host == "127.0.0.1" {
        format!("127.0.0.1:{port}")
    } else {
        host.to_string()
    };
    let req = format!("GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n");
    s.write_all(req.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    let text = String::from_utf8_lossy(&out).into_owned();
    let status = text.lines().next().unwrap_or("").to_string();
    let body = text.split("\r\n\r\n").nth(1).unwrap_or("").to_string();
    (status, body)
}

async fn json(rig: &Rig, query: &str) -> Value {
    let (status, body) = get(
        rig.port,
        &format!("/huddle-router/history?{query}"),
        "127.0.0.1",
    )
    .await;
    assert!(status.contains("200"), "{query}: {status} {body}");
    serde_json::from_str(&body).unwrap_or_else(|e| panic!("{query}: {e}: {body}"))
}

fn opus_cost() -> f64 {
    cost_usd(
        "claude-opus-5-5",
        &TokenCounts {
            input: 1_000_000,
            output: 100_000,
            cache_read: 1_000_000,
            cache_write: 0,
        },
    )
}

#[tokio::test]
async fn t6_buckets_carry_cost_and_the_price_dates() {
    let rig = rig(true).await;
    let v = json(&rig, "view=buckets&range=15m").await;
    assert_eq!(v["width_ms"], 30_000);
    assert!(v["prices_retrieved"].is_string() && v["claude_prices_retrieved"].is_string());
    let buckets = v["buckets"].as_array().expect("buckets");
    assert_eq!(buckets.len(), 3);
    let glm = buckets
        .iter()
        .find(|b| b["model"] == "glm-5.3" && b["err"] == 0)
        .expect("glm");
    assert!((glm["cost_usd"].as_f64().unwrap_or(0.0) - 5.8).abs() < 1e-9);
    let claude = buckets
        .iter()
        .find(|b| b["model"] == "claude-opus-5-5")
        .expect("claude");
    assert!((claude["cost_usd"].as_f64().unwrap_or(0.0) - opus_cost()).abs() < 1e-9);
    assert_eq!(claude["ttft_p50"], 300);
}

#[tokio::test]
async fn t6_range_widths_follow_the_range() {
    let rig = rig(true).await;
    for (range, width) in [
        ("15m", 30_000u64),
        ("1h", 60_000),
        ("24h", 900_000),
        ("7d", 7_200_000),
        ("30d", 43_200_000),
        ("90d", 86_400_000),
    ] {
        let v = json(&rig, &format!("view=buckets&range={range}")).await;
        assert_eq!(v["width_ms"], width, "{range}");
        assert_eq!(v["range"], range);
    }
    for (range, requests) in [("24h", 3), ("7d", 4)] {
        let v = json(&rig, &format!("view=buckets&range={range}")).await;
        let n: u64 = v["buckets"]
            .as_array()
            .map(|all| all.iter().filter_map(|b| b["n"].as_u64()).sum())
            .unwrap_or(0);
        assert_eq!(n, requests, "{range}");
    }
}

#[tokio::test]
async fn t6_totals_price_each_group_and_flag_api_equivalent() {
    let rig = rig(true).await;
    let v = json(&rig, "view=totals&range=24h&by=lane").await;
    let rows = v["totals"].as_array().expect("totals");
    let anthropic = rows
        .iter()
        .find(|r| r["key"] == "anthropic")
        .expect("anthropic");
    assert_eq!(anthropic["api_equivalent"], true);
    assert!((anthropic["cost_usd"].as_f64().unwrap_or(0.0) - opus_cost()).abs() < 1e-9);
    let zai = rows.iter().find(|r| r["key"] == "zai").expect("zai");
    assert_eq!(zai["api_equivalent"], false);
    assert!((zai["cost_usd"].as_f64().unwrap_or(0.0) - 5.8).abs() < 1e-9);
    assert_eq!(zai["n"], 2);
    assert_eq!(zai["err"], 1);
    assert_eq!(zai["models"][0]["model"], "glm-5.3");
    for by in ["session", "project", "model", "day"] {
        let v = json(&rig, &format!("view=totals&by={by}")).await;
        assert_eq!(v["by"], by);
    }
}

#[tokio::test]
async fn t6_search_filters_pages_and_prices_rows() {
    let rig = rig(true).await;
    let all = json(&rig, "view=search&range=24h").await;
    assert_eq!(all["total"], 3);
    let rows = all["rows"].as_array().expect("rows");
    assert_eq!(rows[0]["model"], "glm-5.3", "newest first");
    assert!((rows[0]["cost_usd"].as_f64().unwrap_or(0.0) - 5.8).abs() < 1e-9);
    assert_eq!(rows[1]["model"], "claude-opus-5-5");
    assert!((rows[1]["cost_usd"].as_f64().unwrap_or(0.0) - opus_cost()).abs() < 1e-9);
    assert!(rows[2]["cost_usd"].is_null(), "unknown usage has no cost");
    assert!(rows[2]["session"].is_null());

    let errors = json(&rig, "view=search&status=error").await;
    assert_eq!(errors["total"], 1);
    let ok = json(&rig, "view=search&status=ok&lane=zai").await;
    assert_eq!(ok["total"], 1);
    let by_project = json(&rig, "view=search&project=%2Fwork%2Fa%20b").await;
    assert_eq!(by_project["total"], 1);
    let by_session = json(&rig, "view=search&session=s2&model=glm-5.3").await;
    assert_eq!(by_session["total"], 1);
    let week = json(&rig, "view=search&range=7d").await;
    assert_eq!(week["total"], 4);

    let page = json(&rig, "view=search&limit=1&offset=1").await;
    assert_eq!(page["rows"].as_array().map(Vec::len), Some(1));
    assert_eq!(page["rows"][0]["model"], "claude-opus-5-5");
    assert_eq!(page["total"], 3);
    let capped = json(&rig, "view=search&limit=9999").await;
    assert_eq!(capped["limit"], 500);
}

#[tokio::test]
async fn t6_bad_parameters_are_400_and_an_unknown_view_404() {
    let rig = rig(true).await;
    for query in [
        "view=totals&by=colour",
        "view=buckets&range=5y",
        "view=search&status=maybe",
        "view=search&limit=abc",
        "view=search&offset=-1",
        "range=24h",
    ] {
        let (status, body) = get(
            rig.port,
            &format!("/huddle-router/history?{query}"),
            "127.0.0.1",
        )
        .await;
        assert!(status.contains("400"), "{query}: {status}");
        assert_eq!(body.trim().lines().count(), 1, "{query}: one-line reason");
    }
    let (status, _) = get(rig.port, "/huddle-router/history?view=nope", "127.0.0.1").await;
    assert!(status.contains("404"), "{status}");
}

#[tokio::test]
async fn t6_a_missing_database_answers_empty_results() {
    let rig = rig(false).await;
    let b = json(&rig, "view=buckets").await;
    assert_eq!(b["buckets"].as_array().map(Vec::len), Some(0));
    let t = json(&rig, "view=totals&by=model").await;
    assert_eq!(t["totals"].as_array().map(Vec::len), Some(0));
    let s = json(&rig, "view=search").await;
    assert_eq!(s["total"], 0);
    assert_eq!(s["rows"].as_array().map(Vec::len), Some(0));
}

#[tokio::test]
async fn t6_a_dashboard_host_may_read_it_and_another_host_is_refused() {
    let rig = rig(true).await;
    let path = "/huddle-router/history?view=search";
    let (status, _) = get(rig.port, path, HOST).await;
    assert!(status.contains("200"), "{status}");
    let (status, _) = get(rig.port, path, "evil.example").await;
    assert!(status.contains("421"), "{status}");
    assert_eq!(
        rig.opened.load(Ordering::SeqCst),
        1,
        "the refused host opened nothing"
    );
}

#[tokio::test]
async fn t6_the_database_is_not_opened_until_a_history_request_comes() {
    let rig = rig(true).await;
    for path in ["/", "/huddle-router/state", "/huddle-router/health"] {
        let _ = get(rig.port, path, "127.0.0.1").await;
    }
    assert_eq!(rig.opened.load(Ordering::SeqCst), 0);
    let _ = json(&rig, "view=search").await;
    assert_eq!(rig.opened.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn t6_the_answer_holds_no_prompt_key_or_header_value() {
    let rig = rig(true).await;
    let (_, body) = get(rig.port, "/huddle-router/history?view=search", "127.0.0.1").await;
    let v: Value = serde_json::from_str(&body).expect("json");
    let keys: Vec<&String> = v["rows"][0].as_object().expect("row").keys().collect();
    for key in keys {
        assert!(
            !["prompt", "key", "authorization", "header", "body"]
                .iter()
                .any(|bad| key.contains(bad)),
            "{key}"
        );
    }
}

#[tokio::test]
async fn t9_buckets_narrow_to_a_session_or_a_project() {
    let rig = rig(true).await;
    let n = |v: &Value| -> u64 {
        v["buckets"]
            .as_array()
            .map(|all| all.iter().filter_map(|b| b["n"].as_u64()).sum())
            .unwrap_or(0)
    };
    let all = json(&rig, "view=buckets&range=30d").await;
    let s1 = json(&rig, "view=buckets&range=30d&session=s1").await;
    let proj = json(&rig, "view=buckets&range=30d&project=%2Fwork%2Fa%20b").await;
    let none = json(&rig, "view=buckets&range=30d&session=zzz").await;
    assert_eq!((n(&all), n(&s1), n(&proj), n(&none)), (4, 1, 1, 0));
    assert_eq!(s1["buckets"][0]["model"], "claude-opus-5-5");
}

#[tokio::test]
async fn d11_totals_flag_unpriced_models_and_price_cache_savings() {
    let rig = rig(true).await;
    let v = json(&rig, "view=totals&range=24h&by=day").await;
    let day = v["totals"][0].as_object().expect("a day");
    let saved = huddle_catalog::prices::price_of("claude-opus-5-5")
        .map(|p| p.input - p.cache_read)
        .unwrap_or(0.0);
    assert!((day["cache_saved_usd"].as_f64().unwrap_or(-1.0) - saved).abs() < 1e-9);
    let models = day["models"].as_array().expect("models");
    let opus = models
        .iter()
        .find(|m| m["model"] == "claude-opus-5-5")
        .expect("opus");
    assert_eq!(opus["priced"], true);
    assert!((opus["cache_saved_usd"].as_f64().unwrap_or(-1.0) - saved).abs() < 1e-9);
    let glm = models
        .iter()
        .find(|m| m["model"] == "glm-5.3")
        .expect("glm");
    assert_eq!(glm["cache_saved_usd"], 0.0);
    assert_eq!(day["priced"], true);
}

#[tokio::test]
async fn c1_totals_by_reason_count_each_provider_and_cause() {
    let rig = rig(true).await;
    let t = now();
    let mut quota = row(t - MIN, "glm-5.3", "zai", 429);
    quota.reason = Some("quota".into());
    let busy = row(t - MIN, "claude-opus-5-5", "anthropic", 529);
    let left = row(t - MIN, "glm-5.3", "zai", 499);
    History::open(&rig._home.root().join("huddle.db"))
        .expect("open")
        .insert(&[quota.clone(), quota, busy, left])
        .expect("insert");
    let v = json(&rig, "view=totals&by=reason&range=1h").await;
    assert_eq!(v["by"], "reason");
    let rows = v["totals"].as_array().expect("totals");
    let find = |lane: &str, status: u64| {
        rows.iter()
            .find(|r| r["lane"] == lane && r["status"] == status)
            .unwrap_or_else(|| panic!("no {lane} {status} in {v}"))
    };
    let quota = find("zai", 429);
    assert_eq!(quota["reason"], "quota");
    assert_eq!(quota["n"], 2);
    assert_eq!(find("anthropic", 529)["n"], 1);
    assert!(find("anthropic", 529)["reason"].is_null());
    assert_eq!(find("zai", 500)["n"], 1);
    assert!(
        rows.iter()
            .all(|r| r["status"] != 200 && r["status"] != 499),
        "only errors: {v}"
    );
}
