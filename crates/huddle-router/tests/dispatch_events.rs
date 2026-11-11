use huddle_config::{env::Env, paths::Paths};
use huddle_router::{
    dispatch::{Router, RouterConfig},
    events::EventSink,
    server::{ServerLimits, bind, serve},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    ports::free_port,
    temp_home::TempHome,
};
use hyper::Uri;
use std::{fs, sync::Arc, time::Duration};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::watch,
};

struct Rig {
    home: TempHome,
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
    let dir = paths.state_dir.join("events");
    let config = RouterConfig {
        env,
        paths,
        port,
        anthropic_url: anthropic.url().parse::<Uri>().expect("uri"),
        log: Arc::new(|_| {}),
        events: Some(EventSink::start(dir)),
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
    Rig { home, port, stop }
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

async fn events(home: &TempHome, want: usize) -> Vec<serde_json::Value> {
    let dir = home.root().join(".local/state/huddle/events");
    let deadline = std::time::Instant::now() + Duration::from_secs(3);
    loop {
        let mut lines: Vec<serde_json::Value> = Vec::new();
        if let Ok(entries) = fs::read_dir(&dir) {
            for entry in entries {
                let text = fs::read_to_string(entry.expect("entry").path()).expect("read");
                lines.extend(text.lines().map(|l| serde_json::from_str(l).expect("json")));
            }
        }
        if lines.len() >= want {
            return lines;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "only {} events",
            lines.len()
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

#[tokio::test]
async fn a_provider_request_writes_one_event_with_router_us_and_usage() {
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
    let lines = events(&r.home, 1).await;
    let event = &lines[0];
    assert_eq!(event["plugin"], "zai");
    assert_eq!(event["model"], "glm-5.3");
    assert_eq!(event["route"], "provider");
    assert_eq!(event["status"], 200);
    assert_eq!(event["usage"]["output_tokens"], 78);
    assert!(
        event["router_us"].as_u64().expect("router_us") >= 1,
        "{event}"
    );
    assert!(event["latency_ms"].as_u64().is_some());
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn a_claude_request_writes_its_own_event() {
    let zai = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let r = rig(anthropic, zai).await;
    post(r.port, r#"{"model":"claude-opus-5-5","messages":[]}"#).await;
    let lines = events(&r.home, 1).await;
    assert_eq!(lines[0]["plugin"], "huddle-router");
    assert_eq!(lines[0]["route"], "anthropic");
    assert_eq!(lines[0]["model"], "claude-opus-5-5");
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn a_refused_request_writes_route_refused_with_no_router_us() {
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
    let lines = events(&r.home, 1).await;
    assert_eq!(lines[0]["route"], "refused");
    assert_eq!(lines[0]["status"], 400);
    assert_eq!(lines[0]["error"], "message threads beta");
    assert!(lines[0].get("router_us").is_none());
    let _ = r.stop.send(true);
}
