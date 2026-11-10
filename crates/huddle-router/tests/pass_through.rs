use huddle_config::env::Env;
use huddle_router::anthropic::{PassCtx, PassEvent, PassWord, pass_through_handler};
use huddle_router::server::{BODY_LIMIT, ServerLimits, bind, serve};
use huddle_router::upstream::{FailureCode, IDLE_TIMEOUT, UpstreamClient, UpstreamConfig};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use rustls::RootCertStore;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

struct Rig {
    port: u16,
    upstream: Option<FakeUpstream>,
    target: String,
    notes: Arc<Mutex<Vec<String>>>,
    events: Arc<Mutex<Vec<(u16, u64, PassWord)>>>,
}

async fn rig(upstream: FakeUpstream, idle: Duration) -> Rig {
    let target = upstream.url();
    let client = Arc::new(UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle,
    }));
    let notes: Arc<Mutex<Vec<String>>> = Arc::default();
    let events: Arc<Mutex<Vec<(u16, u64, PassWord)>>> = Arc::default();
    let sink = notes.clone();
    let event_sink = events.clone();
    let ctx = Arc::new(PassCtx {
        client,
        target: target.parse().expect("target"),
        body_limit: BODY_LIMIT,
        note: Arc::new(move |l: &str| sink.lock().unwrap().push(l.to_string())),
        on_pass: Some(Arc::new(move |e: &PassEvent| {
            event_sink.lock().unwrap().push((e.status, e.bytes, e.word))
        })),
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(
            listener,
            pass_through_handler(ctx),
            ServerLimits::default(),
            rx,
        )
        .await
        .expect("serve")
    });
    Rig {
        port,
        upstream: Some(upstream),
        target,
        notes,
        events,
    }
}

async fn request(port: u16, head: &str) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

fn get(path_and_query: &str) -> String {
    format!(
        "GET {path_and_query} HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\nanthropic-version: 2023-06-01\r\n\r\n"
    )
}

async fn last_note(rig: &Rig) -> String {
    for _ in 0..200 {
        if let Some(line) = rig.notes.lock().unwrap().last().cloned() {
            return line;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    panic!("no log line");
}

#[tokio::test]
async fn a_request_reaches_anthropic_with_only_the_host_changed() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "models")).await;
    let rig = rig(upstream, IDLE_TIMEOUT).await;
    let resp = request(rig.port, &get("/v1/models?limit=2")).await;
    assert!(resp.contains("models"), "{resp}");
    let seen = &rig.upstream.as_ref().expect("up").requests()[0];
    assert_eq!(seen.path, "/v1/models?limit=2");
    assert!(
        seen.headers
            .iter()
            .any(|(n, v)| n.eq_ignore_ascii_case("anthropic-version") && v == "2023-06-01")
    );
    assert!(
        !seen
            .headers
            .iter()
            .any(|(n, _)| n.to_ascii_lowercase().starts_with("x-forwarded")
                || n.eq_ignore_ascii_case("via"))
    );
}

#[tokio::test]
async fn the_log_line_names_the_request_and_never_its_query() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "models")).await;
    let rig = rig(upstream, IDLE_TIMEOUT).await;
    request(rig.port, &get("/v1/models?secret=abc")).await;
    let line = last_note(&rig).await;
    assert!(line.starts_with("GET /v1/models host="), "{line}");
    assert!(
        line.contains(" \u{2192} anthropic-pass 200 bytes=6 ms="),
        "{line}"
    );
    assert!(!line.contains("secret"));
    assert_eq!(rig.events.lock().unwrap()[0].0, 200);
}

#[tokio::test]
async fn a_cut_stream_logs_the_cut() {
    let upstream = FakeUpstream::start(|_, _| {
        Step::sse_then_cut(vec!["data: a\n\n".into()], Duration::from_millis(5))
    })
    .await;
    let rig = rig(upstream, IDLE_TIMEOUT).await;
    request(rig.port, &get("/v1/anything")).await;
    let line = last_note(&rig).await;
    assert!(line.contains("anthropic-pass cut (EABORTED)"), "{line}");
    assert_eq!(
        rig.events.lock().unwrap()[0].2,
        PassWord::Cut(FailureCode::EAborted)
    );
}

#[tokio::test]
async fn an_unreachable_anthropic_is_502_with_no_code_in_the_body() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let mut rig = rig(upstream, IDLE_TIMEOUT).await;
    rig.upstream.take();
    tokio::time::sleep(Duration::from_millis(20)).await;
    let resp = request(rig.port, &get("/v1/models")).await;
    assert!(resp.starts_with("HTTP/1.1 502"), "{resp}");
    assert!(
        resp.contains(r#""message":"huddle-router router: anthropic unreachable""#),
        "{resp}"
    );
    let line = last_note(&rig).await;
    assert!(line.contains("anthropic-pass error "), "{line}");
    let _ = &rig.target;
}

#[tokio::test]
async fn an_upgrade_is_relayed_both_ways() {
    let upstream = FakeUpstream::start(|_, _| Step::upgrade_echo()).await;
    let rig = rig(upstream, IDLE_TIMEOUT).await;
    let mut s = TcpStream::connect(("127.0.0.1", rig.port))
        .await
        .expect("connect");
    s.write_all(b"GET /v1/ws HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
        .await
        .expect("head");
    let mut head = Vec::new();
    let mut byte = [0u8; 1];
    while !head.ends_with(b"\r\n\r\n") {
        s.read_exact(&mut byte).await.expect("read");
        head.push(byte[0]);
    }
    assert!(
        String::from_utf8_lossy(&head).starts_with("HTTP/1.1 101"),
        "{}",
        String::from_utf8_lossy(&head)
    );
    s.write_all(b"ping").await.expect("ping");
    let mut pong = [0u8; 4];
    tokio::time::timeout(Duration::from_secs(3), s.read_exact(&mut pong))
        .await
        .expect("timely")
        .expect("read");
    assert_eq!(&pong, b"ping");
}
