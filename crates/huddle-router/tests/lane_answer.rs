//! The provider lane's answer side: errors judged before the client sees them, successes
//! streamed byte-identical past the usage tap.

use http_body_util::BodyExt;
use huddle_catalog::providers::provider_by_id;
use huddle_config::env::Env;
use huddle_keystore::port::SecretString;
use huddle_router::{
    keycache::{KeyRead, KeySources},
    lane::Lane,
    server::RespBody,
    upstream::{IDLE_TIMEOUT, UpstreamClient, UpstreamConfig},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    temp_home::TempHome,
};
use hyper::body::Bytes;
use hyper::{Method, Request, Response, StatusCode};
use rustls::RootCertStore;
use std::sync::{
    Arc,
    atomic::{AtomicUsize, Ordering},
};

const KEY: &str = "test-key-test-key-1234";

fn lane(url: &str, reads: Arc<AtomicUsize>) -> Arc<Lane> {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_ROUTER_URL", url)]);
    let read: KeyRead = Arc::new(move || {
        reads.fetch_add(1, Ordering::SeqCst);
        Ok(Some(SecretString::new(KEY.to_string())))
    });
    let client = Arc::new(UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle: IDLE_TIMEOUT,
    }));
    Lane::build(
        provider_by_id("zai").expect("zai"),
        &env,
        KeySources {
            env_key: None,
            os: None,
            file: read,
        },
        client,
        Arc::new(|_| {}),
    )
    .expect("lane")
}

fn parts() -> hyper::http::request::Parts {
    Request::builder()
        .method(Method::POST)
        .uri("/v1/messages")
        .body(())
        .expect("request")
        .into_parts()
        .0
}

fn body() -> Bytes {
    Bytes::from_static(
        br#"{"model":"glm-5.3","system":"s","messages":[{"role":"user","content":"hi"}]}"#,
    )
}

async fn text(response: Response<RespBody>) -> String {
    String::from_utf8_lossy(
        &response
            .into_body()
            .collect()
            .await
            .expect("body")
            .to_bytes(),
    )
    .into_owned()
}

async fn send(answer: Step) -> (Response<RespBody>, Arc<AtomicUsize>) {
    let reads = Arc::new(AtomicUsize::new(0));
    let once = Arc::new(std::sync::Mutex::new(Some(answer)));
    let upstream = FakeUpstream::start(move |_, _| {
        once.lock()
            .expect("lock")
            .take()
            .unwrap_or(Step::answer(200, "ok"))
    })
    .await;
    let lane = lane(&upstream.url(), Arc::clone(&reads));
    (lane.forward(parts(), body(), "glm-5.3").await, reads)
}

#[tokio::test]
async fn a_401_is_a_non_retried_400_and_the_key_is_forgotten() {
    let (response, reads) = send(Step::answer(401, "{\"error\":{\"message\":\"bad\"}}")).await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert_eq!(
        response.headers().get("x-should-retry").expect("header"),
        "false"
    );
    assert!(text(response).await.contains("Z.ai GLM answered HTTP 401"));
    // The next request reads the key again.
    assert_eq!(reads.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn a_402_is_a_balance_refusal_and_keeps_the_key() {
    let (response, reads) = send(Step::answer(402, "{}")).await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert!(text(response).await.contains("no balance or quota left"));
    assert_eq!(reads.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn a_plain_500_goes_through_with_its_status_and_body() {
    let (response, _) = send(Step::answer(500, "upstream exploded")).await;
    assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
    assert_eq!(text(response).await, "upstream exploded");
}

#[tokio::test]
async fn an_error_quoting_the_key_goes_out_with_the_key_blanked() {
    let (response, _) = send(Step::answer(500, format!("bad key {KEY} in request"))).await;
    assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
    let body = text(response).await;
    assert!(!body.contains(KEY), "{body}");
    assert!(body.contains("[key redacted]"), "{body}");
}

#[tokio::test]
async fn a_success_stream_with_a_zero_message_start_reaches_the_client_byte_identical() {
    let sse = "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":1}}}\r\n\r\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9,\"input_tokens\":0}}\n\n";
    let reads = Arc::new(AtomicUsize::new(0));
    let upstream = FakeUpstream::start(move |_, _| {
        Step::answer_with_headers(
            200,
            vec![("content-type".to_string(), "text/event-stream".to_string())],
            sse,
        )
    })
    .await;
    let lane = lane(&upstream.url(), reads);
    let response = lane.forward(parts(), body(), "glm-5.3").await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(text(response).await, sse);
}

#[tokio::test]
async fn a_json_success_reaches_the_client_byte_identical() {
    let (response, _) = send(Step::answer(200, "plain json body")).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(text(response).await, "plain json body");
}
