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
