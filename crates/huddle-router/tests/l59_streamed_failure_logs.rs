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

#[tokio::test]
#[ignore = "L59: a single failed streamed Anthropic upload is answered 502 with no router log line"]
async fn l59_a_failed_streamed_upload_leaves_a_log_line() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let ca = load_or_create(&paths.ca_dir, SystemTime::now()).expect("ca");
    let port = free_port();
    let dead = free_port();
    let lines: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = lines.clone();
    let router = Router::new(
        RouterConfig {
            env,
            paths,
            port,
            anthropic_url: format!("http://127.0.0.1:{dead}").parse().expect("uri"),
            log: Arc::new(move |l| sink.lock().unwrap().push(l)),
            history: None,
            dashboard: huddle_router::dashboard::DashboardInputs::default(),
        },
        &[],
    );
    let listener = bind(port).await.expect("bind");
    let (_stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });

    let tcp = connect_tunnel(port, &format!("{ROUTER_HOST}:443")).await;
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    let mut tls = tls_client(ca.cert_der())
        .connect(name, tcp)
        .await
        .expect("handshake");
    tls.write_all(streamed_post().as_bytes())
        .await
        .expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), tls.read_to_end(&mut out)).await;
    let answer = String::from_utf8_lossy(&out).into_owned();
    assert!(answer.starts_with("HTTP/1.1 502"), "{answer}");

    tokio::time::sleep(Duration::from_millis(300)).await;
    let lines = lines.lock().unwrap().clone();
    assert!(
        lines
            .iter()
            .any(|l| l.contains("POST /v1/messages") && l.contains("502")),
        "the 502 left no line naming the request: {lines:?}"
    );
}
