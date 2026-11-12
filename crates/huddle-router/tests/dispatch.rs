//! The dispatch: a model goes to its lane or to Anthropic, the health answer lists the lanes,
//! and the streak raises once and recovers once.

use huddle_config::{env::Env, paths::Paths};
use huddle_router::{
    dispatch::{Router, RouterConfig},
    server::{ServerLimits, bind, serve},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    ports::free_port,
    temp_home::TempHome,
};
use hyper::Uri;
use std::sync::Arc;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::watch,
};

async fn post(port: u16, path: &str, body: &str) -> String {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let head = format!(
        "POST {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");
    String::from_utf8_lossy(&out).into_owned()
}

struct Rig {
    _home: TempHome,
    port: u16,
    anthropic: FakeUpstream,
    zai: FakeUpstream,
    stop: watch::Sender<bool>,
}

async fn rig(enabled: &[&str]) -> Rig {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "from-anthropic")).await;
    let zai = FakeUpstream::start(|_, _| Step::answer(200, "from-zai")).await;
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let zai_url = zai.url();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_ROUTER_URL", zai_url.as_str()),
        ("ZAI_API_KEY", "test-key-test-key"),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let port = free_port();
    let config = RouterConfig {
        env,
        paths,
        port,
        anthropic_url: anthropic.url().parse::<Uri>().expect("uri"),
        log: Arc::new(|_| {}),
        history: None,
        dashboard: huddle_router::dashboard::DashboardInputs::default(),
    };
    let enabled: Vec<String> = enabled.iter().map(|s| s.to_string()).collect();
    let router = Router::new(config, &enabled);
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
        port,
        anthropic,
        zai,
        stop,
    }
}

#[tokio::test]
async fn a_lane_model_goes_to_its_provider_and_a_claude_model_goes_to_anthropic() {
    let r = rig(&["zai"]).await;
    let zai = post(
        r.port,
        "/v1/messages",
        r#"{"model":"glm-5.3","messages":[]}"#,
    )
    .await;
    assert!(zai.contains("from-zai"), "{zai}");
    let claude = post(
        r.port,
        "/v1/messages",
        r#"{"model":"claude-opus-5-5","messages":[]}"#,
    )
    .await;
    assert!(claude.contains("from-anthropic"), "{claude}");
    assert_eq!(r.anthropic.requests().len(), 1);
    assert_eq!(r.zai.requests().len(), 1);
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn an_unknown_model_or_a_body_without_a_model_goes_to_anthropic() {
    let r = rig(&["zai"]).await;
    post(r.port, "/v1/messages", r#"{"model":"unknown-model"}"#).await;
    post(r.port, "/v1/messages", r#"{"messages":[]}"#).await;
    assert_eq!(r.anthropic.requests().len(), 2);
    assert_eq!(r.zai.requests().len(), 0);
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn a_claude_request_reaches_anthropic_with_the_clients_headers_unchanged() {
    let r = rig(&["zai"]).await;
    let mut stream = TcpStream::connect(("127.0.0.1", r.port))
        .await
        .expect("connect");
    let body = r#"{"model":"claude-opus-5-5","messages":[]}"#;
    let head = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nx-api-key: claude-key\r\nauthorization: Bearer claude-auth\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        r.port,
        body.len()
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");
    let seen = &r.anthropic.requests()[0];
    assert!(
        seen.headers
            .iter()
            .any(|(n, v)| n.eq_ignore_ascii_case("x-api-key") && v == "claude-key")
    );
    assert!(
        seen.headers
            .iter()
            .any(|(n, v)| n.eq_ignore_ascii_case("authorization") && v == "Bearer claude-auth")
    );
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn the_health_answer_lists_the_lanes() {
    let r = rig(&["zai"]).await;
    let mut stream = TcpStream::connect(("127.0.0.1", r.port))
        .await
        .expect("connect");
    let head = format!(
        "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
        r.port
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    stream.read_to_end(&mut out).await.expect("read");
    assert!(String::from_utf8_lossy(&out).contains("\"providers\":[\"zai\"]"));
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn no_lane_at_all_still_serves_claude_requests() {
    let r = rig(&[]).await;
    let answer = post(
        r.port,
        "/v1/messages",
        r#"{"model":"glm-5.3","messages":[]}"#,
    )
    .await;
    assert!(
        answer.contains("from-anthropic"),
        "with no lane a model goes to Anthropic: {answer}"
    );
    let _ = r.stop.send(true);
}

#[test]
fn a_streak_raises_once_and_recovers_once() {
    use huddle_router::dispatch::FailStreak;
    let mut streak = FailStreak::default();
    assert!(!streak.fail(0));
    assert!(!streak.fail(1000));
    assert!(streak.fail(2000), "the third failure within a minute");
    assert!(!streak.fail(3000), "quiet after a raise");
    assert!(streak.ok(4000).is_some(), "the first success after a raise");
    assert!(streak.ok(5000).is_none());
}

#[test]
fn failures_spread_over_more_than_a_minute_do_not_raise() {
    use huddle_router::dispatch::FailStreak;
    let mut streak = FailStreak::default();
    assert!(!streak.fail(0));
    assert!(!streak.fail(61_000));
    assert!(!streak.fail(122_000));
}

#[test]
fn os_roots_read_the_platform_trust_store_and_nothing_bundled() {
    if std::env::var_os("HUDDLE_OS_ROOTS_CHILD").is_some() {
        // SSL_CERT_FILE names a one-certificate store; the platform loader returns exactly it.
        let store = huddle_router::dispatch::os_roots(&|_| {});
        assert_eq!(
            store.len(),
            1,
            "the loader's certificates and no bundled roots"
        );
        return;
    }
    let home = TempHome::new();
    let dir = home.root().join(".local/share/huddle/ca");
    huddle_pki::ca::load_or_create(&dir, std::time::SystemTime::now()).expect("ca");
    // The probe reads the system cert directory when SSL_CERT_DIR is unset:
    // point it at an empty one so the file names the whole store.
    let no_dir = home.root().join("no-certs");
    std::fs::create_dir_all(&no_dir).expect("cert dir");
    let exe = std::env::current_exe().expect("test binary");
    let out = std::process::Command::new(exe)
        .args([
            "--exact",
            "os_roots_read_the_platform_trust_store_and_nothing_bundled",
            "--nocapture",
        ])
        .env("SSL_CERT_FILE", huddle_pki::ca::ca_cert_path(&dir))
        .env("SSL_CERT_DIR", &no_dir)
        .env("HUDDLE_OS_ROOTS_CHILD", "1")
        .output()
        .expect("the child test");
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
}
