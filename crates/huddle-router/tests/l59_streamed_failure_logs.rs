//! L59: an Anthropic-bound upload that streams through a decrypted tunnel and meets a dead
//! upstream is answered 502 and leaves a router log line naming the request, the way the
//! whole-body route does.

use huddle_config::{env::Env, paths::Paths};
use huddle_pki::ca::{ROUTER_HOST, load_or_create};
use huddle_router::dispatch::{Router, RouterConfig};
use huddle_router::server::{ServerLimits, bind, serve};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use rustls::pki_types::{CertificateDer, ServerName};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;
use tokio_rustls::TlsConnector;

fn tls_client(ca_der: &[u8]) -> TlsConnector {
    let mut roots = rustls::RootCertStore::empty();
    roots
        .add(CertificateDer::from(ca_der.to_vec()))
        .expect("root");
    let mut config = rustls::ClientConfig::builder_with_provider(Arc::new(
        rustls::crypto::ring::default_provider(),
    ))
    .with_safe_default_protocol_versions()
    .expect("versions")
    .with_root_certificates(roots)
    .with_no_client_auth();
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    TlsConnector::from(Arc::new(config))
}

async fn connect_tunnel(port: u16, target: &str) -> TcpStream {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(format!("CONNECT {target} HTTP/1.1\r\nHost: {target}\r\n\r\n").as_bytes())
        .await
        .expect("write");
    let mut head = Vec::new();
    let mut byte = [0u8; 1];
    while !head.ends_with(b"\r\n\r\n") {
        s.read_exact(&mut byte).await.expect("read head");
        head.push(byte[0]);
    }
    s
}

/// A body far past the 64 KiB head the router reads before it relays.
fn streamed_post() -> String {
    let pad = "x".repeat(300 * 1024);
    let body = format!(r#"{{"model":"claude-opus-5-5","messages":[],"pad":"{pad}"}}"#);
    format!(
        "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    )
}

type Lines = Arc<Mutex<Vec<String>>>;

struct Rig {
    port: u16,
    ca_der: Vec<u8>,
    lines: Lines,
    _home: TempHome,
    _stop: watch::Sender<bool>,
}

/// A router whose Anthropic origin is `anthropic_url`, serving on a free port; its log lines
/// are collected.
async fn rig(anthropic_url: &str) -> Rig {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let ca = load_or_create(&paths.ca_dir, SystemTime::now()).expect("ca");
    let port = free_port();
    let lines: Lines = Arc::default();
    let sink = lines.clone();
    let router = Router::new(
        RouterConfig {
            env,
            paths,
            port,
            anthropic_url: anthropic_url.parse().expect("uri"),
            log: Arc::new(move |l| sink.lock().unwrap().push(l)),
            history: None,
            dashboard: huddle_router::dashboard::DashboardInputs::default(),
        },
        &[],
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
        port,
        ca_der: ca.cert_der().to_vec(),
        lines,
        _home: home,
        _stop: stop,
    }
}

fn dead_url() -> String {
    format!("http://127.0.0.1:{}", free_port())
}

/// Sends `request` through a decrypted tunnel; the router's whole answer.
async fn through_tunnel(rig: &Rig, request: &str) -> String {
    let tcp = connect_tunnel(rig.port, &format!("{ROUTER_HOST}:443")).await;
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    let mut tls = tls_client(&rig.ca_der)
        .connect(name, tcp)
        .await
        .expect("handshake");
    tls.write_all(request.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), tls.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

/// Sends `request` straight to the router's port: a small body takes the whole-body route.
async fn direct(rig: &Rig, request: &str) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", rig.port))
        .await
        .expect("connect");
    s.write_all(request.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

fn small_post(port: u16) -> String {
    let body = r#"{"model":"claude-opus-5-5","messages":[]}"#;
    format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    )
}

/// The lines that name the request, after the router has had time to write them.
async fn request_lines(rig: &Rig) -> Vec<String> {
    tokio::time::sleep(Duration::from_millis(300)).await;
    named_lines(rig)
}

/// The lines that name the request, as written so far.
fn named_lines(rig: &Rig) -> Vec<String> {
    rig.lines
        .lock()
        .unwrap()
        .iter()
        .filter(|l| l.contains("POST /v1/messages"))
        .cloned()
        .collect()
}

#[tokio::test]
async fn l59_a_failed_streamed_upload_leaves_a_log_line() {
    let rig = rig(&dead_url()).await;
    let answer = through_tunnel(&rig, &streamed_post()).await;
    assert!(answer.starts_with("HTTP/1.1 502"), "{answer}");
    let lines = request_lines(&rig).await;
    assert!(
        lines.iter().any(|l| l.contains("502")),
        "the 502 left no line naming the request: {lines:?}"
    );
}

#[tokio::test]
async fn l61_a_streamed_failure_logs_one_line_in_the_whole_body_shape() {
    let rig = rig(&dead_url()).await;
    through_tunnel(&rig, &streamed_post()).await;
    let lines = request_lines(&rig).await;
    assert_eq!(lines.len(), 1, "{lines:?}");
    assert!(
        lines[0]
            .contains("model=claude-opus-5-5 -> anthropic 502 error ECONNREFUSED phase=connect"),
        "{lines:?}"
    );
}

#[tokio::test]
async fn l61_a_whole_body_failure_logs_one_line() {
    let rig = rig(&dead_url()).await;
    let answer = direct(&rig, &small_post(rig.port)).await;
    assert!(answer.starts_with("HTTP/1.1 502"), "{answer}");
    let lines = request_lines(&rig).await;
    assert_eq!(lines.len(), 1, "{lines:?}");
    assert!(
        lines[0].contains("-> anthropic 502 error ECONNREFUSED"),
        "{lines:?}"
    );
}

#[tokio::test]
async fn l61_an_unbuildable_request_logs_one_line() {
    // A scheme the router never forwards to cannot form an upstream URL.
    let rig = rig("ftp://127.0.0.1:9").await;
    let answer = through_tunnel(&rig, &streamed_post()).await;
    assert!(answer.starts_with("HTTP/1.1 502"), "{answer}");
    let lines = request_lines(&rig).await;
    assert_eq!(lines.len(), 1, "{lines:?}");
    assert!(
        lines[0].contains("-> anthropic 502 error EUPSTREAM"),
        "{lines:?}"
    );
}

#[tokio::test]
async fn l61_a_failure_line_holds_no_secret() {
    let rig = rig(&dead_url()).await;
    let markers = [
        "AUTHMARK7f3a",
        "KEYMARK91bc",
        "QUERYMARK4d2e",
        "BODYMARK58aa",
    ];
    let pad = "x".repeat(300 * 1024);
    let body = format!(
        r#"{{"model":"claude-opus-5-5","messages":[{{"content":"{}"}}],"pad":"{pad}"}}"#,
        markers[3]
    );
    let post = |host: &str, body: &str| {
        format!(
            "POST /v1/messages?q={} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\nauthorization: Bearer {}\r\nx-api-key: {}\r\nContent-Length: {}\r\n\r\n{body}",
            markers[2],
            markers[0],
            markers[1],
            body.len()
        )
    };
    let small = format!(
        r#"{{"model":"claude-opus-5-5","messages":[{{"content":"{}"}}]}}"#,
        markers[3]
    );
    through_tunnel(&rig, &post("api.anthropic.com", &body)).await;
    direct(&rig, &post(&format!("127.0.0.1:{}", rig.port), &small)).await;
    let lines = request_lines(&rig).await;
    assert_eq!(lines.len(), 2, "{lines:?}");
    for marker in markers {
        assert!(
            lines.iter().all(|l| !l.contains(marker)),
            "{marker} reached the log: {lines:?}"
        );
    }
}

#[tokio::test]
async fn l62_a_model_with_a_newline_stays_on_one_log_line() {
    let rig = rig(&dead_url()).await;
    let body = r#"{"model":"a\nb -> anthropic 200","messages":[]}"#;
    let request = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
        rig.port,
        body.len()
    );
    let answer = direct(&rig, &request).await;
    assert!(answer.starts_with("HTTP/1.1 502"), "{answer}");
    tokio::time::sleep(Duration::from_millis(300)).await;
    let lines = rig.lines.lock().unwrap().clone();
    let named: Vec<_> = lines
        .iter()
        .filter(|l| l.contains("POST /v1/messages"))
        .collect();
    assert_eq!(named.len(), 1, "{lines:?}");
    assert!(
        named[0].contains("model=a?b -> anthropic 200 -> anthropic 502"),
        "{lines:?}"
    );
    assert!(
        lines
            .iter()
            .all(|l| !l.contains('\n') && !l.starts_with('b')),
        "{lines:?}"
    );
}

/// A streamed upload whose client leaves halfway, against an upstream that never answers, is a
/// 499 and writes no failure line.
#[tokio::test]
async fn l62_a_streamed_client_that_left_logs_nothing() {
    let holder = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let url = format!("http://{}", holder.local_addr().expect("addr"));
    let (closed, mut upstream_closed) = tokio::sync::mpsc::unbounded_channel();
    let _held = tokio::spawn(async move {
        while let Ok((mut socket, _)) = holder.accept().await {
            let closed = closed.clone();
            tokio::spawn(async move {
                let mut sink = [0u8; 4096];
                while matches!(socket.read(&mut sink).await, Ok(n) if n > 0) {}
                let _ = closed.send(());
            });
        }
    });
    let rig = rig(&url).await;
    let tcp = connect_tunnel(rig.port, &format!("{ROUTER_HOST}:443")).await;
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    let mut tls = tls_client(&rig.ca_der)
        .connect(name, tcp)
        .await
        .expect("handshake");
    let pad = "x".repeat(200 * 1024);
    let head = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Length: {}\r\n\r\n{{\"model\":\"claude-opus-5-5\",\"pad\":\"{pad}",
        600 * 1024
    );
    tls.write_all(head.as_bytes()).await.expect("write");
    tls.flush().await.expect("flush");
    drop(tls);
    // The router closes its upstream connection once it has settled the request as a 499.
    let settled = tokio::time::timeout(Duration::from_secs(5), upstream_closed.recv()).await;
    assert!(
        settled.is_ok(),
        "the router never closed the upstream connection"
    );
    let lines = named_lines(&rig);
    assert!(lines.is_empty(), "{lines:?}");
}
