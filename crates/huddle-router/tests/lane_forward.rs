//! The provider lane: one request, the provider key, the body as it came.

use http_body_util::BodyExt;
use huddle_catalog::providers::provider_by_id;
use huddle_config::env::Env;
use huddle_keystore::port::{KeystoreError, SecretString};
use huddle_router::{
    keycache::{KeyRead, KeySources},
    lane::{Lane, LaneError},
    server::RespBody,
    upstream::{IDLE_TIMEOUT, StreamedBody, UpstreamClient, UpstreamConfig},
};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use hyper::body::Bytes;
use hyper::http::request::Parts;
use hyper::{Method, Request, Response, StatusCode};
use rustls::RootCertStore;
use std::sync::Arc;
use std::time::Duration;

fn client() -> Arc<UpstreamClient> {
    Arc::new(UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle: IDLE_TIMEOUT,
    }))
}

fn sources(key: Option<&'static str>) -> KeySources {
    let read: KeyRead = Arc::new(move || Ok(key.map(|k| SecretString::new(k.to_string()))));
    KeySources {
        env_key: None,
        os: None,
        file: read,
    }
}

fn lane_for(url: &str, key: Option<&'static str>) -> (Arc<Lane>, Arc<UpstreamClient>) {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_ROUTER_URL", url)]);
    let upstream = client();
    let lane = Lane::build(
        provider_by_id("zai").expect("zai"),
        &env,
        sources(key),
        Arc::clone(&upstream),
        Arc::new(|_| {}),
    )
    .expect("lane");
    (lane, upstream)
}

fn request(path: &str, headers: &[(&str, &str)]) -> Parts {
    let mut builder = Request::builder().method(Method::POST).uri(path);
    for (name, value) in headers {
        builder = builder.header(*name, *value);
    }
    builder.body(()).expect("request").into_parts().0
}

async fn body_of(response: Response<RespBody>) -> String {
    let bytes = response
        .into_body()
        .collect()
        .await
        .expect("body")
        .to_bytes();
    String::from_utf8_lossy(&bytes).into_owned()
}

fn has_header(seen: &[(String, String)], name: &str, value: &str) -> bool {
    seen.iter()
        .any(|(n, v)| n.eq_ignore_ascii_case(name) && v == value)
}

fn body() -> Bytes {
    Bytes::from_static(br#"{"model":"glm-5.3","messages":[]}"#)
}

#[tokio::test]
async fn a_provider_request_goes_with_the_bearer_key_and_nothing_of_the_clients_key() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _client) = lane_for(&upstream.url(), Some("test-key-test-key"));
    let response = lane
        .forward(
            request(
                "/v1/messages?a=1",
                &[
                    ("x-api-key", "claude-key"),
                    ("authorization", "Bearer claude-token"),
                    ("anthropic-version", "2023-06-01"),
                    ("accept-encoding", "gzip"),
                ],
            ),
            body(),
            "glm-5.3",
        )
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    let seen = &upstream.requests()[0];
    assert_eq!(seen.path, "/v1/messages?a=1");
    assert!(
        has_header(&seen.headers, "authorization", "Bearer test-key-test-key"),
        "{:?}",
        seen.headers
    );
    assert!(
        !seen.headers.iter().any(
            |(n, v)| n.eq_ignore_ascii_case("authorization") && v != "Bearer test-key-test-key"
        ),
        "{:?}",
        seen.headers
    );
    assert!(
        !seen
            .headers
            .iter()
            .any(|(n, _)| n.eq_ignore_ascii_case("x-api-key")),
        "{:?}",
        seen.headers
    );
    assert!(has_header(&seen.headers, "anthropic-version", "2023-06-01"));
    assert!(has_header(&seen.headers, "accept-encoding", "identity"));
}

#[tokio::test]
async fn a_body_with_no_edit_goes_out_byte_identical() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _client) = lane_for(&upstream.url(), Some("test-key-test-key"));
    let original = Bytes::from_static(
        br#"{ "model":"glm-5.3-flash","max_tokens":9999999,"tools":[{"type":"web_search_20250305"}],"cache_control":{"type":"ephemeral"},"context_management":{"x":1}}"#,
    );
    lane.forward(
        request("/v1/messages", &[]),
        original.clone(),
        "glm-5.3-flash",
    )
    .await;
    assert_eq!(upstream.requests()[0].body, original.to_vec());
}

#[tokio::test]
async fn a_streamed_body_goes_upstream_before_its_last_byte_arrives() {
    let upstream = Arc::new(FakeUpstream::start(|_, _| Step::answer(200, "ok")).await);
    let (lane, _client) = lane_for(&upstream.url(), Some("test-key-test-key"));
    let first = Bytes::from(vec![b'x'; 64 * 1024]);
    let last = Bytes::from(vec![b'y'; 64 * 1024]);
    let total = first.len() + last.len();
    let body = StreamedBody::new(total as u64);
    body.push(first.clone());
    let sending = tokio::spawn({
        let body = Arc::clone(&body);
        async move {
            lane.forward_streamed(request("/v1/messages", &[]), body, "glm-5.3")
                .await
        }
    });
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    while upstream.received_bytes() == 0 {
        assert!(
            tokio::time::Instant::now() < deadline,
            "nothing reached the provider"
        );
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    assert!(
        upstream.received_bytes() < total,
        "the last chunk was not pushed yet"
    );
    body.push(last);
    let response = sending.await.expect("the task");
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(upstream.requests()[0].body.len(), total);
}

#[tokio::test]
async fn a_count_tokens_request_reaches_the_provider_like_any_other() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _client) = lane_for(&upstream.url(), Some("test-key-test-key"));
    lane.forward(request("/v1/messages/count_tokens", &[]), body(), "glm-5.3")
        .await;
    assert_eq!(upstream.requests()[0].path, "/v1/messages/count_tokens");
}

#[tokio::test]
async fn the_answer_comes_back_as_the_provider_sent_it() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "hello")).await;
    let (lane, _client) = lane_for(&upstream.url(), Some("test-key-test-key"));
    let response = lane
        .forward(request("/v1/messages", &[]), body(), "glm-5.3")
        .await;
    assert_eq!(body_of(response).await, "hello");
}

#[tokio::test]
async fn a_threads_beta_is_refused_without_reaching_the_provider() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _client) = lane_for(&upstream.url(), Some("test-key-test-key"));
    let response = lane
        .forward(
            request(
                "/v1/messages",
                &[("anthropic-beta", "message-threads-2026-08-12")],
            ),
            body(),
            "glm-5.3",
        )
        .await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert!(upstream.requests().is_empty());
    assert!(
        body_of(response)
            .await
            .contains("keeps no server-side message thread")
    );
}

#[tokio::test]
async fn no_key_is_a_400_naming_setup_and_nothing_goes_out() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _client) = lane_for(&upstream.url(), None);
    let response = lane
        .forward(request("/v1/messages", &[]), body(), "glm-5.3")
        .await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert_eq!(
        response.headers().get("x-should-retry").expect("header"),
        "false"
    );
    assert!(upstream.requests().is_empty());
    assert!(
        body_of(response)
            .await
            .contains("run /huddle:zai:setup (no key is set)")
    );
}

#[tokio::test]
async fn an_unusable_key_file_says_so() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let url = upstream.url();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_ROUTER_URL", url.as_str())]);
    let read: KeyRead = Arc::new(|| {
        Err(KeystoreError::InsecureFile {
            path: "/x/env".into(),
            mode: 0o644,
        })
    });
    let lane = Lane::build(
        provider_by_id("zai").expect("zai"),
        &env,
        KeySources {
            env_key: None,
            os: None,
            file: read,
        },
        client(),
        Arc::new(|_| {}),
    )
    .expect("lane");
    let response = lane
        .forward(request("/v1/messages", &[]), body(), "glm-5.3")
        .await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert!(body_of(response).await.contains("chmod 600"));
}

#[tokio::test]
async fn an_upstream_that_is_not_https_or_loopback_is_refused() {
    let (lane, _client) = lane_for("http://provider.example:80", Some("test-key-test-key"));
    let response = lane
        .forward(request("/v1/messages", &[]), body(), "glm-5.3")
        .await;
    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    assert!(
        body_of(response)
            .await
            .contains("provider upstream must be https unless it is loopback")
    );
}

#[tokio::test]
async fn a_key_that_cannot_become_a_header_refuses_and_sends_nothing() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let (lane, _client) = lane_for(&upstream.url(), Some("bad-key\nsecond-line"));
    let response = lane
        .forward(
            request("/v1/messages", &[("authorization", "Bearer claude-token")]),
            body(),
            "glm-5.3",
        )
        .await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    assert!(upstream.requests().is_empty());
    let answer = body_of(response).await;
    assert!(!answer.contains("bad-key"), "{answer}");
    assert!(!answer.contains("claude-token"), "{answer}");
}

#[tokio::test]
async fn an_upstream_that_refuses_the_connection_is_a_502_naming_the_code() {
    let free = std::net::TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    let (lane, _client) = lane_for(
        &format!("http://127.0.0.1:{free}"),
        Some("test-key-test-key"),
    );
    let response = lane
        .forward(request("/v1/messages", &[]), body(), "glm-5.3")
        .await;
    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    assert!(
        body_of(response)
            .await
            .contains("provider unreachable (ECONNREFUSED)")
    );
}

#[tokio::test]
async fn a_router_url_that_does_not_parse_names_its_variable() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_ROUTER_URL", "not a url")]);
    let result = Lane::build(
        provider_by_id("zai").expect("zai"),
        &env,
        sources(None),
        client(),
        Arc::new(|_| {}),
    );
    match result {
        Err(LaneError { name, .. }) => assert_eq!(name, "ZAI_ROUTER_URL"),
        Ok(_) => panic!("a bad URL must not build a lane"),
    }
}

#[tokio::test]
async fn model_env_overrides_change_the_ids_the_lane_answers_to() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), ("ZAI_MODEL_MAIN", "my-glm")]);
    let lane = Lane::build(
        provider_by_id("zai").expect("zai"),
        &env,
        sources(None),
        client(),
        Arc::new(|_| {}),
    )
    .expect("lane");
    assert_eq!(lane.resolved_ids().main, "my-glm");
    assert_eq!(lane.resolved_ids().flash, "glm-5.3-flash");
}
