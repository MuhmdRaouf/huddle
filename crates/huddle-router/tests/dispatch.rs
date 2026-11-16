//! The dispatch: a model goes to its lane or to Anthropic, the health answer lists the lanes,
//! and the streak raises once and recovers once.

use huddle_catalog::routing::{Route, decide_resolved};
use huddle_config::{env::Env, paths::Paths};
use huddle_router::{
    anthropic::AnthropicPath,
    dispatch::{Router, RouterConfig},
    server::{BODY_LIMIT, ServerLimits, bind, full_body, serve},
    upstream::{UpstreamClient, UpstreamConfig},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    ports::free_port,
    temp_home::TempHome,
};
use hyper::{Response, Uri, body::Bytes};
use std::sync::{
    Arc, Mutex,
    atomic::{AtomicUsize, Ordering},
};
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
    rig_with_ports(enabled, free_port).await
}

/// Builds the rig, taking every port from `next_port`.
async fn rig_with_ports(enabled: &[&str], next_port: impl Fn() -> u16) -> Rig {
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
    // A port another process took between the pick and the bind costs a retry, never a failed
    // test: bind first, then build the config with the port that was bound.
    let mut listener = None;
    let mut port = 0;
    let mut last_err = None;
    for _ in 0..10 {
        port = next_port();
        match bind(port).await {
            Ok(bound) => {
                listener = Some(bound);
                break;
            }
            Err(err) if err.kind() == std::io::ErrorKind::AddrInUse => last_err = Some(err),
            Err(err) => panic!("bind 127.0.0.1:{port}: {err}"),
        }
    }
    let listener = match listener {
        Some(listener) => listener,
        None => panic!(
            "bind 127.0.0.1:{port} after 10 tries: {:?}",
            last_err.expect("the last bind error")
        ),
    };
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

/// A model request body whose model sits in the head and whose size pushes it past the
/// streamed relay.
fn l15_message(model: &str, size: usize) -> String {
    let head =
        format!(r#"{{"model":"{model}","max_tokens":16,"messages":[{{"role":"user","content":""#);
    let tail = r#""}]}"#;
    let padding = size.saturating_sub(head.len() + tail.len());
    let mut body = head.into_bytes();
    body.extend(std::iter::repeat_n(b'x', padding));
    body.extend_from_slice(tail.as_bytes());
    String::from_utf8(body).expect("utf8")
}

/// Serves one hand-built path over plain HTTP: the route resolver comes from the caller, the
/// streamed route records the lane decisions it served and answers with `streamed-stub`, and
/// the read-whole route records the decision each request carried and answers with
/// `whole-stub`.
async fn l15_path_rig(
    route_of: Arc<dyn Fn(&str) -> Route + Send + Sync>,
    streamed_calls: Arc<Mutex<Vec<(&'static str, String)>>>,
    whole_calls: Arc<Mutex<Vec<Option<Route>>>>,
) -> u16 {
    let path = Arc::new(AnthropicPath {
        client: Arc::new(UpstreamClient::new(UpstreamConfig {
            env: Env::with(&[]),
            roots: rustls::RootCertStore::empty(),
            idle: std::time::Duration::from_secs(30),
        })),
        target: "http://127.0.0.1:9".parse::<Uri>().expect("target"),
        body_limit: BODY_LIMIT,
        route_of,
        streamed_route: {
            let sink = streamed_calls;
            Arc::new(
                move |_parts, _stream, provider: &'static str, model: String| {
                    let sink = Arc::clone(&sink);
                    Box::pin(async move {
                        sink.lock().expect("sink").push((provider, model));
                        Response::new(full_body("streamed-stub"))
                    })
                },
            )
        },
        whole_route: {
            let sink = whole_calls;
            Arc::new(move |_parts, _body: Bytes, decided: Option<Route>| {
                let sink = Arc::clone(&sink);
                Box::pin(async move {
                    sink.lock().expect("sink").push(decided);
                    Response::new(full_body("whole-stub"))
                })
            })
        },
        pass_through: Arc::new(|_req| Box::pin(async { Response::new(full_body("pass-stub")) })),
        on_outcome: None,
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_stop, rx) = watch::channel(false);
    let handler = path.intercept_handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    port
}

#[tokio::test]
async fn l15_a_model_request_decides_its_lane_once() {
    let decisions = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&decisions);
    let route_of: Arc<dyn Fn(&str) -> Route + Send + Sync> = Arc::new(move |model: &str| {
        counted.fetch_add(1, Ordering::SeqCst);
        decide_resolved(model, &["zai"], &[])
    });
    let streamed_calls: Arc<Mutex<Vec<(&'static str, String)>>> = Arc::default();
    let whole_calls: Arc<Mutex<Vec<Option<Route>>>> = Arc::default();
    let port = l15_path_rig(
        route_of,
        Arc::clone(&streamed_calls),
        Arc::clone(&whole_calls),
    )
    .await;
    let body = l15_message("glm-5.3", 512 * 1024);
    let answer = post(port, "/v1/messages", &body).await;
    assert!(answer.contains("streamed-stub"), "{answer}");
    assert_eq!(
        *streamed_calls.lock().expect("calls"),
        vec![("zai", "glm-5.3".to_owned())],
        "the streamed route took the decision's lane"
    );
    assert_eq!(
        decisions.load(Ordering::SeqCst),
        1,
        "one request, one lane decision"
    );
}

/// A body that fits in the head is read whole, and the head's lane decision travels with it.
#[tokio::test]
async fn l40_a_small_body_reaches_the_read_whole_route_with_the_heads_decision() {
    let decisions = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&decisions);
    let route_of: Arc<dyn Fn(&str) -> Route + Send + Sync> = Arc::new(move |model: &str| {
        counted.fetch_add(1, Ordering::SeqCst);
        decide_resolved(model, &["zai"], &[])
    });
    let streamed_calls: Arc<Mutex<Vec<(&'static str, String)>>> = Arc::default();
    let whole_calls: Arc<Mutex<Vec<Option<Route>>>> = Arc::default();
    let port = l15_path_rig(route_of, streamed_calls, Arc::clone(&whole_calls)).await;
    let body = l15_message("glm-5.3", 512);
    let answer = post(port, "/v1/messages", &body).await;
    assert!(answer.contains("whole-stub"), "{answer}");
    assert_eq!(
        decisions.load(Ordering::SeqCst),
        1,
        "one request, one lane decision"
    );
    let calls = whole_calls.lock().expect("calls").clone();
    assert_eq!(calls.len(), 1, "{calls:?}");
    assert!(
        matches!(calls[0], Some(Route::Lane(provider)) if provider.id == "zai"),
        "the head's decision reaches the whole route: {calls:?}"
    );
}

/// A body whose model sits past the head is read whole with no decision: the route decides on
/// the whole body itself.
#[tokio::test]
async fn l40_a_model_late_body_reaches_the_read_whole_route_with_no_decision() {
    let decisions = Arc::new(AtomicUsize::new(0));
    let counted = Arc::clone(&decisions);
    let route_of: Arc<dyn Fn(&str) -> Route + Send + Sync> = Arc::new(move |model: &str| {
        counted.fetch_add(1, Ordering::SeqCst);
        decide_resolved(model, &["zai"], &[])
    });
    let streamed_calls: Arc<Mutex<Vec<(&'static str, String)>>> = Arc::default();
    let whole_calls: Arc<Mutex<Vec<Option<Route>>>> = Arc::default();
    let port = l15_path_rig(route_of, streamed_calls, Arc::clone(&whole_calls)).await;
    let padding = "x".repeat(70 * 1024);
    let body = format!(
        r#"{{"messages":[{{"role":"user","content":"{padding}"}}],"model":"glm-5.3","max_tokens":16}}"#
    );
    let answer = post(port, "/v1/messages", &body).await;
    assert!(answer.contains("whole-stub"), "{answer}");
    assert_eq!(
        decisions.load(Ordering::SeqCst),
        1,
        "the head's unsettled model asks once; its answer is discarded"
    );
    assert_eq!(
        *whole_calls.lock().expect("calls"),
        vec![None],
        "the whole route decides on the whole body itself"
    );
}

#[tokio::test]
async fn l15_routing_results_are_unchanged() {
    let r = rig(&["zai"]).await;
    let claude = post(
        r.port,
        "/v1/messages",
        r#"{"model":"claude-opus-5-5","messages":[]}"#,
    )
    .await;
    assert!(claude.contains("from-anthropic"), "{claude}");
    let lane = post(
        r.port,
        "/v1/messages",
        r#"{"model":"glm-5.3","messages":[]}"#,
    )
    .await;
    assert!(lane.contains("from-zai"), "{lane}");
    let unknown = post(
        r.port,
        "/v1/messages",
        r#"{"model":"unknown-model","messages":[]}"#,
    )
    .await;
    assert!(unknown.contains("from-anthropic"), "{unknown}");
    assert_eq!(r.anthropic.requests().len(), 2);
    assert_eq!(r.zai.requests().len(), 1);
    let _ = r.stop.send(true);
}

/// The rig survives a port another process holds: it moves past the taken port and serves on the
/// one it bound.
#[tokio::test]
async fn l49_the_rig_survives_a_taken_port() {
    let taken = free_port();
    let holder = std::net::TcpListener::bind(("127.0.0.1", taken)).expect("hold the port");
    let queue = Arc::new(Mutex::new(vec![taken]));
    let next = {
        let queue = Arc::clone(&queue);
        move || {
            let mut queue = queue.lock().expect("queue");
            match queue.pop() {
                Some(port) => port,
                None => free_port(),
            }
        }
    };
    let r = rig_with_ports(&["zai"], next).await;
    drop(holder);
    assert_ne!(r.port, taken, "the rig moved past the taken port");
    let answer = post(
        r.port,
        "/v1/messages",
        r#"{"model":"glm-5.3","messages":[]}"#,
    )
    .await;
    assert!(answer.contains("from-zai"), "{answer}");
    assert_eq!(r.zai.requests().len(), 1);
    let _ = r.stop.send(true);
}
