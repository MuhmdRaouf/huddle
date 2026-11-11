use huddle_config::{env::Env, paths::Paths};
use huddle_router::{
    dashboard::DashboardInputs,
    dispatch::{Router, RouterConfig},
    server::{ServerLimits, bind, serve},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    temp_home::TempHome,
};
use hyper::Uri;
use serde_json::Value;
use std::{sync::Arc, time::Duration};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::watch,
};

struct Rig {
    _home: TempHome,
    port: u16,
    stop: watch::Sender<bool>,
}

/// A router with no event writer, so the live state is the only record of a request.
async fn rig(anthropic: FakeUpstream) -> Rig {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_API_KEY", "test-key-test-key"),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let router = Router::new(
        RouterConfig {
            env,
            paths,
            port,
            anthropic_url: anthropic.url().parse::<Uri>().expect("uri"),
            log: Arc::new(|_| {}),
            history: None,
            dashboard: DashboardInputs::default(),
        },
        &["zai".to_string()],
    );
    let (stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    std::mem::forget(anthropic);
    Rig {
        _home: home,
        port,
        stop,
    }
}

async fn send(port: u16, path: &str, extra: &str, body: &str) -> TcpStream {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let head = format!(
        "POST {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n{extra}Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    stream
}

async fn state(port: u16) -> Value {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    stream
        .write_all(
            format!(
                "GET /huddle-router/state HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
            )
            .as_bytes(),
        )
        .await
        .expect("write");
    let mut out = String::new();
    stream.read_to_string(&mut out).await.expect("read");
    let (_, body) = out.split_once("\r\n\r\n").expect("head");
    serde_json::from_str(body).expect("json")
}

async fn until(port: u16, what: &str, done: impl Fn(&Value) -> bool) -> Value {
    for _ in 0..100 {
        let snapshot = state(port).await;
        if done(&snapshot) {
            return snapshot;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("timed out waiting for {what}");
}

#[tokio::test]
async fn a_refused_request_is_listed_as_an_error_and_not_left_in_flight() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let r = rig(anthropic).await;
    let mut stream = send(
        r.port,
        "/v1/messages",
        "anthropic-beta: message-threads-2026-08-12\r\n",
        r#"{"model":"glm-5.3"}"#,
    )
    .await;
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");

    let snapshot = until(r.port, "the refused request", |s| {
        !s["recent"].as_array().expect("recent").is_empty()
    })
    .await;

    assert_eq!(snapshot["recent"][0]["status"], 400);
    assert_eq!(snapshot["recent"][0]["lane"], "zai");
    assert!(
        snapshot["in_flight"]
            .as_array()
            .expect("in_flight")
            .is_empty()
    );
    assert_eq!(snapshot["errors_5m"], 1);
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn a_client_that_leaves_mid_answer_ends_as_client_left() {
    let chunk = "event: x\ndata: {\"type\":\"ping\"}\n\n".to_string();
    let anthropic = FakeUpstream::start(move |_, _| {
        Step::sse(vec![chunk.clone(); 20], Duration::from_millis(200))
    })
    .await;
    let r = rig(anthropic).await;
    let stream = send(r.port, "/v1/messages", "", r#"{"model":"claude-opus-5-5"}"#).await;
    until(r.port, "the request in flight", |s| {
        !s["in_flight"].as_array().expect("in_flight").is_empty()
    })
    .await;

    drop(stream);

    let snapshot = until(r.port, "the request to end", |s| {
        s["in_flight"].as_array().expect("in_flight").is_empty()
    })
    .await;
    assert_eq!(snapshot["recent"][0]["status"], 499);
    assert_eq!(snapshot["recent"][0]["reason"], "client left");
    assert_eq!(snapshot["errors_5m"], 0);
    let _ = r.stop.send(true);
}

fn header(name: &str, value: &str) -> (String, String) {
    (name.to_string(), value.to_string())
}

async fn ask_claude(r: &Rig) {
    let mut stream = send(r.port, "/v1/messages", "", r#"{"model":"claude-opus-5-5"}"#).await;
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");
}

#[tokio::test]
async fn an_anthropic_answer_fills_the_request_row_the_plan_and_the_model_stats() {
    let body = r#"{"id":"m","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}"#;
    let anthropic = FakeUpstream::start(move |_, _| {
        Step::answer_with_headers(
            200,
            vec![
                header("content-type", "application/json"),
                header("request-id", "req_011CVabc"),
                header("anthropic-organization-id", "org-secret-value"),
                header("anthropic-ratelimit-unified-5h-utilization", "0.62"),
                header("anthropic-ratelimit-unified-5h-reset", "1794303600"),
                header("anthropic-ratelimit-unified-7d-utilization", "0.41"),
                header("anthropic-ratelimit-unified-7d-reset", "1794800000"),
            ],
            body,
        )
    })
    .await;
    let r = rig(anthropic).await;
    ask_claude(&r).await;

    let snapshot = until(r.port, "the request", |s| {
        !s["recent"].as_array().expect("recent").is_empty()
    })
    .await;

    let row = &snapshot["recent"][0];
    assert_eq!(row["upstream_id"], "req_011CVabc");
    assert_eq!(row["stop"], "end_turn");
    assert_eq!(row["cache_read"], 5);
    assert_eq!(row["cache_write"], 2);
    assert_eq!(row["tokens_in"], 19);
    assert_eq!(row["tokens_out"], 34);
    assert!(row["dur_ms"].as_u64().is_some());
    assert!(row["router_ms"].as_u64().is_some());
    assert!(row["agent"].is_null());
    assert_eq!(
        snapshot["plan"],
        serde_json::json!({
            "five_hour_pct": 62,
            "five_hour_reset_ms": 1_794_303_600_000u64,
            "week_pct": 41,
            "week_reset_ms": 1_794_800_000_000u64
        })
    );
    let model = &snapshot["models"][0];
    assert_eq!(model["model"], "claude-opus-5-5");
    assert_eq!(model["lane"], "anthropic");
    assert_eq!(model["requests"], 1);
    assert_eq!(model["tokens_out"], 34);
    let buckets = snapshot["buckets"]["models"]["claude-opus-5-5"]
        .as_array()
        .expect("buckets");
    assert_eq!(buckets.len(), 30);
    assert_eq!(
        buckets
            .iter()
            .map(|b| b["n"].as_u64().unwrap_or(0))
            .sum::<u64>(),
        1
    );
    assert_eq!(snapshot["lanes"][0]["last"]["status"], 200);
    assert!(!snapshot.to_string().contains("org-secret-value"));
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn an_answer_without_the_headers_leaves_the_id_and_plan_null() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    ask_claude(&r).await;

    let snapshot = until(r.port, "the request", |s| {
        !s["recent"].as_array().expect("recent").is_empty()
    })
    .await;

    assert!(snapshot["recent"][0]["upstream_id"].is_null());
    assert!(snapshot["plan"].is_null());
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn a_request_id_that_is_not_a_plain_id_is_dropped() {
    let anthropic = FakeUpstream::start(|_, _| {
        Step::answer_with_headers(200, vec![header("request-id", "bad id\twith space")], "{}")
    })
    .await;
    let r = rig(anthropic).await;
    ask_claude(&r).await;

    let snapshot = until(r.port, "the request", |s| {
        !s["recent"].as_array().expect("recent").is_empty()
    })
    .await;

    assert!(snapshot["recent"][0]["upstream_id"].is_null());
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn open_live_feeds_are_the_router_viewers() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    assert_eq!(state(r.port).await["router"]["viewers"], 0);

    let mut feed = TcpStream::connect(("127.0.0.1", r.port))
        .await
        .expect("connect");
    feed.write_all(
        format!(
            "GET /huddle-router/live HTTP/1.1\r\nHost: 127.0.0.1:{}\r\n\r\n",
            r.port
        )
        .as_bytes(),
    )
    .await
    .expect("write");
    let open = until(r.port, "the feed to open", |s| s["router"]["viewers"] == 1).await;
    assert!(open["router"]["connections"].is_u64());

    drop(feed);
    until(r.port, "the feed to close", |s| s["router"]["viewers"] == 0).await;
    let _ = r.stop.send(true);
}
