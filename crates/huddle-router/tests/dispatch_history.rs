use huddle_config::{env::Env, paths::Paths};
use huddle_router::{
    dispatch::{Router, RouterConfig},
    live::HistorySink,
    server::{ServerLimits, bind, serve},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    ports::free_port,
    temp_home::TempHome,
};
use hyper::Uri;
use std::{
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::watch,
};

#[derive(Default)]
struct Collect {
    rows: Mutex<Vec<huddle_history::Row>>,
}

impl HistorySink for Collect {
    fn finished(&self, row: huddle_history::Row) {
        self.rows.lock().unwrap().push(row);
    }

    fn credited(&self, _credits: Vec<huddle_history::Credit>) {}
}

struct Rig {
    rows: Arc<Collect>,
    port: u16,
    stop: watch::Sender<bool>,
}

async fn rig(anthropic: FakeUpstream, zai: FakeUpstream) -> Rig {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let zai_url = zai.url();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_ROUTER_URL", zai_url.as_str()),
        ("ZAI_API_KEY", "test-key-test-key"),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let port = free_port();
    let rows = Arc::new(Collect::default());
    let config = RouterConfig {
        env,
        paths,
        port,
        anthropic_url: anthropic.url().parse::<Uri>().expect("uri"),
        log: Arc::new(|_| {}),
        history: Some(rows.clone()),
        dashboard: huddle_router::dashboard::DashboardInputs::default(),
    };
    let router = Router::new(config, &["zai".to_string()]);
    let listener = bind(port).await.expect("bind");
    let (stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    std::mem::forget(anthropic);
    std::mem::forget(zai);
    Rig { rows, port, stop }
}

async fn post(port: u16, body: &str) {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let head = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");
}

async fn rows(rig: &Rig, want: usize) -> Vec<huddle_history::Row> {
    let deadline = std::time::Instant::now() + Duration::from_secs(3);
    loop {
        let rows = rig.rows.rows.lock().unwrap().clone();
        if rows.len() >= want {
            return rows;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "only {} rows",
            rows.len()
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

#[tokio::test]
async fn t5_a_provider_request_reaches_the_history_with_its_usage() {
    let sse = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":78}}\n\n";
    let zai = FakeUpstream::start(move |_, _| {
        Step::answer_with_headers(
            200,
            vec![("content-type".to_string(), "text/event-stream".to_string())],
            sse,
        )
    })
    .await;
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let r = rig(anthropic, zai).await;
    post(r.port, r#"{"model":"glm-5.3","messages":[]}"#).await;
    let rows = rows(&r, 1).await;
    let row = &rows[0];
    assert_eq!(row.lane, "zai");
    assert_eq!(row.model, "glm-5.3");
    assert_eq!(row.status, 200);
    assert_eq!(row.tokens_out, Some(78));
    assert_eq!(row.tokens_in, Some(12));
    assert!(row.at_ms > 0);
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn t5_a_claude_request_reaches_the_history_on_the_anthropic_lane() {
    let zai = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let r = rig(anthropic, zai).await;
    post(r.port, r#"{"model":"claude-opus-5-5","messages":[]}"#).await;
    let rows = rows(&r, 1).await;
    assert_eq!(rows[0].lane, "anthropic");
    assert_eq!(rows[0].model, "claude-opus-5-5");
    assert_eq!(rows[0].status, 200);
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn t5_a_refused_request_reaches_the_history_with_its_reason() {
    let zai = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let r = rig(anthropic, zai).await;
    let mut stream = TcpStream::connect(("127.0.0.1", r.port))
        .await
        .expect("connect");
    let body = r#"{"model":"glm-5.3"}"#;
    let head = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nanthropic-beta: message-threads-2026-08-12\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        r.port,
        body.len()
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");
    let rows = rows(&r, 1).await;
    assert_eq!(rows[0].lane, "zai");
    assert_eq!(rows[0].status, 400);
    assert!(rows[0].reason.is_some(), "{:?}", rows[0]);
    let _ = r.stop.send(true);
}
