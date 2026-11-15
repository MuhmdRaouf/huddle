use http_body_util::BodyExt;
use huddle_catalog::routing::decide_resolved;
use huddle_config::env::Env;
use huddle_pki::ca::{ROUTER_HOST, load_or_create};
use huddle_pki::leaf::LeafCache;
use huddle_router::anthropic::{AnthropicPath, STREAM_HEAD_BYTES};
use huddle_router::connect::{ConnectCtx, Decrypt, TunnelTracker, connect_handler};
use huddle_router::server::{BODY_LIMIT, Handler, ServerLimits, bind, full_body, serve};
use huddle_router::upstream::{
    IDLE_TIMEOUT, REPLAY_PREFIX, RequestBody, SEND_WINDOW, StreamedBody, UpstreamClient,
    UpstreamConfig, UpstreamRequest, forward_headers,
};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::temp_home::TempHome;
use hyper::body::Bytes;
use hyper::header::HeaderMap;
use hyper::{Method, Request, Response, Uri};
use rustls::RootCertStore;
use rustls::pki_types::{CertificateDer, ServerName};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

struct Rig {
    port: u16,
    ca_der: Vec<u8>,
    upstream: FakeUpstream,
    whole_calls: Arc<Mutex<Vec<Vec<u8>>>>,
    streamed_calls: Arc<Mutex<Vec<(String, u64)>>>,
    streams: Arc<Mutex<Vec<Arc<StreamedBody>>>>,
    _home: TempHome,
}

fn message(model: &str, size: usize) -> Vec<u8> {
    let head =
        format!(r#"{{"model":"{model}","max_tokens":16,"messages":[{{"role":"user","content":""#);
    let tail = r#""}]}"#;
    let padding = size.saturating_sub(head.len() + tail.len());
    let mut body = head.into_bytes();
    body.extend(std::iter::repeat_n(b'x', padding));
    body.extend_from_slice(tail.as_bytes());
    body
}

fn stub_handler(tag: &'static str) -> Handler {
    Arc::new(move |_req: Request<hyper::body::Incoming>| {
        Box::pin(async move { Response::new(full_body(tag)) })
    })
}

async fn rig(
    plan: impl Fn(usize, usize) -> Step + Send + Sync + 'static,
    idle: Duration,
    limit: u64,
) -> Rig {
    rig_with_lane(plan, idle, limit, None).await
}

/// The rig, with the stub lane sending to `lane` instead of the fake upstream.
async fn rig_with_lane(
    plan: impl Fn(usize, usize) -> Step + Send + Sync + 'static,
    idle: Duration,
    limit: u64,
    lane: Option<String>,
) -> Rig {
    let home = TempHome::new();
    let dir = home.root().join(".local/share/huddle/ca");
    let ca = load_or_create(&dir, SystemTime::now()).expect("ca");
    let upstream = FakeUpstream::start(plan).await;
    let client = Arc::new(UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle,
    }));
    let streamed_calls: Arc<Mutex<Vec<(String, u64)>>> = Arc::default();
    let streamed_sink = streamed_calls.clone();
    let route_client = client.clone();
    let lane_url: Uri = format!("{}/v1/messages", lane.unwrap_or_else(|| upstream.url()))
        .parse()
        .expect("lane url");
    let streams: Arc<Mutex<Vec<Arc<StreamedBody>>>> = Arc::default();
    let stream_sink = streams.clone();
    // The stub lane relays the body to the fake upstream the way a lane does, so a body past the
    // send window drains at the upstream's pace instead of stalling the filler.
    let streamed_route: huddle_router::anthropic::StreamedRoute =
        Arc::new(move |_parts, stream, _provider, model| {
            let sink = streamed_sink.clone();
            let client = route_client.clone();
            let url = lane_url.clone();
            stream_sink.lock().unwrap().push(Arc::clone(&stream));
            Box::pin(async move {
                sink.lock().unwrap().push((model, stream.filled()));
                let len = stream.len();
                let request = UpstreamRequest {
                    method: Method::POST,
                    headers: forward_headers(&HeaderMap::new(), &url, len),
                    url,
                    body: RequestBody::Streamed(stream),
                };
                let mut status = hyper::StatusCode::OK;
                if let Ok(answer) = client.send(request).await {
                    status = answer.status;
                    let conn = answer.conn;
                    let _ = answer.body.collect().await;
                    conn.finish();
                }
                let mut response = Response::new(full_body("streamed-route"));
                *response.status_mut() = status;
                response
            })
        });
    let whole_calls: Arc<Mutex<Vec<Vec<u8>>>> = Arc::default();
    let sink = whole_calls.clone();
    let whole_route: huddle_router::anthropic::WholeRoute =
        Arc::new(move |_parts, body: Bytes, _decided| {
            let sink = sink.clone();
            Box::pin(async move {
                sink.lock().unwrap().push(body.to_vec());
                Response::new(full_body("whole-route"))
            })
        });
    let path = Arc::new(AnthropicPath {
        client,
        target: upstream.url().parse().expect("target"),
        body_limit: limit,
        route_of: Arc::new(|model: &str| decide_resolved(model, &["zai", "qwen"], &[])),
        streamed_route,
        whole_route,
        pass_through: stub_handler("pass"),
        on_outcome: None,
    });
    let intercept = path.intercept_handler();
    let leaf = Arc::new(LeafCache::new(dir, Arc::new(|_| {})));
    let ctx = Arc::new(ConnectCtx {
        env: Env::with(&[]),
        dial: None,
        tunnels: TunnelTracker::new(),
        decrypt: Some(Arc::new(Decrypt::new(
            leaf,
            intercept,
            ServerLimits::default(),
        ))),
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, connect_handler(ctx), ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        port,
        ca_der: ca.cert_der().to_vec(),
        upstream,
        whole_calls,
        streamed_calls,
        streams,
        _home: home,
    }
}

async fn tls_session(rig: &Rig) -> impl AsyncRead + AsyncWrite + Unpin + use<> {
    let mut tcp = TcpStream::connect(("127.0.0.1", rig.port))
        .await
        .expect("connect");
    tcp.write_all(
        format!("CONNECT {ROUTER_HOST}:443 HTTP/1.1\r\nHost: {ROUTER_HOST}:443\r\n\r\n").as_bytes(),
    )
    .await
    .expect("connect head");
    let mut head = Vec::new();
    let mut byte = [0u8; 1];
    while !head.ends_with(b"\r\n\r\n") {
        tcp.read_exact(&mut byte).await.expect("head");
        head.push(byte[0]);
    }
    let mut roots = RootCertStore::empty();
    roots
        .add(CertificateDer::from(rig.ca_der.clone()))
        .expect("root");
    let config = rustls::ClientConfig::builder_with_provider(Arc::new(
        rustls::crypto::ring::default_provider(),
    ))
    .with_safe_default_protocol_versions()
    .expect("versions")
    .with_root_certificates(roots)
    .with_no_client_auth();
    let name = ServerName::try_from(ROUTER_HOST.to_string()).expect("name");
    tokio_rustls::TlsConnector::from(Arc::new(config))
        .connect(name, tcp)
        .await
        .expect("handshake")
}

fn post_head(len: usize) -> String {
    format!(
        "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Type: application/json\r\nContent-Length: {len}\r\nConnection: close\r\n\r\n"
    )
}

async fn read_to_end(mut tls: impl AsyncRead + Unpin) -> String {
    let mut out = Vec::new();
    let _ = tls.read_to_end(&mut out).await;
    String::from_utf8_lossy(&out).into_owned()
}

async fn wait_for(limit: Duration, mut pred: impl FnMut() -> bool) {
    let deadline = tokio::time::Instant::now() + limit;
    while !pred() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "condition not met in time"
        );
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
}

#[tokio::test]
async fn the_upstream_reads_the_first_bytes_before_the_client_sends_the_last() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let body = message("claude-opus-5-5", 512 * 1024);
    let half = body.len() / 2;
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body[..half]).await.expect("first half");

    wait_for(Duration::from_secs(5), || rig.upstream.received_bytes() > 0).await;
    assert!(
        rig.upstream.received_bytes() < body.len(),
        "the second half was not sent yet"
    );

    tls.write_all(&body[half..]).await.expect("second half");
    let resp = read_to_end(tls).await;
    assert!(resp.starts_with("HTTP/1.1 200"), "{resp}");
    let seen = rig.upstream.requests();
    assert_eq!(seen.len(), 1);
    assert_eq!(
        seen[0].body, body,
        "the upstream got exactly the bytes sent"
    );
    assert!(
        rig.whole_calls.lock().unwrap().is_empty(),
        "a claude model never takes the whole route"
    );
}

#[tokio::test]
async fn a_body_inside_the_first_64_kib_is_read_whole() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let body = message("claude-opus-5-5", STREAM_HEAD_BYTES - 1);
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body).await.expect("body");
    let resp = read_to_end(tls).await;
    assert!(resp.contains("whole-route"), "{resp}");
    assert_eq!(
        rig.upstream.requests().len(),
        0,
        "nothing relayed directly by this path"
    );
    assert_eq!(rig.whole_calls.lock().unwrap()[0], body);
}

#[tokio::test]
async fn a_lane_that_needs_no_edit_gets_the_body_as_it_arrives() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let body = message("glm-5.3-flash", 512 * 1024);
    let half = body.len() / 2;
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body[..half]).await.expect("first half");
    wait_for(Duration::from_secs(5), || {
        !rig.streamed_calls.lock().unwrap().is_empty()
    })
    .await;
    {
        let calls = rig.streamed_calls.lock().unwrap();
        assert_eq!(calls[0].0, "glm-5.3-flash");
        assert!(
            calls[0].1 < body.len() as u64,
            "the lane was called before the last byte arrived"
        );
    }
    tls.write_all(&body[half..]).await.expect("second half");
    let resp = read_to_end(tls).await;
    assert!(resp.contains("streamed-route"), "{resp}");
    assert!(
        rig.whole_calls.lock().unwrap().is_empty(),
        "the lane never takes the whole route"
    );
}

#[tokio::test]
async fn a_lane_that_edits_bodies_is_read_whole_before_anything_goes_upstream() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let body = message("qwen3.8-max", 512 * 1024);
    let half = body.len() / 2;
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body[..half]).await.expect("first half");
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(
        rig.upstream.connections(),
        0,
        "the lane body waits for the whole body"
    );
    assert!(rig.whole_calls.lock().unwrap().is_empty());
    tls.write_all(&body[half..]).await.expect("second half");
    let resp = read_to_end(tls).await;
    assert!(resp.contains("whole-route"), "{resp}");
    assert_eq!(rig.whole_calls.lock().unwrap()[0], body);
}

#[tokio::test]
async fn a_client_that_stops_short_ends_the_call_without_a_retry() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let body = message("claude-opus-5-5", 600 * 1024);
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body[..body.len() / 2]).await.expect("half");
    wait_for(Duration::from_secs(5), || rig.upstream.received_bytes() > 0).await;
    drop(tls);
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert!(
        rig.upstream.requests().iter().all(|r| !r.complete),
        "no complete request"
    );
    assert!(
        rig.upstream.connections() <= 1,
        "no retry on a new connection"
    );
}

#[tokio::test]
async fn a_body_over_the_limit_is_413_with_nothing_sent() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, 128 * 1024).await;
    let body = message("claude-opus-5-5", 300 * 1024);
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    let _ = tls.write_all(&body).await;
    let resp = read_to_end(tls).await;
    assert!(resp.starts_with("HTTP/1.1 413"), "{resp}");
    assert_eq!(rig.upstream.connections(), 0);
}

#[tokio::test]
async fn the_silence_budget_does_not_count_the_clients_upload() {
    let rig = rig(
        |_, _| Step::answer(200, "ok"),
        Duration::from_millis(150),
        BODY_LIMIT,
    )
    .await;
    let body = message("claude-opus-5-5", 512 * 1024);
    let half = body.len() / 2;
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body[..half]).await.expect("first half");
    tokio::time::sleep(Duration::from_millis(400)).await;
    tls.write_all(&body[half..]).await.expect("second half");
    let resp = read_to_end(tls).await;
    assert!(
        resp.starts_with("HTTP/1.1 200"),
        "a slow upload is not silence: {resp}"
    );
}

#[tokio::test]
async fn a_non_model_request_takes_the_pass_through() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let mut tls = tls_session(&rig).await;
    tls.write_all(
        b"GET /v1/models HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n",
    )
    .await
    .expect("req");
    assert!(read_to_end(tls).await.contains("pass"));
}

#[tokio::test]
async fn l5_the_stub_lane_drains_a_body_past_the_send_window() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    let body = message("glm-5.3-flash", 2 * 1024 * 1024);
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body).await.expect("body");
    let resp = read_to_end(tls).await;
    assert!(resp.contains("streamed-route"), "{resp}");
    let seen = rig.upstream.requests();
    assert_eq!(seen.len(), 1);
    assert!(seen[0].body == body, "the lane relayed the whole body");
}

/// A plain HTTP upstream that answers 400 as soon as a request head is in and closes, the way an
/// upstream refuses a request early; it reads nothing past the head.
async fn early_refusal() -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let addr = listener.local_addr().expect("addr");
    tokio::spawn(async move {
        while let Ok((mut socket, _)) = listener.accept().await {
            tokio::spawn(async move {
                let mut head = Vec::new();
                let mut byte = [0u8; 1];
                while !head.ends_with(b"\r\n\r\n") {
                    if socket.read_exact(&mut byte).await.is_err() {
                        return;
                    }
                    head.push(byte[0]);
                }
                let answer = "HTTP/1.1 400 Bad Request\r\nContent-Length: 11\r\nConnection: close\r\n\r\nbad request";
                let _ = socket.write_all(answer.as_bytes()).await;
                let _ = socket.shutdown().await;
                // Drains what is in flight so the close is a FIN, not a reset.
                let mut sink = vec![0u8; 64 * 1024];
                let deadline = tokio::time::Instant::now() + Duration::from_millis(200);
                while let Ok(Ok(read)) =
                    tokio::time::timeout_at(deadline, socket.read(&mut sink)).await
                {
                    if read == 0 {
                        break;
                    }
                }
            });
        }
    });
    format!("http://{addr}")
}

#[tokio::test]
async fn l36_fast_4xx_does_not_buffer_the_upload() {
    let lane = early_refusal().await;
    let rig = rig_with_lane(
        |_, _| Step::answer(200, "ok"),
        IDLE_TIMEOUT,
        BODY_LIMIT,
        Some(lane),
    )
    .await;
    let body = message("glm-5.3-flash", 20 * 1024 * 1024);
    let (read_half, mut write_half) = tokio::io::split(tls_session(&rig).await);
    let answer = tokio::spawn(read_to_end(read_half));
    write_half
        .write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    // A push that waits has already added its frame, so the body holds one frame more until it
    // returns.
    let limit = REPLAY_PREFIX + SEND_WINDOW + 64 * 1024;
    let mut most = 0;
    for part in body.chunks(256 * 1024) {
        write_half.write_all(part).await.expect("body");
        if let Some(stream) = rig.streams.lock().unwrap().first() {
            most = most.max(stream.retained_bytes());
        }
    }
    write_half.flush().await.expect("flush");
    let stream = rig
        .streams
        .lock()
        .unwrap()
        .first()
        .cloned()
        .expect("relayed");
    wait_for(Duration::from_secs(10), || stream.is_whole()).await;
    most = most.max(stream.retained_bytes());
    let resp = answer.await.expect("answer");
    assert!(resp.starts_with("HTTP/1.1 400"), "{resp}");
    assert!(most <= limit, "held {most} bytes of an upload nobody reads");
}

#[tokio::test]
async fn l36_whole_body_path_is_untouched() {
    let rig = rig(|_, _| Step::answer(200, "ok"), IDLE_TIMEOUT, BODY_LIMIT).await;
    // The model comes after the first 64 KiB, so the route reads the body whole first.
    let padding = "x".repeat(3 * 1024 * 1024);
    let body = format!(
        r#"{{"messages":[{{"role":"user","content":"{padding}"}}],"model":"claude-sonnet-5-5"}}"#
    )
    .into_bytes();
    let mut tls = tls_session(&rig).await;
    tls.write_all(&post_head(body.len()).into_bytes())
        .await
        .expect("head");
    tls.write_all(&body).await.expect("body");
    tls.flush().await.expect("flush");
    let resp = read_to_end(tls).await;
    assert!(resp.contains("whole-route"), "{resp}");
    let calls = rig.whole_calls.lock().unwrap();
    assert_eq!(calls.len(), 1);
    assert!(calls[0] == body, "the whole route got the full body");
}
