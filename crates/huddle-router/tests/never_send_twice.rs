//! Never send twice: a reused socket is retried once, and only before the whole body reached the
//! transport.
//!
//! Six tests push an 8 MiB body through one fake upstream each. They hold `HEAVY` while they run,
//! so at most one big body is in flight at a time; several concurrent drains starve a fake's
//! read loop past its read timeout, and the request it then records is incomplete through no
//! fault of the client.

use std::sync::Arc;
use std::time::Duration;

use http_body_util::BodyExt;
use huddle_config::env::Env;
use huddle_router::upstream::{
    FailureCode, IDLE_TIMEOUT, RequestBody, StreamedBody, UpstreamClient, UpstreamConfig,
    UpstreamRequest, forward_headers,
};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use hyper::body::Bytes;
use hyper::header::HeaderMap;
use hyper::{Method, Uri};
use rustls::RootCertStore;
use tokio::sync::Mutex;
use tokio::task::yield_now;

/// Held by every test that pushes an 8 MiB body, so the big drains never overlap.
static HEAVY: Mutex<()> = Mutex::const_new(());

fn client() -> UpstreamClient {
    UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle: IDLE_TIMEOUT,
    })
}

fn request(url: &str, body: RequestBody, len: u64) -> UpstreamRequest {
    let uri: Uri = url.parse().expect("uri");
    UpstreamRequest {
        method: Method::POST,
        headers: forward_headers(&HeaderMap::new(), &uri, len),
        url: uri,
        body,
    }
}

const BIG: usize = 8 * 1024 * 1024;

fn big_bytes() -> Bytes {
    Bytes::from(vec![b'p'; BIG])
}

fn big_stream() -> Arc<StreamedBody> {
    let s = StreamedBody::new(BIG as u64);
    let data = big_bytes();
    let mut at = 0;
    while at < BIG {
        let end = (at + 64 * 1024).min(BIG);
        s.push(data.slice(at..end));
        at = end;
    }
    s
}

// A warm-up keeps socket 1 open and idle in the pool, so the next attempt is on a reused socket.
async fn warm(c: &UpstreamClient, url: &str) {
    let first = c
        .send(request(
            url,
            RequestBody::Whole(Bytes::from_static(b"{}")),
            2,
        ))
        .await
        .expect("warm");
    let conn = first.conn;
    let _ = first.body.collect().await.expect("warm body");
    conn.finish();
    let uri: Uri = url.parse().expect("uri");
    while c.idle_sockets(&uri) != 1 {
        yield_now().await;
    }
}

fn full_copies_of_big(upstream: &FakeUpstream) -> usize {
    upstream
        .requests()
        .iter()
        .filter(|r| r.complete && r.body.len() == BIG)
        .count()
}

#[tokio::test]
async fn an_upstream_that_reads_the_whole_body_and_closes_gets_exactly_one_full_request() {
    let _heavy = HEAVY.lock().await;
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadAllThenClose,
        _ => Step::answer(200, "a second attempt"),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let err = c
        .send(request(&url, RequestBody::Whole(big_bytes()), BIG as u64))
        .await
        .err()
        .expect("the call fails");
    assert!(!err.client_closed);
    assert_eq!(
        full_copies_of_big(&upstream),
        1,
        "one full request reached the upstream"
    );
    assert_eq!(upstream.connections(), 1, "no retry on another socket");
}

#[tokio::test]
async fn a_streamed_body_that_was_handed_over_is_not_sent_twice() {
    let _heavy = HEAVY.lock().await;
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadAllThenClose,
        _ => Step::answer(200, "a second attempt"),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let err = c
        .send(request(
            &url,
            RequestBody::Streamed(big_stream()),
            BIG as u64,
        ))
        .await
        .err()
        .expect("the call fails");
    assert!(!err.client_closed);
    assert_eq!(full_copies_of_big(&upstream), 1);
    assert_eq!(upstream.connections(), 1);
}

#[tokio::test]
async fn a_reset_before_the_full_body_reached_the_transport_retries_once_on_a_fresh_connection() {
    let _heavy = HEAVY.lock().await;
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadThenReset(100 * 1024),
        _ => Step::answer(200, "retried"),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let answer = c
        .send(request(&url, RequestBody::Whole(big_bytes()), BIG as u64))
        .await
        .expect("the retry succeeds");
    assert_eq!(answer.status.as_u16(), 200);
    assert_eq!(upstream.connections(), 2, "exactly one fresh connection");
    assert_eq!(
        full_copies_of_big(&upstream),
        1,
        "the fresh attempt delivered one complete copy"
    );
}

#[tokio::test]
async fn a_streamed_body_replays_from_byte_zero_on_the_fresh_connection() {
    let _heavy = HEAVY.lock().await;
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadThenReset(100 * 1024),
        _ => Step::answer(200, "retried"),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let answer = c
        .send(request(
            &url,
            RequestBody::Streamed(big_stream()),
            BIG as u64,
        ))
        .await
        .expect("the retry succeeds");
    assert_eq!(answer.status.as_u16(), 200);
    assert_eq!(upstream.connections(), 2);
    assert_eq!(full_copies_of_big(&upstream), 1);
    let full = upstream
        .requests()
        .into_iter()
        .find(|r| r.complete && r.body.len() == BIG)
        .expect("the full copy");
    assert!(full.body.iter().all(|b| *b == b'p'));
}

#[tokio::test]
async fn a_reset_on_a_socket_the_pool_never_used_is_not_retried() {
    let _heavy = HEAVY.lock().await;
    let upstream = FakeUpstream::start(|_, _| Step::ReadThenReset(100 * 1024)).await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    let err = c
        .send(request(&url, RequestBody::Whole(big_bytes()), BIG as u64))
        .await
        .err()
        .expect("no retry");
    assert!(matches!(
        err.code,
        FailureCode::EConnReset | FailureCode::EPipe | FailureCode::EUpstream
    ));
    assert_eq!(upstream.connections(), 1, "a fresh socket is never retried");
}

#[tokio::test]
async fn the_retry_gets_one_attempt_and_reports_once() {
    let _heavy = HEAVY.lock().await;
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        _ => Step::ReadThenReset(100 * 1024),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;
    let err = c
        .send(request(&url, RequestBody::Whole(big_bytes()), BIG as u64))
        .await
        .err()
        .expect("both attempts fail");
    assert!(matches!(
        err.code,
        FailureCode::EConnReset | FailureCode::EPipe | FailureCode::EUpstream
    ));
    assert_eq!(
        upstream.connections(),
        2,
        "one stale reuse and one fresh retry, no more"
    );
    assert_eq!(
        full_copies_of_big(&upstream),
        0,
        "neither attempt carried the whole body"
    );
}

#[tokio::test]
async fn a_socket_the_upstream_closed_while_idle_is_replaced_before_a_byte_is_sent() {
    let upstream = FakeUpstream::start(|conn, _| {
        if conn == 1 {
            Step::answer_and_close(200, "warm")
        } else {
            Step::answer(200, "fresh")
        }
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    let first = c
        .send(request(
            &url,
            RequestBody::Whole(Bytes::from_static(b"{}")),
            2,
        ))
        .await
        .expect("first");
    let conn = first.conn;
    let _ = first.body.collect().await.expect("first body");
    conn.finish();
    // The fake closed socket 1 with the answer; wait until the pool reads it as gone.
    let uri: Uri = url.parse().expect("uri");
    while c.idle_sockets(&uri) != 0 {
        yield_now().await;
    }

    let answer = c
        .send(request(
            &url,
            RequestBody::Whole(Bytes::from_static(b"{}")),
            2,
        ))
        .await
        .expect("no failure");
    assert_eq!(answer.status.as_u16(), 200);
    assert_eq!(upstream.connections(), 2);
    assert_eq!(
        upstream.complete_requests(),
        2,
        "the stale socket got no bytes of the second request"
    );
}

#[tokio::test]
async fn a_timeout_or_a_refused_connection_is_never_retried() {
    let upstream = FakeUpstream::start(|_, _| Step::ReadAllThenHold).await;
    let c = UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle: Duration::from_millis(100),
    });
    let url = format!("{}/v1/messages", upstream.url());
    let err = c
        .send(request(
            &url,
            RequestBody::Whole(Bytes::from_static(b"{}")),
            2,
        ))
        .await
        .err()
        .expect("timeout");
    assert_eq!(err.code, FailureCode::ETimedOut);
    assert_eq!(upstream.connections(), 1);
}
