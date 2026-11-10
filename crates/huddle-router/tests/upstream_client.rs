use std::time::Duration;

use http_body_util::BodyExt;
use huddle_config::env::Env;
use huddle_router::upstream::{
    Failure, FailureCode, IDLE_TIMEOUT, RequestBody, StreamedBody, UpstreamClient, UpstreamConfig,
    UpstreamRequest, forward_headers, response_headers, upstream_url,
};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use hyper::body::Bytes;
use hyper::header::{CONTENT_LENGTH, HOST, HeaderMap, HeaderName, HeaderValue};
use hyper::{Method, Uri};
use rustls::RootCertStore;

fn client(roots: RootCertStore) -> UpstreamClient {
    UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots,
        idle: IDLE_TIMEOUT,
    })
}

fn client_idle(idle: Duration) -> UpstreamClient {
    UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle,
    })
}

fn post(url: &str, body: RequestBody, len: u64) -> UpstreamRequest {
    let uri: Uri = url.parse().expect("uri");
    let mut incoming = HeaderMap::new();
    incoming.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
    UpstreamRequest {
        method: Method::POST,
        headers: forward_headers(&incoming, &uri, len),
        url: uri,
        body,
    }
}

async fn read_all(answer: huddle_router::upstream::UpstreamAnswer) -> (u16, Vec<u8>) {
    let status = answer.status.as_u16();
    let bytes = answer
        .body
        .collect()
        .await
        .expect("body")
        .to_bytes()
        .to_vec();
    (status, bytes)
}

#[tokio::test]
async fn a_request_goes_out_with_its_body_and_headers() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let c = client(RootCertStore::empty());
    let url = format!("{}/v1/messages", upstream.url());
    let answer = c
        .send(post(&url, RequestBody::Whole(Bytes::from_static(b"{}")), 2))
        .await
        .expect("send");
    assert_eq!(read_all(answer).await, (200, b"ok".to_vec()));
    let seen = &upstream.requests()[0];
    assert_eq!(seen.method, "POST");
    assert_eq!(seen.path, "/v1/messages");
    assert_eq!(seen.body, b"{}");
    assert!(seen.complete);
    assert!(
        seen.headers
            .iter()
            .any(|(n, v)| n.eq_ignore_ascii_case("anthropic-version") && v == "2023-06-01")
    );
    assert!(
        seen.headers
            .iter()
            .any(|(n, v)| n.eq_ignore_ascii_case("content-length") && v == "2")
    );
    assert!(
        !seen
            .headers
            .iter()
            .any(|(n, _)| n.eq_ignore_ascii_case("user-agent")),
        "the router adds no header of its own"
    );
}

#[tokio::test]
async fn a_finished_connection_returns_to_the_pool_and_is_reused() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let c = client(RootCertStore::empty());
    let url = format!("{}/v1/messages", upstream.url());
    let first = c
        .send(post(&url, RequestBody::Whole(Bytes::from_static(b"{}")), 2))
        .await
        .expect("send");
    read_and_finish(first).await;
    tokio::time::sleep(Duration::from_millis(20)).await;
    let second = c
        .send(post(&url, RequestBody::Whole(Bytes::from_static(b"{}")), 2))
        .await
        .expect("send");
    read_and_finish(second).await;
    assert_eq!(
        upstream.connections(),
        1,
        "the second call reused the first socket"
    );
    assert_eq!(upstream.complete_requests(), 2);
}

async fn read_and_finish(answer: huddle_router::upstream::UpstreamAnswer) {
    let conn = answer.conn;
    let _ = answer.body.collect().await.expect("body");
    conn.finish();
}

#[tokio::test]
async fn a_large_body_is_handed_over_in_small_frames_and_arrives_whole() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let body = Bytes::from(vec![b'x'; 2 * 1024 * 1024]);
    let c = client(RootCertStore::empty());
    let url = format!("{}/v1/messages", upstream.url());
    let answer = c
        .send(post(
            &url,
            RequestBody::Whole(body.clone()),
            body.len() as u64,
        ))
        .await
        .expect("send");
    let _ = read_all(answer).await;
    assert_eq!(upstream.requests()[0].body.len(), 2 * 1024 * 1024);
    assert!(upstream.requests()[0].complete);
}

#[tokio::test]
async fn a_streamed_body_is_read_from_its_first_byte() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
    let streamed = StreamedBody::new(6);
    streamed.push(Bytes::from_static(b"ab"));
    let filler = streamed.clone();
    tokio::spawn(async move {
        tokio::time::sleep(Duration::from_millis(30)).await;
        filler.push(Bytes::from_static(b"cdef"));
    });
    let c = client(RootCertStore::empty());
    let url = format!("{}/v1/messages", upstream.url());
    let answer = c
        .send(post(&url, RequestBody::Streamed(streamed.clone()), 6))
        .await
        .expect("send");
    let _ = read_all(answer).await;
    assert_eq!(upstream.requests()[0].body, b"abcdef");
    assert!(streamed.is_whole());
}

#[tokio::test]
async fn a_tls_upstream_is_trusted_through_the_given_roots() {
    let upstream = FakeUpstream::start_tls(|_, _| Step::answer(200, "tls ok")).await;
    let mut roots = RootCertStore::empty();
    roots
        .add(rustls::pki_types::CertificateDer::from(upstream.ca_der()))
        .expect("root");
    let c = client(roots);
    let url = format!("{}/v1/messages", upstream.url());
    let answer = c
        .send(post(&url, RequestBody::Whole(Bytes::from_static(b"{}")), 2))
        .await
        .expect("send");
    assert_eq!(read_all(answer).await, (200, b"tls ok".to_vec()));
}

#[tokio::test]
async fn an_unknown_certificate_is_ecert() {
    let upstream = FakeUpstream::start_tls(|_, _| Step::answer(200, "x")).await;
    let c = client(RootCertStore::empty());
    let url = format!("{}/v1/messages", upstream.url());
    let err = c
        .send(post(&url, RequestBody::Whole(Bytes::from_static(b"{}")), 2))
        .await
        .err()
        .expect("refused");
    assert_eq!(
        err,
        Failure {
            code: FailureCode::ECert,
            client_closed: false
        }
    );
}

#[tokio::test]
async fn a_port_nobody_listens_on_is_econnrefused() {
    let probe = tokio::net::TcpListener::bind(("127.0.0.1", 0))
        .await
        .expect("bind");
    let port = probe.local_addr().expect("addr").port();
    drop(probe);
    let c = client(RootCertStore::empty());
    let err = c
        .send(post(
            &format!("http://127.0.0.1:{port}/v1/messages"),
            RequestBody::Whole(Bytes::new()),
            0,
        ))
        .await
        .err()
        .expect("refused");
    assert_eq!(err.code, FailureCode::EConnRefused);
    assert_eq!(err.code.as_str(), "ECONNREFUSED");
}

#[tokio::test]
async fn an_upstream_that_never_answers_times_out_as_etimedout() {
    let upstream = FakeUpstream::start(|_, _| Step::ReadAllThenHold).await;
    let c = client_idle(Duration::from_millis(100));
    let url = format!("{}/v1/messages", upstream.url());
    let err = c
        .send(post(&url, RequestBody::Whole(Bytes::from_static(b"{}")), 2))
        .await
        .err()
        .expect("times out");
    assert_eq!(err.code, FailureCode::ETimedOut);
}

#[test]
fn the_upstream_url_keeps_the_target_path_and_the_query() {
    let target: Uri = "https://api.z.ai/api/anthropic/".parse().expect("uri");
    assert_eq!(
        upstream_url(&target, "/v1/messages?beta=true")
            .expect("url")
            .to_string(),
        "https://api.z.ai/api/anthropic/v1/messages?beta=true"
    );
    let plain: Uri = "http://127.0.0.1:9/".parse().expect("uri");
    assert_eq!(
        upstream_url(&plain, "/v1/messages/count_tokens")
            .expect("url")
            .to_string(),
        "http://127.0.0.1:9/v1/messages/count_tokens"
    );
}

#[test]
fn hop_headers_are_dropped_and_host_and_length_are_set() {
    let mut h = HeaderMap::new();
    h.insert(HOST, HeaderValue::from_static("old"));
    h.insert(CONTENT_LENGTH, HeaderValue::from_static("9"));
    for name in [
        "connection",
        "keep-alive",
        "proxy-authorization",
        "proxy-connection",
        "te",
        "trailer",
        "transfer-encoding",
        "upgrade",
    ] {
        h.insert(HeaderName::from_static(name), HeaderValue::from_static("x"));
    }
    h.insert("anthropic-beta", HeaderValue::from_static("keep"));
    h.insert("authorization", HeaderValue::from_static("Bearer client"));
    let target: Uri = "https://api.z.ai/api/anthropic".parse().expect("uri");
    let out = forward_headers(&h, &target, 5);
    assert_eq!(out.get(HOST).expect("host"), "api.z.ai");
    assert_eq!(out.get(CONTENT_LENGTH).expect("len"), "5");
    assert_eq!(out.get("anthropic-beta").expect("beta"), "keep");
    assert_eq!(out.get("authorization").expect("auth"), "Bearer client");
    assert_eq!(out.len(), 4, "{out:?}");
}

#[test]
fn response_hop_headers_are_dropped_and_the_rest_kept() {
    let mut h = HeaderMap::new();
    for name in ["connection", "keep-alive", "transfer-encoding"] {
        h.insert(HeaderName::from_static(name), HeaderValue::from_static("x"));
    }
    h.insert("content-encoding", HeaderValue::from_static("gzip"));
    h.insert("request-id", HeaderValue::from_static("req_1"));
    let out = response_headers(&h);
    assert_eq!(out.len(), 2);
    assert_eq!(out.get("content-encoding").expect("ce"), "gzip");
}
