//! E1: after a handover the old router closes idle connections at once, lets a request in flight
//! finish for at most the drain limit, then exits.

use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::serve::{ServeOptions, ServeOutcome, run};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    sync::watch,
    task::JoinHandle,
};

type Lines = Arc<Mutex<Vec<String>>>;
type Run = JoinHandle<Result<ServeOutcome, huddle_daemon::serve::ServeError>>;

fn options(
    home: &TempHome,
    port: u16,
    takeover: bool,
    upstream: u16,
    drain_ms: &str,
) -> (ServeOptions, Lines) {
    let root = home.root().to_str().expect("utf8").to_string();
    let url = format!("http://127.0.0.1:{upstream}");
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("HUDDLE_ROUTER_ANTHROPIC_URL", url.as_str()),
        ("HUDDLE_ROUTER_DRAIN_LIMIT_MS", drain_ms),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let lines: Lines = Arc::default();
    let sink = Arc::clone(&lines);
    let opts = ServeOptions {
        env,
        paths,
        port,
        version: "test".into(),
        takeover,
        by: "test".into(),
        signal: Arc::default(),
        log: Arc::new(move |l| sink.lock().expect("lines").push(l)),
        output: None,
    };
    (opts, lines)
}

/// An upstream that reads each request and answers it after `delay`; `None` never answers.
async fn upstream(delay: Option<Duration>) -> u16 {
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    tokio::spawn(async move {
        loop {
            let Ok((mut stream, _)) = listener.accept().await else {
                return;
            };
            tokio::spawn(async move {
                let mut buffer = [0u8; 4096];
                let _ = stream.read(&mut buffer).await;
                let Some(delay) = delay else {
                    std::future::pending::<()>().await;
                    return;
                };
                tokio::time::sleep(delay).await;
                let body = r#"{"type":"message","content":[]}"#;
                let answer = format!(
                    "HTTP/1.1 200 OK\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = stream.write_all(answer.as_bytes()).await;
            });
        }
    });
    port
}

async fn health_on(stream: &mut TcpStream, port: u16) -> String {
    let request = format!("GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n\r\n");
    stream.write_all(request.as_bytes()).await.expect("write");
    let mut buffer = [0u8; 2048];
    let n = stream.read(&mut buffer).await.expect("read");
    String::from_utf8_lossy(&buffer[..n]).into_owned()
}

async fn message_request(port: u16) -> TcpStream {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let body = r#"{"model":"claude-test","max_tokens":1,"messages":[]}"#;
    let request = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\ncontent-type: application/json\r\ncontent-length: {}\r\n\r\n{body}",
        body.len()
    );
    stream.write_all(request.as_bytes()).await.expect("write");
    stream
}

async fn wait_for(lines: &Lines, needle: &str) {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if lines
            .lock()
            .expect("lines")
            .iter()
            .any(|line| line.contains(needle))
        {
            return;
        }
        assert!(Instant::now() < deadline, "never logged {needle}");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

async fn serving(port: u16) {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if let Ok(mut stream) = TcpStream::connect(("127.0.0.1", port)).await {
            let request = format!(
                "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
            );
            if stream.write_all(request.as_bytes()).await.is_ok() {
                let mut text = String::new();
                let _ = stream.read_to_string(&mut text).await;
                if text.contains("huddle-router") {
                    return;
                }
            }
        }
        assert!(Instant::now() < deadline, "the daemon never answered");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

struct Handover {
    old: Run,
    old_lines: Lines,
    taker_stop: watch::Sender<bool>,
    taker: Run,
}

/// Starts the old router, runs `during` against it, then starts a taker that takes its socket.
async fn handover_after<F, Fut, T>(
    home: &TempHome,
    port: u16,
    upstream_port: u16,
    drain_ms: &str,
    during: F,
) -> (Handover, T)
where
    F: FnOnce() -> Fut,
    Fut: std::future::Future<Output = T>,
{
    let (old_opts, old_lines) = options(home, port, false, upstream_port, drain_ms);
    let (old_stop, old_stop_rx) = watch::channel(false);
    std::mem::forget(old_stop);
    let old = tokio::spawn(run(old_opts, old_stop_rx));
    serving(port).await;
    let held = during().await;
    let (taker_opts, _taker_lines) = options(home, port, true, upstream_port, drain_ms);
    let (taker_stop, taker_stop_rx) = watch::channel(false);
    let taker = tokio::spawn(run(taker_opts, taker_stop_rx));
    wait_for(&old_lines, "handed over to pid").await;
    (
        Handover {
            old,
            old_lines,
            taker_stop,
            taker,
        },
        held,
    )
}

async fn finish(handover: Handover) {
    handover.taker_stop.send(true).expect("stop");
    handover.taker.await.expect("join").expect("taker");
}

#[tokio::test]
async fn e1_old_router_exits_after_handover_with_an_idle_connection() {
    let home = TempHome::new();
    let port = free_port();
    let upstream_port = upstream(Some(Duration::ZERO)).await;
    let (handover, mut idle) = handover_after(&home, port, upstream_port, "600000", || async {
        let mut idle = TcpStream::connect(("127.0.0.1", port))
            .await
            .expect("connect");
        assert!(health_on(&mut idle, port).await.contains("200"));
        idle
    })
    .await;
    let mut rest = Vec::new();
    let closed = tokio::time::timeout(Duration::from_secs(5), idle.read_to_end(&mut rest)).await;
    assert!(closed.is_ok(), "the idle connection closes at once");
    let ended = tokio::time::timeout(Duration::from_secs(5), handover.old).await;
    assert!(ended.is_ok(), "the old router is gone within 5 s");
    wait_for(&handover.old_lines, "exit pid=").await;
    finish(Handover {
        old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
        ..handover
    })
    .await;
}

#[tokio::test]
async fn e1_drain_limit_ends_a_stuck_request() {
    let home = TempHome::new();
    let port = free_port();
    let upstream_port = upstream(None).await;
    let (handover, mut stuck) = handover_after(&home, port, upstream_port, "300", || async {
        message_request(port).await
    })
    .await;
    let mut rest = Vec::new();
    let closed = tokio::time::timeout(Duration::from_secs(5), stuck.read_to_end(&mut rest)).await;
    assert!(closed.is_ok(), "the limit cuts the request off");
    let ended = tokio::time::timeout(Duration::from_secs(5), handover.old).await;
    assert!(ended.is_ok(), "the old router is gone within 5 s");
    wait_for(&handover.old_lines, "drain limit reached").await;
    wait_for(&handover.old_lines, "exit pid=").await;
    finish(Handover {
        old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
        ..handover
    })
    .await;
}

#[tokio::test]
async fn e1_a_request_that_ends_inside_the_limit_completes_on_the_old_router() {
    let home = TempHome::new();
    let port = free_port();
    let upstream_port = upstream(Some(Duration::from_millis(800))).await;
    let (handover, mut request) = handover_after(&home, port, upstream_port, "600000", || async {
        message_request(port).await
    })
    .await;
    assert!(
        !handover.old.is_finished(),
        "the old router waits for the request in flight"
    );
    let mut answer = String::new();
    let read = tokio::time::timeout(Duration::from_secs(5), request.read_to_string(&mut answer));
    assert!(read.await.is_ok(), "the answer arrives");
    assert!(
        answer.contains("200 OK"),
        "the old router answered: {answer}"
    );
    let ended = tokio::time::timeout(Duration::from_secs(5), handover.old).await;
    assert!(ended.is_ok(), "the old router exits once the answer is out");
    finish(Handover {
        old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
        ..handover
    })
    .await;
}
