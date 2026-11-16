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
        install_mark: None,
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

/// Runs `body` with a fresh home on a fresh current-thread runtime, and removes the home only
/// once the runtime is gone. A takeover leaves a blocking task that polls for the old router's
/// lock and creates the lock file when it gets it; dropping the runtime waits for that task, so
/// nothing the test started can write into the home after it is removed.
fn in_home(body: impl AsyncFnOnce(&TempHome)) {
    let home = TempHome::new();
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("runtime");
    runtime.block_on(body(&home));
    drop(runtime);
    drop(home);
}

#[test]
fn l68_a_router_started_just_before_the_drop_leaves_nothing() {
    let mut roots = Vec::new();
    for _ in 0..30 {
        in_home(async |home: &TempHome| {
            roots.push(home.root().to_path_buf());
            let port = free_port();
            let upstream_port = upstream(None).await;
            // A request stuck in the old router's drain keeps its lock held, so the taker's lock
            // wait is still polling when the body ends.
            let (_handover, _stuck) =
                handover_after(home, port, upstream_port, "600000", || async {
                    message_request(port).await
                })
                .await;
        });
    }
    let left: Vec<_> = roots.iter().filter(|root| root.exists()).collect();
    assert!(left.is_empty(), "{} of 30 homes left: {left:?}", left.len());
}

#[test]
fn l70_the_lock_wait_stops_when_its_run_is_gone() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(None).await;
        let run_dir = home.root().join(".local/state/huddle/run");
        // A request stuck in the old router's drain keeps its lock held for 300 ms.
        let (handover, _stuck) = handover_after(home, port, upstream_port, "300", || async {
            message_request(port).await
        })
        .await;
        let old = handover.old;
        finish(Handover {
            old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
            ..handover
        })
        .await;
        assert!(
            run_dir.join("router.lock").exists(),
            "the old router's lock file"
        );
        std::fs::remove_dir_all(&run_dir).expect("remove the run folder");
        let ended = tokio::time::timeout(Duration::from_secs(5), old).await;
        assert!(ended.is_ok(), "the old router frees its lock");
        tokio::time::sleep(Duration::from_millis(300)).await;
        assert!(
            !run_dir.exists(),
            "the lock wait of an ended run created the run folder again"
        );
    });
}

#[test]
fn e1_old_router_exits_after_handover_with_an_idle_connection() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(Some(Duration::ZERO)).await;
        let (handover, mut idle) = handover_after(home, port, upstream_port, "600000", || async {
            let mut idle = TcpStream::connect(("127.0.0.1", port))
                .await
                .expect("connect");
            assert!(health_on(&mut idle, port).await.contains("200"));
            idle
        })
        .await;
        let mut rest = Vec::new();
        let closed =
            tokio::time::timeout(Duration::from_secs(5), idle.read_to_end(&mut rest)).await;
        assert!(closed.is_ok(), "the idle connection closes at once");
        let ended = tokio::time::timeout(Duration::from_secs(5), handover.old).await;
        assert!(ended.is_ok(), "the old router is gone within 5 s");
        wait_for(&handover.old_lines, "exit pid=").await;
        finish(Handover {
            old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
            ..handover
        })
        .await;
    });
}

#[test]
fn e1_drain_limit_ends_a_stuck_request() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(None).await;
        let (handover, mut stuck) = handover_after(home, port, upstream_port, "300", || async {
            message_request(port).await
        })
        .await;
        let mut rest = Vec::new();
        let closed =
            tokio::time::timeout(Duration::from_secs(5), stuck.read_to_end(&mut rest)).await;
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
    });
}

#[test]
fn e1_a_request_that_ends_inside_the_limit_completes_on_the_old_router() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(Some(Duration::from_millis(800))).await;
        let (handover, mut request) =
            handover_after(home, port, upstream_port, "600000", || async {
                message_request(port).await
            })
            .await;
        assert!(
            !handover.old.is_finished(),
            "the old router waits for the request in flight"
        );
        let mut answer = String::new();
        let read =
            tokio::time::timeout(Duration::from_secs(5), request.read_to_string(&mut answer));
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
    });
}

fn logged(lines: &Lines, needle: &str) -> Option<String> {
    lines
        .lock()
        .expect("lines")
        .iter()
        .find(|line| line.contains(needle))
        .cloned()
}

#[test]
fn l19_a_drain_logs_its_counts_at_the_start() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(None).await;
        let (handover, stuck) = handover_after(home, port, upstream_port, "300", || async {
            message_request(port).await
        })
        .await;
        wait_for(&handover.old_lines, "drain starts").await;
        let line = logged(&handover.old_lines, "drain starts").expect("start line");
        assert_eq!(
            line,
            "huddle-router: drain starts; 0 tunnels and 1 connections open"
        );
        drop(stuck);
        let _ = tokio::time::timeout(Duration::from_secs(5), handover.old).await;
        finish(Handover {
            old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
            ..handover
        })
        .await;
    });
}

#[test]
fn l19_a_drain_that_hits_the_limit_names_what_stayed_open() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(None).await;
        let (handover, _stuck) = handover_after(home, port, upstream_port, "300", || async {
            message_request(port).await
        })
        .await;
        wait_for(&handover.old_lines, "drain limit reached").await;
        let line = logged(&handover.old_lines, "drain limit reached").expect("end line");
        assert_eq!(
            line,
            "huddle-router: drain limit reached; open requests cut; 0 tunnels and 1 connections still open"
        );
        let _ = tokio::time::timeout(Duration::from_secs(5), handover.old).await;
        finish(Handover {
            old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
            ..handover
        })
        .await;
    });
}

#[test]
fn l19_a_drain_with_nothing_open_logs_how_long_it_took() {
    in_home(async |home: &TempHome| {
        let port = free_port();
        let upstream_port = upstream(Some(Duration::ZERO)).await;
        let (handover, _) = handover_after(home, port, upstream_port, "600000", || async {}).await;
        wait_for(&handover.old_lines, "drained in").await;
        let line = logged(&handover.old_lines, "drained in").expect("end line");
        assert!(
            line.starts_with("huddle-router: drained in ") && line.ends_with(" ms"),
            "{line}"
        );
        finish(Handover {
            old: tokio::spawn(async { Ok(ServeOutcome::Stopped) }),
            ..handover
        })
        .await;
    });
}
