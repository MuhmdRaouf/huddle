//! L61: a request a provider lane refuses to send, because its destination is unusable,
//! answers 502 and leaves one log line.

use huddle_catalog::providers::provider_by_id;
use huddle_config::env::Env;
use huddle_keystore::port::SecretString;
use huddle_router::{
    keycache::{KeyRead, KeySources},
    lane::Lane,
    upstream::{IDLE_TIMEOUT, UpstreamClient, UpstreamConfig},
};
use huddle_testkit::temp_home::TempHome;
use hyper::body::Bytes;
use hyper::{Method, Request, StatusCode};
use rustls::RootCertStore;
use std::sync::{Arc, Mutex};

#[tokio::test]
async fn l61_a_refused_provider_upstream_logs_one_line() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_ROUTER_URL", "http://provider.example:80"),
    ]);
    let read: KeyRead = Arc::new(|| Ok(Some(SecretString::new("test-key-test-key".to_string()))));
    let lines: Arc<Mutex<Vec<String>>> = Arc::default();
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
    let parts = Request::builder()
        .method(Method::POST)
        .uri("/v1/messages?q=QUERYMARK")
        .body(())
        .expect("request")
        .into_parts()
        .0;
    let body = Bytes::from_static(br#"{"model":"glm-5.3","messages":[]}"#);
    let answer = lane.forward(parts, body, "glm-5.3").await;
    assert_eq!(answer.status(), StatusCode::BAD_GATEWAY);
    let lines = lines.lock().unwrap().clone();
    assert_eq!(
        lines,
        vec![
            "POST /v1/messages model=glm-5.3 -> zai 502 error EUPSTREAM phase=send peer=- conn=fresh"
                .to_owned()
        ]
    );
}

type Lines = Arc<Mutex<Vec<String>>>;

/// A zai lane aimed at `upstream`, and the lines it logs.
fn lane_at(upstream: &str) -> (Arc<Lane>, Lines, TempHome) {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_ROUTER_URL", upstream)]);
    let read: KeyRead = Arc::new(|| Ok(Some(SecretString::new("test-key-test-key".to_string()))));
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
    (lane, lines, home)
}

async fn send(lane: &Lane, uri: &str) -> StatusCode {
    let parts = Request::builder()
        .method(Method::POST)
        .uri(uri)
        .body(())
        .expect("request")
        .into_parts()
        .0;
    let body = Bytes::from_static(br#"{"model":"glm-5.3","messages":[]}"#);
    lane.forward(parts, body, "glm-5.3").await.status()
}

#[tokio::test]
async fn l62_both_lane_failures_share_one_shape() {
    let dead = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
    let dead_url = format!("http://{}", dead.local_addr().expect("addr"));
    drop(dead);
    let (lane, sent, _home) = lane_at(&dead_url);
    assert_eq!(send(&lane, "/v1/messages").await, StatusCode::BAD_GATEWAY);
    let (refusing, refused, _home) = lane_at("http://provider.example:80");
    assert_eq!(
        send(&refusing, "/v1/messages").await,
        StatusCode::BAD_GATEWAY
    );
    let sent = sent.lock().unwrap().clone();
    let refused = refused.lock().unwrap().clone();
    assert_eq!(sent.len(), 1, "{sent:?}");
    assert_eq!(refused.len(), 1, "{refused:?}");
    for line in sent.iter().chain(refused.iter()) {
        assert!(
            line.starts_with("POST /v1/messages model=glm-5.3 -> zai 502 error E"),
            "{line}"
        );
    }
    assert!(sent[0].contains("ECONNREFUSED"), "{sent:?}");
}

/// The lane's failure line for a request whose model text is `model`, against a refusing upstream.
async fn line_for_model(model: &str) -> Vec<String> {
    let (lane, lines, _home) = lane_at("http://provider.example:80");
    let parts = Request::builder()
        .method(Method::POST)
        .uri("/v1/messages")
        .body(())
        .expect("request")
        .into_parts()
        .0;
    let body = Bytes::from_static(br#"{"model":"glm-5.3","messages":[]}"#);
    lane.forward(parts, body, model).await;
    lines.lock().unwrap().clone()
}

#[tokio::test]
async fn l64_a_lane_model_with_a_newline_stays_on_one_log_line() {
    let lines = line_for_model("glm-5.3\nb").await;
    assert_eq!(lines.len(), 1, "{lines:?}");
    assert!(lines[0].contains("model=glm-5.3?b "), "{lines:?}");
    assert!(!lines[0].contains('\n'), "{lines:?}");
}

#[tokio::test]
async fn l64_a_model_with_bidi_or_line_separator_text_is_logged_plain() {
    let lines = line_for_model("glm-5.3\u{2028}a\u{2029}b\u{202E}c\u{2066}d\u{2069}e").await;
    assert_eq!(lines.len(), 1, "{lines:?}");
    assert!(lines[0].contains("model=glm-5.3?a?b?c?d?e "), "{lines:?}");
}
