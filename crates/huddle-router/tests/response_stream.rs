use huddle_config::env::Env;
use huddle_router::anthropic::{AnswerOutcome, AnthropicPath, OutcomeHook};
use huddle_router::server::{BODY_LIMIT, Handler, ServerLimits, bind, full_body, receive, serve};
use huddle_router::upstream::{FailureCode, IDLE_TIMEOUT, UpstreamClient, UpstreamConfig};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use hyper::body::Bytes;
use hyper::{Request, Response};
use rustls::RootCertStore;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

struct Rig {
    port: u16,
    upstream: FakeUpstream,
    outcomes: Arc<Mutex<Vec<AnswerOutcome>>>,
}

async fn rig(plan: impl Fn(usize, usize) -> Step + Send + Sync + 'static, idle: Duration) -> Rig {
    let upstream = FakeUpstream::start(plan).await;
    let client = Arc::new(UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle,
    }));
    let outcomes: Arc<Mutex<Vec<AnswerOutcome>>> = Arc::default();
    let sink = outcomes.clone();
    let on_outcome: OutcomeHook = Arc::new(move |o| sink.lock().unwrap().push(o));
    let path = Arc::new(AnthropicPath {
        client,
        target: upstream.url().parse().expect("target"),
        body_limit: BODY_LIMIT,
        is_anthropic: Arc::new(|_| true),
        lane_streams: Arc::new(|_| false),
        streamed_route: Arc::new(|_, _, _| Box::pin(async { Response::new(full_body("x")) })),
        whole_route: Arc::new(|_, _| Box::pin(async { Response::new(full_body("x")) })),
        pass_through: Arc::new(|_req| Box::pin(async { Response::new(full_body("x")) })),
        on_outcome: Some(on_outcome),
    });
    let handler: Handler = Arc::new(move |req: Request<hyper::body::Incoming>| {
        let path = path.clone();
        Box::pin(async move {
            match receive(req, BODY_LIMIT).await {
                Ok((parts, body)) => path.forward_whole(parts, body).await,
                Err(resp) => resp,
            }
        })
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        port,
        upstream,
        outcomes,
    }
}

async fn post(port: u16) -> TcpStream {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let body = r#"{"model":"claude-opus-5-5"}"#;
    s.write_all(
        format!(
            "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
            body.len()
        )
        .as_bytes(),
    )
    .await
    .expect("write");
    s
}

/// One answer read to EOF and de-framed: the head with a chunked body reassembled into one
/// body, and whether the answer ended clean. Clean means the chunked terminator arrived, or
/// an identity body reached its declared length; a socket that stops short reads as cut.
async fn read_answer(mut s: TcpStream) -> (String, bool) {
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    let raw = String::from_utf8_lossy(&out).into_owned();
    let Some(head_end) = raw.find("\r\n\r\n").map(|at| at + 4) else {
        return (raw, false);
    };
    let (head, rest) = raw.split_at(head_end);
    let lower = head.to_ascii_lowercase();
    if lower.contains("transfer-encoding: chunked") {
        let mut body = String::new();
        let mut rest = rest;
        let mut ended = false;
        while let Some(line) = rest.find("\r\n") {
            let Ok(size) = usize::from_str_radix(&rest[..line], 16) else {
                break;
            };
            let data = &rest[line + 2..];
            if size == 0 {
                ended = true;
                break;
            }
            if data.len() < size + 2 {
                break;
            }
            body.push_str(&data[..size]);
            rest = &data[size + 2..];
        }
        (format!("{head}{body}"), ended)
    } else {
        let declared = lower
            .split("content-length: ")
            .nth(1)
            .and_then(|value| value.split("\r\n").next())
            .and_then(|value| value.trim().parse::<usize>().ok());
        let ended = declared.is_some_and(|len| rest.len() >= len);
        (raw, ended)
    }
}

async fn read_to_end(s: TcpStream) -> String {
    read_answer(s).await.0
}

async fn outcome(rig: &Rig) -> AnswerOutcome {
    for _ in 0..200 {
        if let Some(o) = rig.outcomes.lock().unwrap().first() {
            return *o;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    panic!("no outcome recorded");
}

const CUT_EVENT: &str = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream connection failed (EABORTED)\"}}\n\n";

#[tokio::test]
async fn the_answer_streams_through_chunk_by_chunk() {
    let rig = rig(
        |_, _| {
            Step::sse(
                vec!["data: a\n\n".into(), "data: b\n\n".into()],
                Duration::from_millis(200),
            )
        },
        IDLE_TIMEOUT,
    )
    .await;
    let mut s = post(rig.port).await;
    let mut buf = vec![0u8; 8192];
    // One read may carry the head alone; the first chunk has the same 150 ms budget.
    let deadline = tokio::time::Instant::now() + Duration::from_millis(150);
    let mut first = String::new();
    while !first.contains("data: a") {
        let n = tokio::time::timeout_at(deadline, s.read(&mut buf))
            .await
            .expect("the first chunk arrives before the second is sent")
            .expect("read");
        first.push_str(&String::from_utf8_lossy(&buf[..n]));
    }
    assert!(!first.contains("data: b"));
    let rest = read_to_end(s).await;
    assert!(rest.contains("data: b"), "{rest}");
    assert!(matches!(
        outcome(&rig).await,
        AnswerOutcome::Finished {
            status: 200,
            bytes: 18,
            ..
        }
    ));
}

#[tokio::test]
async fn compressed_bytes_and_headers_pass_untouched() {
    let rig = rig(
        |_, _| {
            Step::raw(
                200,
                vec![
                    ("content-encoding".into(), "gzip".into()),
                    ("request-id".into(), "req_1".into()),
                ],
                vec![0x1f, 0x8b, 0x00, 0x42, 0x42],
            )
        },
        IDLE_TIMEOUT,
    )
    .await;
    let s = post(rig.port).await;
    let mut raw = s;
    let mut out = Vec::new();
    tokio::time::timeout(Duration::from_secs(5), raw.read_to_end(&mut out))
        .await
        .expect("timely")
        .expect("read");
    let split = out
        .windows(4)
        .position(|w| w == b"\r\n\r\n")
        .expect("head end")
        + 4;
    let head = String::from_utf8_lossy(&out[..split]).to_lowercase();
    assert!(head.contains("content-encoding: gzip"), "{head}");
    assert!(head.contains("request-id: req_1"), "{head}");
    assert_eq!(
        &out[split..],
        &[0x1f, 0x8b, 0x00, 0x42, 0x42],
        "bytes pass untouched"
    );
}

#[tokio::test]
async fn a_cut_event_stream_ends_with_the_terminal_event() {
    let rig = rig(
        |_, _| Step::sse_then_cut(vec!["data: a\n\n".into()], Duration::from_millis(5)),
        IDLE_TIMEOUT,
    )
    .await;
    let body = read_to_end(post(rig.port).await).await;
    assert!(body.ends_with(CUT_EVENT), "{body}");
    assert_eq!(
        outcome(&rig).await,
        AnswerOutcome::Cut {
            code: FailureCode::EAborted,
            bytes: 9
        }
    );
}

#[tokio::test]
async fn a_cut_inside_an_event_closes_it_before_the_terminal_event() {
    let rig = rig(
        |_, _| Step::sse_then_cut(vec!["data: a\n".into()], Duration::from_millis(5)),
        IDLE_TIMEOUT,
    )
    .await;
    let body = read_to_end(post(rig.port).await).await;
    assert!(
        body.contains(&format!("data: a\n\n\n{CUT_EVENT}")),
        "the prefix closes the split event: {body:?}"
    );
}

#[tokio::test]
async fn a_cut_after_a_crlf_boundary_needs_no_extra_blank_line() {
    let rig = rig(
        |_, _| Step::sse_then_cut(vec!["data: a\r\n\r\n".into()], Duration::from_millis(5)),
        IDLE_TIMEOUT,
    )
    .await;
    let body = read_to_end(post(rig.port).await).await;
    assert!(
        body.contains(&format!("data: a\r\n\r\n{CUT_EVENT}")),
        "{body:?}"
    );
}

#[tokio::test]
async fn a_one_byte_final_chunk_after_a_boundary_is_still_a_boundary() {
    let rig = rig(
        |_, _| {
            Step::sse_then_cut(
                vec!["data: a\n".into(), "\n".into()],
                Duration::from_millis(5),
            )
        },
        IDLE_TIMEOUT,
    )
    .await;
    let body = read_to_end(post(rig.port).await).await;
    assert!(
        body.contains(&format!("data: a\n\n{CUT_EVENT}")),
        "tracking crosses chunks: {body:?}"
    );
}

#[tokio::test]
async fn a_non_stream_answer_broken_after_its_head_cuts_the_connection() {
    let rig = rig(
        |_, _| Step::json_then_cut(200, "{\"partial\":", 30),
        IDLE_TIMEOUT,
    )
    .await;
    let (body, ended) = read_answer(post(rig.port).await).await;
    assert!(!ended, "a cut answer reads as cut, not as a clean end");
    assert!(!body.contains(CUT_EVENT), "{body}");
    assert!(body.contains("\"partial\":"), "{body}");
}

#[tokio::test]
async fn a_mid_stream_silence_ends_with_etimedout() {
    let rig = rig(
        |_, _| Step::sse_head_then_silence(),
        Duration::from_millis(150),
    )
    .await;
    let body = read_to_end(post(rig.port).await).await;
    assert!(
        body.contains("upstream connection failed (ETIMEDOUT)"),
        "{body}"
    );
    assert_eq!(
        outcome(&rig).await,
        AnswerOutcome::Cut {
            code: FailureCode::ETimedOut,
            bytes: 0
        }
    );
}

#[tokio::test]
async fn an_upstream_that_never_answers_is_504() {
    let rig = rig(|_, _| Step::ReadAllThenHold, Duration::from_millis(100)).await;
    let resp = read_to_end(post(rig.port).await).await;
    assert!(resp.starts_with("HTTP/1.1 504"), "{resp}");
    assert!(
        resp.contains("huddle-router router: anthropic unreachable (ETIMEDOUT)"),
        "{resp}"
    );
    assert!(matches!(
        outcome(&rig).await,
        AnswerOutcome::Failed {
            code: FailureCode::ETimedOut,
            ..
        }
    ));
}

#[tokio::test]
async fn an_unreachable_upstream_is_502_naming_the_code() {
    let upstream = FakeUpstream::start(|_, _| Step::answer(200, "x")).await;
    let port = upstream.port();
    let target = upstream.url();
    drop(upstream);
    let client = Arc::new(UpstreamClient::new(UpstreamConfig {
        env: Env::with(&[]),
        roots: RootCertStore::empty(),
        idle: IDLE_TIMEOUT,
    }));
    let path = Arc::new(AnthropicPath {
        client,
        target: target.parse().expect("target"),
        body_limit: BODY_LIMIT,
        is_anthropic: Arc::new(|_| true),
        lane_streams: Arc::new(|_| false),
        streamed_route: Arc::new(|_, _, _| Box::pin(async { Response::new(full_body("x")) })),
        whole_route: Arc::new(|_, _| Box::pin(async { Response::new(full_body("x")) })),
        pass_through: Arc::new(|_req| Box::pin(async { Response::new(full_body("x")) })),
        on_outcome: None,
    });
    let body = Bytes::from_static(b"{}");
    let parts = hyper::Request::post(format!("http://127.0.0.1:{port}/v1/messages"))
        .body(())
        .expect("parts")
        .into_parts()
        .0;
    let resp = path.forward_whole(parts, body).await;
    assert_eq!(resp.status().as_u16(), 502);
}

#[tokio::test]
async fn a_5xx_before_the_stream_reaches_the_client_whole() {
    let rig = rig(
        |_, req| {
            if req == 1 {
                Step::answer(
                    503,
                    r#"{"type":"error","error":{"type":"overloaded_error","message":"x"}}"#,
                )
            } else {
                Step::answer(200, "next")
            }
        },
        IDLE_TIMEOUT,
    )
    .await;
    let first = read_to_end(post(rig.port).await).await;
    assert!(first.starts_with("HTTP/1.1 503"), "{first}");
    assert!(first.contains("overloaded_error"), "{first}");
    assert!(!first.contains("event: error"));
}

#[tokio::test]
async fn a_client_that_leaves_mid_answer_aborts_the_upstream_and_gets_no_error_body() {
    let rig = rig(
        |_, _| {
            Step::sse(
                (0..1000).map(|i| format!("data: {i}\n\n")).collect(),
                Duration::from_millis(2),
            )
        },
        IDLE_TIMEOUT,
    )
    .await;
    let mut s = post(rig.port).await;
    let mut buf = [0u8; 256];
    let read = s.read(&mut buf).await.expect("first chunk");
    assert!(read > 0, "the answer started");
    drop(s);
    for _ in 0..200 {
        if rig.upstream.requests().iter().any(|r| r.aborted) {
            break;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    assert!(
        rig.upstream.requests().iter().any(|r| r.aborted),
        "the upstream call stopped"
    );
    assert!(matches!(
        outcome(&rig).await,
        AnswerOutcome::ClientClosed { .. }
    ));
}
