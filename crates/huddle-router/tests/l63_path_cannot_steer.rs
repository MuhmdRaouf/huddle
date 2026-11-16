//! L63: the scheme, host and port of every upstream request come from the configured base only.
//! A request target that could change them is answered 400, sends nothing upstream and carries
//! no key anywhere.

use huddle_catalog::providers::provider_by_id;
use huddle_config::{env::Env, paths::Paths};
use huddle_keystore::port::SecretString;
use huddle_router::{
    dispatch::{Router, RouterConfig},
    keycache::{KeyRead, KeySources},
    lane::Lane,
    server::{ServerLimits, bind, serve},
    upstream::{IDLE_TIMEOUT, UpstreamClient, UpstreamConfig},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    ports::free_port,
    temp_home::TempHome,
};
use hyper::{Method, Request, StatusCode, Uri, body::Bytes};
use rustls::RootCertStore;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

const KEY: &str = "test-key-test-key";
const CLIENT_KEY: &str = "client-secret-marker";

type Lines = Arc<Mutex<Vec<String>>>;

struct Rig {
    port: u16,
    lane: FakeUpstream,
    anthropic: FakeUpstream,
    foreign: FakeUpstream,
    lines: Lines,
    _home: TempHome,
    _stop: watch::Sender<bool>,
}

async fn rig() -> Rig {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "from-anthropic")).await;
    let lane = FakeUpstream::start(|_, _| Step::answer(200, "from-zai")).await;
    let foreign = FakeUpstream::start(|_, _| Step::answer(200, "from-foreign")).await;
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let lane_url = lane.url();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_ROUTER_URL", lane_url.as_str()),
        ("ZAI_API_KEY", KEY),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let port = free_port();
    let listener = bind(port).await.expect("bind");
    let lines: Lines = Arc::default();
    let sink = lines.clone();
    let router = Router::new(
        RouterConfig {
            env,
            paths,
            port,
            anthropic_url: anthropic.url().parse::<Uri>().expect("uri"),
            log: Arc::new(move |l| sink.lock().unwrap().push(l)),
            history: None,
            dashboard: huddle_router::dashboard::DashboardInputs::default(),
        },
        &["zai".to_owned()],
    );
    let (stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        port,
        lane,
        anthropic,
        foreign,
        lines,
        _home: home,
        _stop: stop,
    }
}

/// One raw request line to the router; the router's whole answer.
async fn raw(port: u16, method: &str, target: &str, body: &str) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let request = format!(
        "{method} {target} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nAuthorization: Bearer {CLIENT_KEY}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    s.write_all(request.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

fn status_of(answer: &str) -> &str {
    answer.split(' ').nth(1).unwrap_or("")
}

fn saw_key(upstream: &FakeUpstream) -> bool {
    upstream.requests().iter().any(|r| {
        r.headers
            .iter()
            .any(|(_, v)| v.contains(KEY) || v.contains(CLIENT_KEY))
    })
}

/// Every target whose path part could change the host an assembled URL points at.
fn steering_targets(foreign: u16) -> Vec<String> {
    vec![
        "*".to_owned(),
        format!("@127.0.0.1:{foreign}/v1/messages"),
        ".evil/v1".to_owned(),
        format!(":{foreign}/v1/messages"),
        format!("//127.0.0.1:{foreign}/v1/messages"),
        format!("\\\\127.0.0.1:{foreign}"),
        format!("/\\127.0.0.1:{foreign}/v1/messages"),
        format!("http://127.0.0.1:{foreign}/v1/messages"),
        format!("/v1/messages?x=@127.0.0.1:{foreign}"),
        format!("/v1/messages/../../x@127.0.0.1:{foreign}"),
        format!("/%2f%2f127.0.0.1:{foreign}/v1/messages"),
    ]
}

/// The targets the router itself refuses: not an origin-form path, or one that holds a
/// backslash.
fn must_be_refused(target: &str) -> bool {
    !target.starts_with('/') && !target.starts_with("http://")
        || target.starts_with("//")
        || target.contains('\\')
}

#[tokio::test]
async fn l63_no_request_target_reaches_another_host() {
    let r = rig().await;
    let glm = r#"{"model":"glm-5.3","messages":[]}"#;
    let claude = r#"{"model":"claude-opus-5-5","messages":[]}"#;
    let mut report = Vec::new();
    let mut not_refused = Vec::new();
    for target in steering_targets(r.foreign.port()) {
        for (route, method, body) in [
            ("lane", "POST", glm),
            ("anthropic", "POST", claude),
            ("pass-through", "GET", ""),
            ("options", "OPTIONS", claude),
            ("options-lane", "OPTIONS", glm),
        ] {
            let answer = raw(r.port, method, &target, body).await;
            let status = status_of(&answer).to_owned();
            report.push(format!("{route:<12} {target:<48} -> {status}"));
            if target.starts_with("/\\") && !answer.contains("request path refused") {
                not_refused.push(format!("{route} {target}: wrong body"));
            }
            if must_be_refused(&target) && status != "400" {
                not_refused.push(format!("{route} {target}: {status}"));
            }
        }
    }
    let reached = r.foreign.requests();
    assert!(
        reached.is_empty() && !saw_key(&r.foreign),
        "the foreign fake got {} requests, key seen: {}\n{}",
        reached.len(),
        saw_key(&r.foreign),
        report.join("\n")
    );
    assert!(
        not_refused.is_empty(),
        "not answered 400:\n{}",
        not_refused.join("\n")
    );
    // A refused request is never sent to the lane or to Anthropic either.
    for upstream in [&r.lane, &r.anthropic] {
        for seen in upstream.requests() {
            assert!(
                seen.path.starts_with('/') && !seen.path.starts_with("//"),
                "{}",
                seen.path
            );
        }
    }
}

fn lane_at(upstream: &str) -> (Arc<Lane>, Lines, TempHome) {
    let (lane, lines, home, _reads) = lane_counting(upstream);
    (lane, lines, home)
}

/// A lane that counts its key reads.
fn lane_counting(upstream: &str) -> (Arc<Lane>, Lines, TempHome, Arc<AtomicUsize>) {
    let reads = Arc::new(AtomicUsize::new(0));
    let counter = reads.clone();
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_ROUTER_URL", upstream)]);
    let read: KeyRead = Arc::new(move || {
        counter.fetch_add(1, Ordering::SeqCst);
        Ok(Some(SecretString::new(KEY.to_string())))
    });
    let lines: Lines = Arc::default();
    let sink = lines.clone();
    let lane = Lane::build(
        provider_by_id("zai").expect("zai"),
        &env,
        KeySources {
            env_key: None,
            os: None,
            file: read,
        },
        Arc::new(UpstreamClient::new(UpstreamConfig {
            env: Env::with(&[]),
            roots: RootCertStore::empty(),
            idle: IDLE_TIMEOUT,
        })),
        Arc::new(move |l| sink.lock().unwrap().push(l)),
    )
    .expect("lane");
    (lane, lines, home, reads)
}

#[tokio::test]
async fn l63_a_refused_target_logs_one_line_without_the_target() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, lines, _home) = lane_at(&upstream.url());
    let parts = Request::builder()
        .method(Method::OPTIONS)
        .uri("*")
        .body(())
        .expect("request")
        .into_parts()
        .0;
    let body = Bytes::from_static(br#"{"model":"glm-5.3","messages":[]}"#);
    let answer = lane.forward(parts, body, "glm-5.3").await;
    assert_eq!(answer.status(), StatusCode::BAD_REQUEST);
    assert!(upstream.requests().is_empty());
    let lines = lines.lock().unwrap().clone();
    assert_eq!(
        lines,
        vec![
            "OPTIONS - model=glm-5.3 -> zai 400 error EPATH phase=send peer=- conn=fresh"
                .to_owned()
        ]
    );
}

#[tokio::test]
async fn l63_a_normal_path_and_query_arrive_unchanged() {
    let r = rig().await;
    let glm = r#"{"model":"glm-5.3","messages":[]}"#;
    let path = "/v1/messages/../x?a=1&b=@host:9";
    let answer = raw(r.port, "POST", path, glm).await;
    assert_eq!(status_of(&answer), "200", "{answer}");
    assert_eq!(r.lane.requests()[0].path, path);
    let claude = r#"{"model":"claude-opus-5-5","messages":[]}"#;
    let answer = raw(r.port, "POST", "/v1/messages?c=2", claude).await;
    assert_eq!(status_of(&answer), "200", "{answer}");
    assert_eq!(r.anthropic.requests()[0].path, "/v1/messages?c=2");
    let _ = r.lines.lock().unwrap().len();
}

#[tokio::test]
async fn l63_a_refused_target_reads_no_key() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _lines, _home, reads) = lane_counting(&upstream.url());
    let parts = Request::builder()
        .method(Method::OPTIONS)
        .uri("*")
        .body(())
        .expect("request")
        .into_parts()
        .0;
    let body = Bytes::from_static(br#"{"model":"glm-5.3","messages":[]}"#);
    let answer = lane.forward(parts, body, "glm-5.3").await;
    assert_eq!(answer.status(), StatusCode::BAD_REQUEST);
    assert_eq!(reads.load(Ordering::SeqCst), 0);
}
