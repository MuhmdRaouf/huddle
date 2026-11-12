//! K18: the state snapshot carries the router's recent events and the Anthropic path's state.

use huddle_config::{env::Env, paths::Paths};
use huddle_router::dispatch::{Router, RouterConfig, Tuning};
use huddle_router::server::{ServerLimits, bind, serve};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use hyper::Uri;
use serde_json::Value;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::watch;

struct Rig {
    _home: TempHome,
    router: Arc<Router>,
    port: u16,
    sunk: Arc<Mutex<Vec<String>>>,
    _stop: watch::Sender<bool>,
}

async fn rig(anthropic: &str) -> Rig {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let port = free_port();
    let sunk: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = sunk.clone();
    let router = Router::with_tuning(
        RouterConfig {
            env,
            paths,
            port,
            anthropic_url: anthropic.parse::<Uri>().expect("uri"),
            log: Arc::new(move |l| sink.lock().unwrap().push(l)),
            history: None,
            dashboard: huddle_router::dashboard::DashboardInputs::default(),
        },
        &[],
        Tuning::default(),
    );
    let listener = bind(port).await.expect("bind");
    let (stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        _home: home,
        router,
        port,
        sunk,
        _stop: stop,
    }
}

async fn exchange(port: u16, request: String) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(request.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

fn post(port: u16) -> String {
    let body = r#"{"model":"claude-opus-5-5","messages":[]}"#;
    format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
}

async fn state(port: u16) -> Value {
    let answer = exchange(
        port,
        format!(
            "GET /huddle-router/state HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
        ),
    )
    .await;
    let (_, body) = answer.split_once("\r\n\r\n").expect("head");
    serde_json::from_str(body).expect("json")
}

async fn serve_ok(port: u16) {
    let listener = TcpListener::bind(("127.0.0.1", port)).await.expect("bind");
    tokio::spawn(async move {
        while let Ok((mut s, _)) = listener.accept().await {
            tokio::spawn(async move {
                let mut seen = Vec::new();
                let mut buf = [0u8; 1024];
                while !seen.windows(4).any(|w| w == b"\r\n\r\n") {
                    match s.read(&mut buf).await {
                        Ok(0) | Err(_) => return,
                        Ok(n) => seen.extend_from_slice(&buf[..n]),
                    }
                }
                let _ = s
                    .write_all(
                        b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok",
                    )
                    .await;
            });
        }
    });
}

#[tokio::test]
async fn k18_the_state_carries_router_events_and_path() {
    let dead = free_port();
    let r = rig(&format!("http://127.0.0.1:{dead}")).await;
    let quiet = state(r.port).await;
    assert_eq!(quiet["router"]["path"]["state"], "ok");
    assert_eq!(quiet["router"]["events"], serde_json::json!([]));

    for _ in 0..3 {
        exchange(r.port, post(r.port)).await;
    }
    let failing = state(r.port).await;
    let path = &failing["router"]["path"];
    assert_eq!(path["state"], "failing", "{path}");
    assert_eq!(path["code"], "ECONNREFUSED");
    assert_eq!(path["phase"], "connect");
    assert_eq!(path["peer"], format!("127.0.0.1:{dead}"));
    assert_eq!(path["failed"], 3);
    assert!(path["since_ms"].as_u64().unwrap() > 0);
    assert!(path["hint"].as_str().unwrap().contains("HTTPS_PROXY"));
    let events = failing["router"]["events"].as_array().expect("events");
    let last = events.last().expect("an event");
    assert_eq!(last["kind"], "failing");
    assert!(
        last["text"]
            .as_str()
            .unwrap()
            .starts_with("Anthropic path failing (ECONNREFUSED at connect"),
        "{last}"
    );
    assert!(last["at_ms"].as_u64().unwrap() > 0);

    serve_ok(dead).await;
    exchange(r.port, post(r.port)).await;
    let recovered = state(r.port).await;
    assert_eq!(recovered["router"]["path"]["state"], "ok");
    let back = &recovered["router"]["path"]["last_recovery"];
    assert_eq!(back["failed"], 3, "{back}");
    assert!(back["at_ms"].as_u64().unwrap() > 0);
    assert!(back["after_ms"].is_u64());
    let kinds: Vec<&str> = recovered["router"]["events"]
        .as_array()
        .unwrap()
        .iter()
        .map(|e| e["kind"].as_str().unwrap())
        .collect();
    assert_eq!(kinds.last(), Some(&"recovered"), "{kinds:?}");
}

#[tokio::test]
async fn k18_only_event_lines_reach_the_events_and_the_newest_twenty_stay() {
    let r = rig("http://127.0.0.1:9").await;
    let log = r.router.logger();
    log("POST /v1/messages 200 model=x".to_string());
    for n in 0..25 {
        log(format!("huddle-router: reload {n}"));
    }
    let events = state(r.port).await["router"]["events"].clone();
    let events = events.as_array().expect("events");
    assert_eq!(events.len(), 20);
    assert_eq!(events[0]["text"], "reload 5");
    assert_eq!(events[19]["text"], "reload 24");
    assert_eq!(events[19]["kind"], "event");
    assert!(r.sunk.lock().unwrap().iter().any(|l| l.starts_with("POST")));
}
