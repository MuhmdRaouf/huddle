//! K16: a failed upstream attempt names its phase, its peer and its connection; the failing and
//! recovered lines name where and how long; probes run only while the path fails.

use huddle_config::{env::Env, paths::Paths};
use huddle_router::anthropic::{PassCtx, pass_through_handler};
use huddle_router::dispatch::{ProbeTiming, Router, RouterConfig, Tuning};
use huddle_router::server::{BODY_LIMIT, ServerLimits, bind, serve};
use huddle_router::upstream::{UpstreamClient, UpstreamConfig};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    ports::free_port,
    temp_home::TempHome,
};
use hyper::Uri;
use rustls::RootCertStore;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::watch;

type Lines = Arc<Mutex<Vec<String>>>;

async fn exchange(port: u16, request: String) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(request.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

fn get(path: &str) -> String {
    format!("GET {path} HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n")
}

fn post(port: u16) -> String {
    let body = r#"{"model":"claude-opus-5-5","messages":[]}"#;
    format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
}

/// A pass-through to `target`, with its log lines collected.
async fn pass_rig(target: String, idle: Duration) -> (u16, Lines) {
    let client = UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle,
    });
    let notes: Lines = Arc::default();
    let sink = notes.clone();
    let ctx = Arc::new(PassCtx {
        client: Arc::new(client),
        target: target.parse().expect("target"),
        body_limit: BODY_LIMIT,
        note: Arc::new(move |l: &str| sink.lock().unwrap().push(l.to_string())),
        on_outcome: None,
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        let _keep = _tx;
        serve(
            listener,
            pass_through_handler(ctx),
            ServerLimits::default(),
            rx,
        )
        .await
        .expect("serve")
    });
    (port, notes)
}

async fn wait_line(lines: &Lines, needle: &str) -> String {
    for _ in 0..400 {
        if let Some(line) = lines.lock().unwrap().iter().find(|l| l.contains(needle)) {
            return line.clone();
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("no line with {needle}: {:?}", lines.lock().unwrap());
}

#[tokio::test]
async fn k16_a_failed_connect_names_the_phase_and_the_peer() {
    let dead = free_port();
    let (port, notes) = pass_rig(format!("http://127.0.0.1:{dead}"), Duration::from_secs(5)).await;
    exchange(port, get("/v1/code/sessions/x/worker/events/stream")).await;
    let line = wait_line(&notes, "anthropic-pass error").await;
    assert!(line.contains("error ECONNREFUSED ms="), "{line}");
    assert!(
        line.contains(&format!(
            "phase=connect peer=127.0.0.1:{dead} conn=fresh dns_ms="
        )),
        "{line}"
    );
}

#[tokio::test]
async fn k16_a_server_that_never_answers_names_the_headers_phase() {
    let upstream = FakeUpstream::start(|_, _| Step::ReadAllThenHold).await;
    let (port, notes) = pass_rig(upstream.url(), Duration::from_millis(200)).await;
    exchange(port, get("/v1/x")).await;
    let line = wait_line(&notes, "anthropic-pass error").await;
    assert!(
        line.contains(&format!(
            "phase=headers peer=127.0.0.1:{} conn=fresh dns_ms=",
            upstream.port()
        )),
        "{line}"
    );
    assert!(line.contains("connect_ms="), "{line}");
}

struct RouterRig {
    _home: TempHome,
    port: u16,
    lines: Lines,
    _stop: watch::Sender<bool>,
}

async fn router_rig(anthropic: &str, tuning: Tuning) -> RouterRig {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let port = free_port();
    let lines: Lines = Arc::default();
    let sink = lines.clone();
    let config = RouterConfig {
        env,
        paths,
        port,
        anthropic_url: anthropic.parse::<Uri>().expect("uri"),
        log: Arc::new(move |l| sink.lock().unwrap().push(l)),
        history: None,
        dashboard: huddle_router::dashboard::DashboardInputs::default(),
    };
    let router = Router::with_tuning(config, &[], tuning);
    let listener = bind(port).await.expect("bind");
    let (stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    RouterRig {
        _home: home,
        port,
        lines,
        _stop: stop,
    }
}

#[tokio::test]
async fn k16_the_model_path_logs_code_and_phase_on_a_504() {
    let upstream = FakeUpstream::start(|_, _| Step::ReadAllThenHold).await;
    let tuning = Tuning {
        idle: Duration::from_millis(200),
        ..Tuning::default()
    };
    let r = router_rig(&upstream.url(), tuning).await;
    let answer = exchange(r.port, post(r.port)).await;
    assert!(answer.starts_with("HTTP/1.1 504"), "{answer}");
    let line = wait_line(&r.lines, "-> anthropic 504").await;
    assert!(line.contains("ETIMEDOUT"), "{line}");
    assert!(line.contains("phase=headers peer=127.0.0.1:"), "{line}");
}

/// Answers every request 200 on `port`, once something listens there.
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
                tokio::time::sleep(Duration::from_millis(30)).await;
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
async fn k16_the_failing_line_names_phase_and_peer_and_the_recovered_line_the_duration_and_count() {
    let dead = free_port();
    let r = router_rig(&format!("http://127.0.0.1:{dead}"), Tuning::default()).await;
    for _ in 0..3 {
        exchange(r.port, post(r.port)).await;
    }
    let failing = wait_line(&r.lines, "Anthropic path failing").await;
    assert!(
        failing.contains(&format!(
            "huddle-router: Anthropic path failing (ECONNREFUSED at connect to 127.0.0.1:{dead}): "
        )),
        "{failing}"
    );
    serve_ok(dead).await;
    exchange(r.port, post(r.port)).await;
    let recovered = wait_line(&r.lines, "Anthropic path recovered").await;
    assert!(
        recovered.contains("recovered after ")
            && recovered.ends_with(&format!(
                "; 3 requests failed; now via 127.0.0.1:{dead} (fresh)"
            )),
        "{recovered}"
    );
}

#[tokio::test]
async fn k16_probes_run_only_while_failing_and_stop_on_recovery() {
    let dead = free_port();
    let tuning = Tuning {
        probe: ProbeTiming {
            interval: Duration::from_millis(50),
            connect_limit: Duration::from_millis(500),
        },
        ..Tuning::default()
    };
    let r = router_rig(&format!("http://127.0.0.1:{dead}"), tuning).await;
    let probes = |lines: &Lines| {
        lines
            .lock()
            .unwrap()
            .iter()
            .filter(|l| l.contains("huddle-router: probe "))
            .count()
    };
    exchange(r.port, post(r.port)).await;
    exchange(r.port, post(r.port)).await;
    tokio::time::sleep(Duration::from_millis(300)).await;
    assert_eq!(probes(&r.lines), 0, "no probe while healthy");
    exchange(r.port, post(r.port)).await;
    let line = wait_line(&r.lines, "huddle-router: probe ").await;
    assert!(
        line.contains("probe 127.0.0.1 dns=ok(") && line.contains("connect=error(ECONNREFUSED"),
        "{line}"
    );
    serve_ok(dead).await;
    exchange(r.port, post(r.port)).await;
    wait_line(&r.lines, "Anthropic path recovered").await;
    tokio::time::sleep(Duration::from_millis(200)).await;
    let settled = probes(&r.lines);
    tokio::time::sleep(Duration::from_millis(400)).await;
    assert_eq!(probes(&r.lines), settled, "no probe after recovery");
}
