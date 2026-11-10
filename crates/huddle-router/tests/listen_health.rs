use huddle_router::health::{HEALTH_PATH, health_answer};
use huddle_router::server::{Handler, ServerLimits, bind, serve};
use hyper::{Request, Response, StatusCode};
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

fn health_only(providers: Vec<String>) -> Handler {
    Arc::new(move |req: Request<hyper::body::Incoming>| {
        let providers = providers.clone();
        Box::pin(async move {
            if req.uri().path() == HEALTH_PATH {
                health_answer(&providers)
            } else {
                let mut r = Response::new(huddle_router::server::empty_body());
                *r.status_mut() = StatusCode::NOT_FOUND;
                r
            }
        })
    })
}

async fn start(handler: Handler, limits: ServerLimits) -> u16 {
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move { serve(listener, handler, limits, rx).await.expect("serve") });
    port
}

async fn get(port: u16, path: &str) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(
        format!("GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n")
            .as_bytes(),
    )
    .await
    .expect("write");
    let mut out = Vec::new();
    s.read_to_end(&mut out).await.expect("read");
    String::from_utf8_lossy(&out).into_owned()
}

fn body_of(resp: &str) -> &str {
    resp.split("\r\n\r\n").nth(1).expect("a body")
}

#[tokio::test]
async fn the_health_answer_names_the_router_and_its_providers() {
    let port = start(
        health_only(vec!["zai".into(), "kimi".into()]),
        ServerLimits::default(),
    )
    .await;
    let resp = get(port, HEALTH_PATH).await;
    assert!(resp.starts_with("HTTP/1.1 200"), "{resp}");
    assert!(
        resp.to_lowercase()
            .contains("content-type: application/json"),
        "{resp}"
    );
    assert_eq!(
        body_of(&resp),
        r#"{"ok":true,"name":"huddle-router","providers":["zai","kimi"],"components":[]}"#
    );
}

#[tokio::test]
async fn no_provider_is_an_empty_list_never_null() {
    let port = start(health_only(vec![]), ServerLimits::default()).await;
    assert_eq!(
        body_of(&get(port, HEALTH_PATH).await),
        r#"{"ok":true,"name":"huddle-router","providers":[],"components":[]}"#
    );
}

#[tokio::test]
async fn the_listener_binds_loopback_only() {
    let listener = bind(0).await.expect("bind");
    assert!(listener.local_addr().expect("addr").ip().is_loopback());
}

#[tokio::test]
async fn a_keep_alive_connection_serves_a_second_request() {
    let port = start(health_only(vec![]), ServerLimits::default()).await;
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    for i in 0..2 {
        s.write_all(
            format!("GET {HEALTH_PATH} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n\r\n").as_bytes(),
        )
        .await
        .expect("write");
        let mut buf = [0u8; 4096];
        let n = tokio::time::timeout(Duration::from_secs(2), s.read(&mut buf))
            .await
            .expect("timely")
            .expect("read");
        assert!(
            String::from_utf8_lossy(&buf[..n]).contains(r#""ok":true"#),
            "request {i}"
        );
    }
}

#[tokio::test]
async fn a_client_that_never_finishes_its_header_is_cut_off() {
    let port = start(
        health_only(vec![]),
        ServerLimits {
            header_read: Duration::from_millis(150),
        },
    )
    .await;
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(b"GET /huddle-router/hea").await.expect("write");
    let mut buf = [0u8; 16];
    let n = tokio::time::timeout(Duration::from_secs(3), s.read(&mut buf))
        .await
        .expect("closed within the limit")
        .expect("read");
    assert_eq!(n, 0, "the connection closes with no answer");
}

#[tokio::test]
async fn a_stop_signal_ends_the_server() {
    let listener = bind(0).await.expect("bind");
    let (tx, rx) = watch::channel(false);
    let task = tokio::spawn(async move {
        serve(listener, health_only(vec![]), ServerLimits::default(), rx).await
    });
    tx.send(true).expect("signal");
    tokio::time::timeout(Duration::from_secs(2), task)
        .await
        .expect("timely")
        .expect("join")
        .expect("serve ended ok");
}
