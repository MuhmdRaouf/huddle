use huddle_config::env::Env;
use huddle_router::connect::{ConnectCtx, DialFn, DrainOutcome, TunnelTracker, connect_handler};
use huddle_router::server::{ServerLimits, bind, serve};
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::watch;

async fn echo_server() -> u16 {
    let listener = TcpListener::bind(("127.0.0.1", 0)).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    tokio::spawn(async move {
        while let Ok((mut sock, _)) = listener.accept().await {
            tokio::spawn(async move {
                let (mut r, mut w) = sock.split();
                let _ = tokio::io::copy(&mut r, &mut w).await;
            });
        }
    });
    port
}

fn dial_to(port: u16) -> DialFn {
    Arc::new(move |_target: String| {
        Box::pin(async move { TcpStream::connect(("127.0.0.1", port)).await })
    })
}

async fn router(dial: Option<DialFn>, tunnels: Arc<TunnelTracker>) -> u16 {
    let ctx = Arc::new(ConnectCtx {
        env: Env::with(&[]),
        dial,
        tunnels,
        decrypt: None,
    });
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        serve(listener, connect_handler(ctx), ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    port
}

async fn connect(port: u16, target: &str) -> (TcpStream, String) {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    s.write_all(format!("CONNECT {target} HTTP/1.1\r\nHost: {target}\r\n\r\n").as_bytes())
        .await
        .expect("write");
    let mut head = Vec::new();
    let mut byte = [0u8; 1];
    loop {
        let n = tokio::time::timeout(Duration::from_secs(5), s.read(&mut byte))
            .await
            .expect("timely")
            .expect("read");
        assert!(
            n == 1,
            "closed before head end: {}",
            String::from_utf8_lossy(&head)
        );
        head.push(byte[0]);
        if head.ends_with(b"\r\n\r\n") {
            break;
        }
    }
    (s, String::from_utf8_lossy(&head).into_owned())
}

async fn body_after(mut s: TcpStream) -> String {
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(3), s.read_to_end(&mut out)).await;
    String::from_utf8_lossy(&out).into_owned()
}

#[tokio::test]
async fn a_blind_tunnel_dials_first_then_pipes_both_ways() {
    let echo = echo_server().await;
    let tracker = TunnelTracker::new();
    let port = router(Some(dial_to(echo)), tracker.clone()).await;
    let (mut s, head) = connect(port, "example.test:443").await;
    assert!(
        head.starts_with("HTTP/1.1 200 Connection established\r\n"),
        "{head}"
    );
    s.write_all(b"hello").await.expect("write");
    let mut buf = [0u8; 5];
    tokio::time::timeout(Duration::from_secs(3), s.read_exact(&mut buf))
        .await
        .expect("timely")
        .expect("read");
    assert_eq!(&buf, b"hello");
    assert_eq!(tracker.count(), 1, "the tunnel is counted while open");
}

#[tokio::test]
async fn a_target_that_cannot_be_opened_is_502_and_no_200_goes_out() {
    let dial: DialFn = Arc::new(|_| {
        Box::pin(async {
            Err(std::io::Error::new(
                std::io::ErrorKind::ConnectionRefused,
                "down",
            ))
        })
    });
    let port = router(Some(dial), TunnelTracker::new()).await;
    let (s, head) = connect(port, "example.test:443").await;
    assert!(head.starts_with("HTTP/1.1 502"), "{head}");
    assert!(
        body_after(s)
            .await
            .contains("huddle-router router: CONNECT failed (the target cannot be reached)")
    );
}

#[tokio::test]
async fn targets_on_this_machine_are_refused_before_any_dial() {
    let dial_count = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let counted = dial_count.clone();
    let dial: DialFn = Arc::new(move |_| {
        counted.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        Box::pin(async { Err(std::io::Error::other("unused")) })
    });
    let port = router(Some(dial), TunnelTracker::new()).await;
    for target in [
        "127.0.0.1:8080",
        "localhost:8080",
        "foo.localhost:8080",
        "[::1]:8080",
        "169.254.1.1:80",
        "0.0.0.0:80",
    ] {
        let (s, head) = connect(port, target).await;
        assert!(head.starts_with("HTTP/1.1 403"), "{target}: {head}");
        assert!(
            body_after(s).await.contains(
                "huddle-router router: CONNECT refused (a CONNECT may not name this machine)"
            ),
            "{target}"
        );
    }
    assert_eq!(dial_count.load(std::sync::atomic::Ordering::SeqCst), 0);
}

#[tokio::test]
async fn a_malformed_target_is_refused() {
    let port = router(Some(dial_to(1)), TunnelTracker::new()).await;
    for target in ["example.test", "example.test:0"] {
        let (s, head) = connect(port, target).await;
        assert!(head.starts_with("HTTP/1.1 403"), "{target}: {head}");
        assert!(
            body_after(s).await.contains("(a CONNECT names host:port)"),
            "{target}"
        );
    }
}

#[tokio::test]
async fn a_drain_waits_for_a_blind_tunnel_and_returns_when_it_ends() {
    let echo = echo_server().await;
    let tracker = TunnelTracker::new();
    let port = router(Some(dial_to(echo)), tracker.clone()).await;
    let (s, _head) = connect(port, "example.test:443").await;
    let draining = {
        let tracker = tracker.clone();
        tokio::spawn(async move { tracker.drain(Duration::from_secs(10)).await })
    };
    tokio::time::sleep(Duration::from_millis(50)).await;
    assert!(
        !draining.is_finished(),
        "still waiting while the tunnel is open"
    );
    drop(s);
    let outcome = tokio::time::timeout(Duration::from_secs(5), draining)
        .await
        .expect("timely")
        .expect("join");
    assert_eq!(outcome, DrainOutcome::Finished);
}

#[tokio::test]
async fn a_drain_past_its_deadline_closes_the_open_tunnels() {
    let echo = echo_server().await;
    let tracker = TunnelTracker::new();
    let port = router(Some(dial_to(echo)), tracker.clone()).await;
    let (mut s, _head) = connect(port, "example.test:443").await;
    assert_eq!(
        tracker.drain(Duration::from_millis(100)).await,
        DrainOutcome::TimedOut
    );
    let mut buf = [0u8; 1];
    let n = tokio::time::timeout(Duration::from_secs(5), s.read(&mut buf))
        .await
        .expect("timely")
        .unwrap_or(0);
    assert_eq!(n, 0, "the tunnel closed");
}
