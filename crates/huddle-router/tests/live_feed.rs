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
            events: None,
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
