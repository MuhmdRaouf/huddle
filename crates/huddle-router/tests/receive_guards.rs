use huddle_router::health::HEALTH_PATH;
use huddle_router::server::{
    BODY_LIMIT, EntryCtx, Handler, ServerLimits, anthropic_error, bind, entry_handler, full_body,
    receive, safe_path, serve,
};
use hyper::body::Incoming;
use hyper::{Request, Response, StatusCode};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

fn counting(tag: &'static str, seen: Arc<AtomicUsize>) -> Handler {
    Arc::new(move |_req: Request<Incoming>| {
        let seen = seen.clone();
        Box::pin(async move {
            seen.fetch_add(1, Ordering::SeqCst);
            Response::new(full_body(tag))
        })
    })
}

async fn start(handler: Handler) -> u16 {
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    port
}

struct Rig {
    port: u16,
    connects: Arc<AtomicUsize>,
    forwards: Arc<AtomicUsize>,
}

async fn entry() -> Rig {
    let connects = Arc::new(AtomicUsize::new(0));
    let forwards = Arc::new(AtomicUsize::new(0));
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let handler = entry_handler(Arc::new(EntryCtx {
        port: Some(port),
        connect: counting("connect", connects.clone()),
        forward: counting("forward", forwards.clone()),
        providers: Arc::new(|| vec!["zai".to_string()]),
    }));
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    Rig {
        port,
        connects,
        forwards,
    }
}

async fn raw(port: u16, req: String) -> String {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(req.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

fn close_req(method: &str, path: &str, host: &str, extra: &str, body: &str) -> String {
    format!(
        "{method} {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n{extra}Content-Length: {}\r\n\r\n{body}",
        body.len()
    )
}

fn body_of(resp: &str) -> &str {
    resp.split("\r\n\r\n").nth(1).unwrap_or("")
}

#[tokio::test]
async fn a_request_for_another_host_is_refused() {
    let rig = entry().await;
    let resp = raw(
        rig.port,
        close_req("POST", "/v1/messages", "evil.example:1", "", "{}"),
    )
    .await;
    assert!(resp.starts_with("HTTP/1.1 421"), "{resp}");
    assert_eq!(
        body_of(&resp),
        r#"{"type":"error","error":{"type":"invalid_request_error","message":"huddle-router router: wrong Host"}}"#
    );
    assert_eq!(rig.forwards.load(Ordering::SeqCst), 0);
}

#[tokio::test]
async fn the_loopback_hosts_are_accepted_case_insensitively() {
    let rig = entry().await;
    for host in [
        format!("127.0.0.1:{}", rig.port),
        format!("LOCALHOST:{}", rig.port),
        format!("[::1]:{}", rig.port),
    ] {
        let resp = raw(rig.port, close_req("POST", "/v1/messages", &host, "", "{}")).await;
        assert_eq!(body_of(&resp), "forward", "{host}: {resp}");
    }
}

#[tokio::test]
async fn connect_skips_the_host_guard() {
    let rig = entry().await;
    let resp = raw(
        rig.port,
        "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n".to_string(),
    )
    .await;
    assert_eq!(rig.connects.load(Ordering::SeqCst), 1, "{resp}");
}

#[tokio::test]
async fn the_health_path_is_never_forwarded() {
    let rig = entry().await;
    let host = format!("127.0.0.1:{}", rig.port);
    let get_query = raw(
        rig.port,
        close_req("GET", &format!("{HEALTH_PATH}?x=1"), &host, "", ""),
    )
    .await;
    assert_eq!(
        body_of(&get_query),
        r#"{"ok":true,"name":"huddle-router","providers":["zai"],"components":[]}"#
    );
    let post = raw(rig.port, close_req("POST", HEALTH_PATH, &host, "", "{}")).await;
    assert!(post.starts_with("HTTP/1.1 404"), "{post}");
    assert!(post.contains("huddle-router: no such path"));
    assert_eq!(rig.forwards.load(Ordering::SeqCst), 0);
}

#[tokio::test]
async fn a_request_with_no_body_and_any_other_path_goes_to_forward() {
    let rig = entry().await;
    let host = format!("127.0.0.1:{}", rig.port);
    assert_eq!(
        body_of(&raw(rig.port, close_req("GET", "/anything", &host, "", "")).await),
        "forward"
    );
}

#[test]
fn only_a_single_leading_slash_is_a_safe_path() {
    assert!(safe_path("/v1/messages?x=1"));
    assert!(!safe_path("//evil.example/x"));
    assert!(!safe_path("https://evil.example/x"));
    assert!(!safe_path(""));
}

fn receiving(limit: u64, got: Arc<tokio::sync::Mutex<Vec<Vec<u8>>>>) -> Handler {
    Arc::new(move |req: Request<Incoming>| {
        let got = got.clone();
        Box::pin(async move {
            match receive(req, limit).await {
                Ok((_parts, body)) => {
                    got.lock().await.push(body.to_vec());
                    anthropic_error(StatusCode::OK, "ok", "received")
                }
                Err(resp) => resp,
            }
        })
    })
}

#[tokio::test]
async fn a_body_over_the_limit_is_413_and_never_delivered() {
    let got = Arc::new(tokio::sync::Mutex::new(Vec::new()));
    let port = start(receiving(128 * 1024, got.clone())).await;
    let body = "x".repeat(200 * 1024);
    let resp = raw(
        port,
        close_req("POST", "/v1/messages", "127.0.0.1", "", &body),
    )
    .await;
    assert!(resp.starts_with("HTTP/1.1 413"), "{resp}");
    assert!(resp.to_lowercase().contains("connection: close"), "{resp}");
    assert!(body_of(&resp).contains(r#""type":"request_too_large""#));
    assert!(body_of(&resp).contains("huddle-router router: request body over 64 MiB"));
    assert!(got.lock().await.is_empty());
}

#[tokio::test]
async fn an_unannounced_body_over_the_limit_is_also_413() {
    let got = Arc::new(tokio::sync::Mutex::new(Vec::new()));
    let port = start(receiving(64, got.clone())).await;
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(b"POST /v1/messages HTTP/1.1\r\nHost: x\r\nConnection: close\r\nTransfer-Encoding: chunked\r\n\r\n")
        .await
        .expect("head");
    s.write_all(format!("{:x}\r\n{}\r\n0\r\n\r\n", 200, "y".repeat(200)).as_bytes())
        .await
        .expect("body");
    let mut out = Vec::new();
    s.read_to_end(&mut out).await.expect("read");
    let resp = String::from_utf8_lossy(&out);
    assert!(resp.starts_with("HTTP/1.1 413"), "{resp}");
    assert!(got.lock().await.is_empty());
}

#[tokio::test]
async fn a_bad_path_is_400_before_any_body_is_read() {
    let got = Arc::new(tokio::sync::Mutex::new(Vec::new()));
    let port = start(receiving(BODY_LIMIT, got.clone())).await;
    let resp = raw(
        port,
        close_req("POST", "//evil.example/x", "127.0.0.1", "", "{}"),
    )
    .await;
    assert!(resp.starts_with("HTTP/1.1 400"), "{resp}");
    assert_eq!(
        body_of(&resp),
        r#"{"type":"error","error":{"type":"invalid_request_error","message":"huddle-router router: bad request path"}}"#
    );
    assert!(got.lock().await.is_empty());
}

#[tokio::test]
async fn a_complete_body_is_delivered_whole() {
    let got = Arc::new(tokio::sync::Mutex::new(Vec::new()));
    let port = start(receiving(BODY_LIMIT, got.clone())).await;
    let resp = raw(
        port,
        close_req("POST", "/v1/messages", "127.0.0.1", "", "hello"),
    )
    .await;
    assert!(resp.starts_with("HTTP/1.1 200"), "{resp}");
    assert_eq!(got.lock().await[0], b"hello");
}

#[tokio::test]
async fn a_client_that_stops_mid_body_gets_no_answer_and_nothing_is_delivered() {
    let got = Arc::new(tokio::sync::Mutex::new(Vec::new()));
    let port = start(receiving(BODY_LIMIT, got.clone())).await;
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(b"POST /v1/messages HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nshort")
        .await
        .expect("write");
    s.shutdown().await.expect("half close");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(3), s.read_to_end(&mut out)).await;
    assert!(
        !String::from_utf8_lossy(&out).contains("200"),
        "no complete answer"
    );
    assert!(got.lock().await.is_empty());
}
