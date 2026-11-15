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
    FailureCode, IDLE_TIMEOUT, RequestBody, SEND_WINDOW, StreamedBody, UpstreamClient,
    UpstreamConfig, UpstreamRequest, forward_headers,
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

async fn big_stream() -> Arc<StreamedBody> {
    fill(StreamedBody::new(BIG as u64)).await
}

/// A streamed body whose replay prefix covers all of it, so it stays replayable.
async fn big_replayable_stream() -> Arc<StreamedBody> {
    fill(StreamedBody::with_limits(
        BIG as u64,
        BIG as u64,
        SEND_WINDOW,
    ))
    .await
}

async fn fill(s: Arc<StreamedBody>) -> Arc<StreamedBody> {
    let data = big_bytes();
    let mut at = 0;
    while at < BIG {
        let end = (at + 64 * 1024).min(BIG);
        s.push(data.slice(at..end)).await;
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
            RequestBody::Streamed(big_stream().await),
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
            RequestBody::Streamed(big_replayable_stream().await),
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

const KIB64: usize = 64 * 1024;
const MIB: usize = 1024 * 1024;

/// Bytes that depend on their offset, so a reordered, lost or doubled chunk changes the body.
fn patterned(len: usize) -> Bytes {
    (0..len as u64)
        .map(|at| (at ^ (at >> 8) ^ (at >> 16)) as u8)
        .collect::<Vec<u8>>()
        .into()
}

/// Pushes `data[from..to]` in 64 KiB chunks.
async fn push_range(stream: &StreamedBody, data: &Bytes, from: usize, to: usize) {
    let mut at = from;
    while at < to {
        let end = (at + KIB64).min(to);
        stream.push(data.slice(at..end)).await;
        at = end;
    }
}

/// Sends `data` as a streamed body that fills while the request goes out, holding it the way a
/// route does until the call returns. The last chunk waits until `release` says so, so the body
/// is still filling when the pooled socket dies and the attempt cannot count as handed over.
async fn send_filling(
    c: &UpstreamClient,
    url: &str,
    data: &Bytes,
    release: impl Fn() -> bool + Send + 'static,
) -> Result<huddle_router::upstream::UpstreamAnswer, huddle_router::upstream::Failure> {
    let stream = StreamedBody::new(data.len() as u64);
    let routing = stream.hold();
    let filler = {
        let stream = Arc::clone(&stream);
        let data = data.clone();
        tokio::spawn(async move {
            let tail = data.len() - KIB64;
            push_range(&stream, &data, 0, tail).await;
            while !release() {
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
            push_range(&stream, &data, tail, data.len()).await;
        })
    };
    let result = c
        .send(request(
            url,
            RequestBody::Streamed(stream),
            data.len() as u64,
        ))
        .await;
    drop(routing);
    filler.abort();
    result
}

#[tokio::test]
async fn l5_a_dead_reused_socket_under_a_small_body_is_retried_once() {
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadThenReset(100 * 1024),
        _ => Step::answer(200, "retried"),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let data = patterned(512 * 1024);
    let probe = upstream.clone();
    let answer = send_filling(&c, &url, &data, move || probe.connections() == 2)
        .await
        .expect("the retry succeeds");
    assert_eq!(answer.status.as_u16(), 200);
    assert_eq!(upstream.connections(), 2, "one fresh connection");
    let complete: Vec<_> = upstream
        .requests()
        .into_iter()
        .filter(|r| r.complete && r.body.len() == data.len())
        .collect();
    assert_eq!(complete.len(), 1, "one full copy");
    assert_eq!(complete[0].connection, 2);
    assert!(
        complete[0].body == data,
        "the retry sent the body byte for byte"
    );
}

#[tokio::test]
async fn l5_a_dead_reused_socket_under_a_large_body_is_not_retried() {
    let upstream = FakeUpstream::start(|conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadThenReset(2 * MIB),
        _ => Step::answer(200, "a second attempt"),
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let data = patterned(3 * MIB);
    let err = send_filling(&c, &url, &data, || false)
        .await
        .err()
        .expect("the client gets the cut");
    assert!(!err.client_closed);
    assert!(matches!(
        err.code,
        FailureCode::EConnReset | FailureCode::EPipe | FailureCode::EUpstream
    ));
    assert_eq!(upstream.connections(), 1, "no second attempt");
    let seen = upstream.requests();
    assert_eq!(seen.len(), 2, "the warm-up and the one large request");
    let cut = &seen[1];
    assert!(!cut.complete);
    assert!(cut.body.len() > MIB, "the cut came past the prefix");
    assert!(
        cut.body[..] == data[..cut.body.len()],
        "the bytes before the cut arrived in order"
    );
}

#[tokio::test]
async fn l5_retry_waits_for_the_first_pump_to_end() {
    let _heavy = HEAVY.lock().await;
    const PREFIX: usize = 6 * MIB;
    let (seen_tx, mut seen_rx) = tokio::sync::mpsc::unbounded_channel::<()>();
    let (go_tx, go_rx) = std::sync::mpsc::channel::<()>();
    let go_rx = std::sync::Mutex::new(go_rx);
    let upstream = FakeUpstream::start(move |conn, req| match (conn, req) {
        (1, 1) => Step::answer(200, "warm"),
        (1, 2) => Step::ReadThenReset(PREFIX),
        _ => {
            // The retry's head is in: hold its body until the test has offered the first chunk
            // past the prefix to whatever reader is still alive.
            let _ = seen_tx.send(());
            let _ = go_rx.lock().map(|rx| rx.recv());
            Step::answer(200, "retried")
        }
    })
    .await;
    let c = client();
    let url = format!("{}/v1/messages", upstream.url());
    warm(&c, &url).await;

    let data = patterned(BIG);
    let stream = StreamedBody::with_limits(BIG as u64, PREFIX as u64, SEND_WINDOW);
    let routing = stream.hold();
    let call = {
        let c = Arc::new(c);
        let stream = Arc::clone(&stream);
        let url = url.clone();
        tokio::spawn(async move {
            c.send(request(&url, RequestBody::Streamed(stream), BIG as u64))
                .await
                .map(|answer| answer.status.as_u16())
                .map_err(|failure| failure.code)
        })
    };
    push_range(&stream, &data, 0, PREFIX).await;
    tokio::time::timeout(Duration::from_secs(30), seen_rx.recv())
        .await
        .expect("the retry is dialled")
        .expect("the fake is alive");
    push_range(&stream, &data, PREFIX, PREFIX + KIB64).await;
    tokio::time::sleep(Duration::from_millis(100)).await;
    go_tx.send(()).expect("go");
    let rest = {
        let stream = Arc::clone(&stream);
        let data = data.clone();
        tokio::spawn(async move { push_range(&stream, &data, PREFIX + KIB64, BIG).await })
    };
    let status = call
        .await
        .expect("call")
        .expect("the retry replays the body");
    drop(routing);
    rest.await.expect("rest");
    assert_eq!(status, 200);
    let retried: Vec<_> = upstream
        .requests()
        .into_iter()
        .filter(|r| r.connection == 2)
        .collect();
    assert_eq!(retried.len(), 1);
    assert!(retried[0].complete, "never a half-sent second request");
    assert!(
        retried[0].body == data,
        "the retry sent the full body in order"
    );
}
