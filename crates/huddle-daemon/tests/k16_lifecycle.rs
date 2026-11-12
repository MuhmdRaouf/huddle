//! K16: a router logs why it started, both sides of a handover and its exit, and every admin
//! request, never a header value or a query.

use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::serve::{ServeOptions, ServeOutcome, run};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream,
    sync::watch,
};

type Lines = Arc<Mutex<Vec<String>>>;

fn options(home: &TempHome, port: u16, takeover: bool, by: &str) -> (ServeOptions, Lines) {
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let lines: Lines = Arc::default();
    let sink = Arc::clone(&lines);
    let opts = ServeOptions {
        env,
        paths,
        port,
        version: "test".into(),
        takeover,
        by: by.into(),
        signal: Arc::default(),
        log: Arc::new(move |l| sink.lock().expect("lines").push(l)),
        output: None,
    };
    (opts, lines)
}

async fn health(port: u16, extra: &str) -> Option<String> {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).await.ok()?;
    let request = format!(
        "GET /huddle-router/health?token=query-secret-1 HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n{extra}Connection: close\r\n\r\n"
    );
    stream.write_all(request.as_bytes()).await.ok()?;
    let mut buffer = Vec::new();
    stream.read_to_end(&mut buffer).await.ok()?;
    Some(String::from_utf8_lossy(&buffer).into_owned())
}

async fn wait_health(port: u16, extra: &str) {
    let deadline = Instant::now() + Duration::from_secs(10);
    while !health(port, extra)
        .await
        .is_some_and(|t| t.contains("huddle-router"))
    {
        assert!(Instant::now() < deadline, "the daemon never answered");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

fn has(lines: &Lines, needle: &str) -> bool {
    lines.lock().unwrap().iter().any(|l| l.contains(needle))
}

#[tokio::test]
async fn k16_admin_requests_and_lifecycle_are_logged_without_values() {
    let home = TempHome::new();
    let port = free_port();
    let (opts, lines) = options(&home, port, false, "session-start");
    let (stop, rx) = watch::channel(false);
    let task = tokio::spawn(run(opts, rx));
    wait_health(port, "X-Api-Key: header-secret-1\r\n").await;
    let pid = std::process::id();
    assert!(
        has(
            &lines,
            &format!("huddle-router: start vtest pid={pid} port={port} by=session-start")
        ),
        "{:?}",
        lines.lock().unwrap()
    );
    assert!(has(&lines, "GET /huddle-router/health -> 200"));
    for line in lines.lock().unwrap().iter() {
        assert!(!line.contains("header-secret-1") && !line.contains("query-secret-1"));
    }
    stop.send(true).expect("stop");
    assert_eq!(
        task.await.expect("join").expect("serve"),
        ServeOutcome::Stopped
    );
}

#[tokio::test]
async fn k16_a_handover_logs_both_sides_and_the_old_router_logs_its_exit() {
    let home = TempHome::new();
    let port = free_port();
    let (old_opts, old_lines) = options(&home, port, false, "setup");
    let (_old_stop, old_rx) = watch::channel(false);
    let old = tokio::spawn(run(old_opts, old_rx));
    wait_health(port, "").await;
    let (new_opts, new_lines) = options(&home, port, true, "prompt-submit");
    let (new_stop, new_rx) = watch::channel(false);
    let new = tokio::spawn(run(new_opts, new_rx));
    assert_eq!(
        old.await.expect("join").expect("serve"),
        ServeOutcome::HandedOver
    );
    let pid = std::process::id();
    assert!(has(
        &old_lines,
        &format!("huddle-router: handing over to pid={pid} (asked by prompt-submit)")
    ));
    assert!(has(
        &old_lines,
        &format!("huddle-router: exit pid={pid} reason=handover")
    ));
    wait_health(port, "").await;
    assert!(has(
        &new_lines,
        &format!("huddle-router: took over from pid={pid}")
    ));
    assert!(has(
        &new_lines,
        &format!("huddle-router: start vtest pid={pid} port={port} by=handover")
    ));
    new_stop.send(true).expect("stop");
    new.await.expect("join").expect("serve");
}
