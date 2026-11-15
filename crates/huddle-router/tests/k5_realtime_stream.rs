//! K5: the event stream behind the refresh picker's `Realtime`: throttled pushes, the state
//! fields only, the Origin guard, and a bounded set of open streams.

use huddle_config::{env::Env, paths::Paths};
use huddle_router::{
    dashboard::{DashboardInputs, MAX_LIVE_STREAMS, PING_EVERY},
    dispatch::{Router, RouterConfig},
    server::{ServerLimits, bind, serve},
};
use huddle_testkit::{
    fake_upstream::{FakeUpstream, Step},
    temp_home::TempHome,
};
use hyper::Uri;
use serde_json::Value;
use std::{
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::watch,
};

struct Rig {
    _home: TempHome,
    port: u16,
    stop: watch::Sender<bool>,
}

async fn rig(anthropic: FakeUpstream) -> Rig {
    rig_with(anthropic, DashboardInputs::default()).await
}

async fn rig_with(anthropic: FakeUpstream, dashboard: DashboardInputs) -> Rig {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("ZAI_API_KEY", "test-key-test-key"),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let router = Router::new(
        RouterConfig {
            env,
            paths,
            port,
            anthropic_url: anthropic.url().parse::<Uri>().expect("uri"),
            log: Arc::new(|_| {}),
            history: None,
            dashboard,
        },
        &["zai".to_string()],
    );
    let (stop, rx) = watch::channel(false);
    let handler = router.handler();
    tokio::spawn(async move {
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    std::mem::forget(anthropic);
    Rig {
        _home: home,
        port,
        stop,
    }
}

/// An open `GET` of the live path that yields one frame per call: a snapshot's JSON, or `None`
/// for a keep-alive comment.
struct Feed {
    stream: TcpStream,
    buf: String,
    head: String,
}

impl Feed {
    async fn open(port: u16, query: &str, extra: &str) -> Feed {
        let mut stream = TcpStream::connect(("127.0.0.1", port))
            .await
            .expect("connect");
        let head = format!(
            "GET /huddle-router/live{query} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n{extra}\r\n"
        );
        stream.write_all(head.as_bytes()).await.expect("write");
        let mut feed = Feed {
            stream,
            buf: String::new(),
            head: String::new(),
        };
        while !feed.buf.contains("\r\n\r\n") {
            if !feed.fill().await {
                break;
            }
        }
        if let Some((head, rest)) = feed.buf.split_once("\r\n\r\n") {
            feed.head = head.to_string();
            feed.buf = rest.to_string();
        }
        feed
    }

    async fn fill(&mut self) -> bool {
        let mut chunk = [0u8; 65536];
        match self.stream.read(&mut chunk).await {
            Ok(0) | Err(_) => false,
            Ok(n) => {
                self.buf.push_str(&String::from_utf8_lossy(&chunk[..n]));
                true
            }
        }
    }

    /// The next frame: `Some(Some(json))` for a snapshot, `Some(None)` for a comment, `None`
    /// when the stream ended. A chunked body's size lines are skipped.
    async fn next(&mut self) -> Option<Option<Value>> {
        loop {
            if let Some(at) = self.buf.find("\n\n") {
                let frame: String = self.buf.drain(..at + 2).collect();
                let frame = frame.trim();
                if let Some(data) = frame.lines().find_map(|l| l.strip_prefix("data: ")) {
                    return Some(Some(serde_json::from_str(data).expect("json")));
                }
                if frame.contains(": ping") {
                    return Some(None);
                }
                continue;
            }
            if !self.fill().await {
                return None;
            }
        }
    }
}

async fn state(port: u16) -> Value {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    stream
        .write_all(
            format!(
                "GET /huddle-router/state HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
            )
            .as_bytes(),
        )
        .await
        .expect("write");
    let mut out = String::new();
    stream.read_to_string(&mut out).await.expect("read");
    let (_, body) = out.split_once("\r\n\r\n").expect("head");
    serde_json::from_str(body).expect("json")
}

async fn ask(port: u16, extra: &str, body: &str) {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let head = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n{extra}Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    stream.write_all(head.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = stream.read_to_end(&mut out).await;
}

#[tokio::test]
async fn k5_stream_pushes_on_change_and_is_throttled() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    let mut feed = Feed::open(r.port, "?recent=0", "").await;
    assert!(
        matches!(feed.next().await, Some(Some(_))),
        "a snapshot at once"
    );

    let port = r.port;
    let burst = tokio::spawn(async move {
        let mut asks = Vec::new();
        for _ in 0..100 {
            asks.push(tokio::spawn(ask(
                port,
                "",
                r#"{"model":"claude-opus-5-5"}"#,
            )));
            tokio::time::sleep(Duration::from_millis(12)).await;
        }
        for a in asks {
            let _ = a.await;
        }
    });
    let mut stamps = Vec::new();
    let began = Instant::now();
    while began.elapsed() < Duration::from_millis(1800) {
        let left = Duration::from_millis(1800).saturating_sub(began.elapsed());
        match tokio::time::timeout(left, feed.next()).await {
            Ok(Some(Some(_))) => stamps.push(Instant::now()),
            Ok(Some(None)) => {}
            _ => break,
        }
    }
    burst.await.expect("burst");

    assert!(
        stamps.len() >= 3,
        "a burst is pushed as it happens, not once a second: {} events",
        stamps.len()
    );
    for pair in stamps.windows(5) {
        assert!(
            pair[4].duration_since(pair[0]) >= Duration::from_millis(950),
            "no more than 4 events in one second"
        );
    }
    let _ = r.stop.send(true);
}

#[test]
fn k5_an_idle_stream_pings_every_fifteen_seconds_by_default() {
    assert_eq!(DashboardInputs::default().ping_every, PING_EVERY);
    assert_eq!(PING_EVERY, Duration::from_secs(15));
}

#[tokio::test]
async fn k5_idle_stream_sends_a_keep_alive_within_its_ping_interval() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let ping_every = Duration::from_millis(300);
    let r = rig_with(
        anthropic,
        DashboardInputs {
            ping_every,
            ..DashboardInputs::default()
        },
    )
    .await;
    let mut feed = Feed::open(r.port, "?recent=0", "").await;
    assert!(matches!(feed.next().await, Some(Some(_))));
    let began = Instant::now();
    let next = tokio::time::timeout(ping_every * 10, feed.next())
        .await
        .expect("a frame inside the ping interval");
    assert!(matches!(next, Some(None)), "an idle stream sends a comment");
    assert!(
        began.elapsed() >= ping_every / 2,
        "no comment long before the interval"
    );
    let _ = r.stop.send(true);
}

fn keys(v: &Value) -> Vec<String> {
    let mut k: Vec<String> = v.as_object().expect("object").keys().cloned().collect();
    k.sort();
    k
}

#[tokio::test]
async fn k5_stream_carries_only_state_fields() {
    let anthropic = FakeUpstream::start(|_, _| {
        Step::answer(
            200,
            r#"{"id":"m","content":[{"type":"tool_result","content":"TOOL-OUTPUT-SECRET"}],"usage":{"input_tokens":1,"output_tokens":2}}"#,
        )
    })
    .await;
    let r = rig(anthropic).await;
    let mut slim = Feed::open(r.port, "?recent=0", "").await;
    let mut full = Feed::open(r.port, "", "").await;

    ask(
        r.port,
        "x-api-key: KEY-HEADER-SECRET\r\nx-claude-code-session-id: s1\r\n",
        r#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":"PROMPT-TEXT-SECRET"},{"role":"user","content":[{"type":"tool_result","content":"TOOL-RESULT-SECRET"}]}]}"#,
    )
    .await;

    let at_state = state(r.port).await;
    let mut slim_seen = Vec::new();
    let mut full_seen = Vec::new();
    for _ in 0..4 {
        if let Ok(Some(Some(v))) = tokio::time::timeout(Duration::from_secs(2), slim.next()).await {
            slim_seen.push(v);
        }
        if let Ok(Some(Some(v))) = tokio::time::timeout(Duration::from_secs(2), full.next()).await {
            full_seen.push(v);
        }
    }
    assert!(!slim_seen.is_empty() && !full_seen.is_empty());
    let state_keys = keys(&at_state);
    let slim_keys: Vec<String> = state_keys
        .iter()
        .filter(|k| *k != "recent")
        .cloned()
        .collect();
    for event in &slim_seen {
        assert_eq!(
            keys(event),
            slim_keys,
            "the slim form is /state without recent"
        );
    }
    for event in &full_seen {
        assert_eq!(
            keys(event),
            state_keys,
            "the full form has every /state field"
        );
    }
    for event in slim_seen.iter().chain(full_seen.iter()) {
        let text = event.to_string();
        for secret in [
            "PROMPT-TEXT-SECRET",
            "KEY-HEADER-SECRET",
            "TOOL-RESULT-SECRET",
            "TOOL-OUTPUT-SECRET",
        ] {
            assert!(!text.contains(secret), "{secret} reached the stream");
        }
    }
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn k5_stream_refuses_a_foreign_origin() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;

    let foreign = Feed::open(r.port, "", "Origin: http://evil.example\r\n").await;
    assert!(
        foreign.head.starts_with("HTTP/1.1 403"),
        "a foreign Origin is refused: {}",
        foreign.head
    );
    let own_origin = format!("Origin: http://127.0.0.1:{}\r\n", r.port);
    let own = Feed::open(r.port, "", &own_origin).await;
    assert!(own.head.starts_with("HTTP/1.1 200"), "{}", own.head);
    let none = Feed::open(r.port, "", "").await;
    assert!(none.head.starts_with("HTTP/1.1 200"), "{}", none.head);
    let _ = r.stop.send(true);
}

/// A `GET` of the live path whose `Origin` bytes are sent as given; returns the head's first bytes.
async fn open_with_raw_origin(port: u16, origin: &[u8]) -> String {
    let mut stream = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let mut head =
        format!("GET /huddle-router/live HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nOrigin: ")
            .into_bytes();
    head.extend_from_slice(origin);
    head.extend_from_slice(b"\r\n\r\n");
    stream.write_all(&head).await.expect("write");
    let mut chunk = [0u8; 4096];
    let n = tokio::time::timeout(Duration::from_secs(3), stream.read(&mut chunk))
        .await
        .expect("an answer")
        .expect("read");
    String::from_utf8_lossy(&chunk[..n]).into_owned()
}

#[tokio::test]
async fn c2_unreadable_origin_is_refused() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    let wrong = Feed::open(r.port, "", "Origin: http://evil.example\r\n").await;
    assert!(wrong.head.starts_with("HTTP/1.1 403"), "{}", wrong.head);
    let unreadable = open_with_raw_origin(r.port, b"http://caf\xc3\xa9.example").await;
    assert!(
        unreadable.starts_with("HTTP/1.1 403"),
        "a non-ASCII Origin is refused like a wrong one: {unreadable}"
    );
    let _ = r.stop.send(true);
}

async fn viewers(port: u16) -> u64 {
    state(port).await["router"]["viewers"]
        .as_u64()
        .expect("viewers")
}

#[tokio::test]
async fn k5_closed_tab_closes_the_stream_and_streams_are_bounded() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    assert_eq!(viewers(r.port).await, 0);

    let mut feeds = Vec::new();
    for _ in 0..MAX_LIVE_STREAMS + 4 {
        feeds.push(Feed::open(r.port, "?recent=0", "").await);
    }
    let mut seen = 0;
    for _ in 0..50 {
        seen = viewers(r.port).await;
        if seen as usize <= MAX_LIVE_STREAMS {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert!(
        seen as usize <= MAX_LIVE_STREAMS,
        "{seen} open streams, the cap is {MAX_LIVE_STREAMS}"
    );

    drop(feeds);
    let mut left = u64::MAX;
    for _ in 0..100 {
        left = viewers(r.port).await;
        if left == 0 {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert_eq!(left, 0, "closing the tabs closes every stream");
    let _ = r.stop.send(true);
}

#[tokio::test]
async fn k5_finished_feeds_do_not_count_against_the_cap() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    let mut kept = Feed::open(r.port, "?recent=0", "").await;
    assert!(matches!(kept.next().await, Some(Some(_))));

    for _ in 0..MAX_LIVE_STREAMS + 2 {
        let gone = Feed::open(r.port, "?recent=0", "").await;
        // The head can arrive before the feed's task counts its viewer; the task is running once
        // the count reaches two, so the drop below ends a task that exists.
        for _ in 0..100 {
            if viewers(r.port).await == 2 {
                break;
            }
            tokio::time::sleep(Duration::from_millis(30)).await;
        }
        assert_eq!(viewers(r.port).await, 2, "the new feed's task is running");
        drop(gone);
        for _ in 0..100 {
            if viewers(r.port).await == 1 {
                break;
            }
            tokio::time::sleep(Duration::from_millis(30)).await;
        }
        assert_eq!(viewers(r.port).await, 1, "the closed feed left the count");
    }

    ask(r.port, "", r#"{"model":"claude-opus-5-5"}"#).await;
    let next = tokio::time::timeout(Duration::from_secs(3), kept.next())
        .await
        .expect("the kept tab still gets pushes");
    assert!(
        matches!(next, Some(Some(_))),
        "the kept feed was not cut by closed ones"
    );
    let _ = r.stop.send(true);
}

/// Bytes the client socket holds that nobody has read.
fn unread_bytes(stream: &TcpStream) -> libc::c_int {
    use std::os::fd::AsRawFd;
    let mut count: libc::c_int = 0;
    // SAFETY: FIONREAD writes one int through the pointer, and the fd is open.
    unsafe { libc::ioctl(stream.as_raw_fd(), libc::FIONREAD, &mut count) };
    count
}

#[tokio::test]
async fn c2_evicted_feed_ends_while_its_reader_is_stalled() {
    let anthropic = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let r = rig(anthropic).await;
    // A client that sends its request and never reads: its socket buffers fill and the feed's
    // sends block.
    let mut stalled = TcpStream::connect(("127.0.0.1", r.port))
        .await
        .expect("connect");
    let head = format!(
        "GET /huddle-router/live HTTP/1.1\r\nHost: 127.0.0.1:{}\r\n\r\n",
        r.port
    );
    stalled.write_all(head.as_bytes()).await.expect("write");
    for _ in 0..100 {
        if viewers(r.port).await == 1 {
            break;
        }
        tokio::time::sleep(Duration::from_millis(30)).await;
    }
    assert_eq!(viewers(r.port).await, 1, "the stalled feed is open");
    // Distinct long model names make every snapshot large, and each change makes the stalled
    // feed push one, until its socket is full and its send blocks.
    let started = Instant::now();
    let mut n = 0;
    while started.elapsed() < Duration::from_secs(6) {
        let model = format!("claude-{n}-{}", "m".repeat(150));
        ask(r.port, "", &format!(r#"{{"model":"{model}"}}"#)).await;
        n += 1;
    }
    // The send is blocked, not just slow: once the socket is full, more changes add no byte to
    // it. The count settles within a few pushes while the last queued frames drain.
    assert_eq!(viewers(r.port).await, 1, "the stalled feed is still open");
    let mut previous = unread_bytes(&stalled);
    let mut blocked = false;
    for _ in 0..10 {
        let model = format!("claude-{n}-{}", "m".repeat(150));
        ask(r.port, "", &format!(r#"{{"model":"{model}"}}"#)).await;
        n += 1;
        tokio::time::sleep(Duration::from_millis(900)).await;
        let now = unread_bytes(&stalled);
        blocked = now == previous;
        if blocked {
            break;
        }
        previous = now;
    }
    assert!(
        blocked,
        "the stalled feed's send is not blocked, so no eviction was needed"
    );
    let mut others = Vec::new();
    for _ in 0..MAX_LIVE_STREAMS {
        others.push(Feed::open(r.port, "?recent=0", "").await);
    }
    let mut seen = u64::MAX;
    for _ in 0..100 {
        seen = viewers(r.port).await;
        if seen as usize <= MAX_LIVE_STREAMS {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert!(
        seen as usize <= MAX_LIVE_STREAMS,
        "{seen} open streams, the stalled one outlived its eviction"
    );
    drop(stalled);
    let _ = r.stop.send(true);
}
