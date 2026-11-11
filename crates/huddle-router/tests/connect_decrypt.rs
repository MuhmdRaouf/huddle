use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_pki::ca::{ROUTER_HOST, load_or_create};
use huddle_pki::leaf::LeafCache;
use huddle_router::connect::{
    ConnectCtx, Decrypt, DialFn, DrainOutcome, TunnelTracker, connect_handler,
};
use huddle_router::dispatch::{Router, RouterConfig};
use huddle_router::live::HistorySink;
use huddle_router::server::{Handler, ServerLimits, bind, full_body, serve};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use hyper::{Request, Response};
use rustls::pki_types::{CertificateDer, ServerName};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::watch;
use tokio_rustls::TlsConnector;

fn ca_dir(home: &TempHome) -> PathBuf {
    home.root().join(".local/share/huddle/ca")
}

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
    // A real client offers ALPN; rustls sends the extension only when the list is not empty.
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    TlsConnector::from(Arc::new(config))
}

fn stub_intercept(seen: Arc<Mutex<Vec<String>>>) -> Handler {
    Arc::new(move |req: Request<hyper::body::Incoming>| {
        let seen = seen.clone();
        Box::pin(async move {
            seen.lock()
                .unwrap()
                .push(format!("{} {}", req.method(), req.uri().path()));
            Response::new(full_body(r#"{"ok":"decrypted"}"#))
        })
    })
}

struct Rig {
    port: u16,
    tunnels: Arc<TunnelTracker>,
    notes: Arc<Mutex<Vec<String>>>,
}

async fn rig(dir: PathBuf, seen: Arc<Mutex<Vec<String>>>, dial: Option<DialFn>) -> Rig {
    let notes: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = notes.clone();
    let leaf = Arc::new(LeafCache::new(
        dir,
        Arc::new(move |l: &str| sink.lock().unwrap().push(l.to_string())),
    ));
    let tunnels = TunnelTracker::new();
    let ctx = Arc::new(ConnectCtx {
        env: Env::with(&[]),
        dial,
        tunnels: tunnels.clone(),
        decrypt: Some(Arc::new(Decrypt {
            leaf,
            intercept: stub_intercept(seen),
            limits: ServerLimits::default(),
        })),
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, connect_handler(ctx), ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        port,
        tunnels,
        notes,
    }
}

async fn connect_ok(port: u16, target: &str) -> TcpStream {
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
    assert!(String::from_utf8_lossy(&head).starts_with("HTTP/1.1 200 Connection established\r\n"));
    s
}

async fn roundtrip<S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin>(mut tls: S) -> String {
    tls.write_all(
        b"POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\nContent-Length: 2\r\n\r\n{}",
    )
    .await
    .expect("write");
    let mut out = Vec::new();
    tls.read_to_end(&mut out).await.expect("read");
    String::from_utf8_lossy(&out).into_owned()
}

#[tokio::test]
async fn the_anthropic_host_on_443_is_decrypted_and_served() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    let ca = load_or_create(&dir, SystemTime::now()).expect("ca");
    let seen: Arc<Mutex<Vec<String>>> = Arc::default();
    let rig = rig(dir, seen.clone(), None).await;
    let tcp = connect_ok(rig.port, &format!("{ROUTER_HOST}:443")).await;
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    let tls = tls_client(ca.cert_der())
        .connect(name, tcp)
        .await
        .expect("TLS handshake against the local CA");
    assert_eq!(
        tls.get_ref().1.alpn_protocol(),
        Some(&b"http/1.1"[..]),
        "ALPN negotiated http/1.1"
    );
    let resp = roundtrip(tls).await;
    assert!(resp.contains(r#"{"ok":"decrypted"}"#), "{resp}");
    assert_eq!(seen.lock().unwrap().as_slice(), ["POST /v1/messages"]);
}

#[tokio::test]
async fn another_port_or_host_stays_blind() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    load_or_create(&dir, SystemTime::now()).expect("ca");
    let upstream = TcpListener::bind(("127.0.0.1", 0)).await.expect("bind");
    let upstream_port = upstream.local_addr().expect("addr").port();
    tokio::spawn(async move {
        while let Ok((mut s, _)) = upstream.accept().await {
            tokio::spawn(async move {
                let _ = s.write_all(b"not tls").await;
            });
        }
    });
    let dial: DialFn = Arc::new(move |_t| {
        Box::pin(async move { TcpStream::connect(("127.0.0.1", upstream_port)).await })
    });
    let seen: Arc<Mutex<Vec<String>>> = Arc::default();
    let rig = rig(dir, seen.clone(), Some(dial)).await;
    for target in [
        format!("{ROUTER_HOST}:8443"),
        "example.test:443".to_string(),
    ] {
        let mut s = connect_ok(rig.port, &target).await;
        let mut buf = [0u8; 7];
        tokio::time::timeout(Duration::from_secs(3), s.read_exact(&mut buf))
            .await
            .expect("timely")
            .expect("read");
        assert_eq!(&buf, b"not tls", "{target} passed through blind");
    }
    assert!(
        seen.lock().unwrap().is_empty(),
        "nothing reached the interceptor"
    );
}

#[tokio::test]
async fn a_ca_that_cannot_load_passes_through_blind() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    std::fs::create_dir_all(&dir).expect("mkdir");
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o770)).expect("chmod");
    let upstream = TcpListener::bind(("127.0.0.1", 0)).await.expect("bind");
    let upstream_port = upstream.local_addr().expect("addr").port();
    tokio::spawn(async move {
        if let Ok((mut s, _)) = upstream.accept().await {
            let _ = s.write_all(b"blind!!").await;
        }
    });
    let dial: DialFn = Arc::new(move |_t| {
        Box::pin(async move { TcpStream::connect(("127.0.0.1", upstream_port)).await })
    });
    let seen: Arc<Mutex<Vec<String>>> = Arc::default();
    let rig = rig(dir, seen.clone(), Some(dial)).await;
    let mut s = connect_ok(rig.port, &format!("{ROUTER_HOST}:443")).await;
    let mut buf = [0u8; 7];
    tokio::time::timeout(Duration::from_secs(3), s.read_exact(&mut buf))
        .await
        .expect("timely")
        .expect("read");
    assert_eq!(&buf, b"blind!!");
    assert!(rig.notes.lock().unwrap()[0].starts_with("local CA unusable ("));
}

#[tokio::test]
async fn a_drain_closes_an_idle_decrypted_conversation_at_once() {
    let home = TempHome::new();
    let dir = ca_dir(&home);
    let ca = load_or_create(&dir, SystemTime::now()).expect("ca");
    let seen: Arc<Mutex<Vec<String>>> = Arc::default();
    let rig = rig(dir, seen.clone(), None).await;
    let tcp = connect_ok(rig.port, &format!("{ROUTER_HOST}:443")).await;
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    let _tls = tls_client(ca.cert_der())
        .connect(name, tcp)
        .await
        .expect("handshake");
    tokio::time::sleep(Duration::from_millis(50)).await;
    let outcome = tokio::time::timeout(
        Duration::from_secs(5),
        rig.tunnels.drain(Duration::from_secs(4)),
    )
    .await
    .expect("timely");
    assert_eq!(
        outcome,
        DrainOutcome::Finished,
        "an idle conversation closes when the drain starts"
    );
}

async fn model_request<S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin>(
    tls: &mut S,
    body: &str,
) {
    let head = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    );
    tls.write_all(head.as_bytes()).await.expect("write");
    let mut seen = Vec::new();
    let mut buf = [0u8; 1024];
    while !String::from_utf8_lossy(&seen).contains("\r\n\r\n") {
        let n = tls.read(&mut buf).await.expect("read");
        assert!(n > 0, "answer head");
        seen.extend_from_slice(&buf[..n]);
    }
    // The answers are one short line each; the next request waits for this one's end.
    tokio::time::sleep(Duration::from_millis(100)).await;
}

#[derive(Default)]
struct Collect {
    rows: std::sync::Mutex<Vec<huddle_history::Row>>,
}

impl HistorySink for Collect {
    fn finished(&self, row: huddle_history::Row) {
        self.rows.lock().unwrap().push(row);
    }

    fn credited(&self, _credits: Vec<huddle_history::Credit>) {}
}

#[tokio::test]
async fn t5_decrypted_model_requests_reach_the_history() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let zai = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let zai_url = zai.url();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_ROUTER_URL", zai_url.as_str()),
        ("ZAI_API_KEY", "test-key-test-key"),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let ca = load_or_create(&paths.ca_dir, SystemTime::now()).expect("ca");
    let port = free_port();
    let rows = Arc::new(Collect::default());
    let router = Router::new(
        RouterConfig {
            env,
            paths: paths.clone(),
            port,
            anthropic_url: anthropic.url().parse().expect("uri"),
            log: Arc::new(|_| {}),
            history: Some(rows.clone()),
            dashboard: huddle_router::dashboard::DashboardInputs::default(),
        },
        &["zai".to_string()],
    );
    let listener = bind(port).await.expect("bind");
    let (_stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    let tcp = connect_ok(port, &format!("{ROUTER_HOST}:443")).await;
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    let mut tls = tls_client(ca.cert_der())
        .connect(name, tcp)
        .await
        .expect("handshake");
    model_request(&mut tls, r#"{"model":"claude-opus-5-5","messages":[]}"#).await;
    model_request(&mut tls, r#"{"model":"glm-5.3","messages":[]}"#).await;
    let deadline = std::time::Instant::now() + Duration::from_secs(3);
    while rows.rows.lock().unwrap().len() < 2 && std::time::Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let got = rows.rows.lock().unwrap().clone();
    let find = |lane: &str| got.iter().find(|row| row.lane == lane);
    let anthropic_row = find("anthropic").unwrap_or_else(|| panic!("no anthropic row: {got:?}"));
    assert_eq!(anthropic_row.model, "claude-opus-5-5");
    assert_eq!(anthropic_row.status, 200);
    let provider_row = find("zai").unwrap_or_else(|| panic!("no provider row: {got:?}"));
    assert_eq!(provider_row.model, "glm-5.3");
    assert_eq!(provider_row.status, 200);
    drop(zai);
    drop(anthropic);
}
