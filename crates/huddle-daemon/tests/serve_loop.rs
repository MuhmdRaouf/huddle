use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::serve::{ServeError, ServeOptions, ServeOutcome, run};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::{
    fs,
    net::TcpStream,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpStream as AsyncStream,
    sync::watch,
};

fn fixture(home: &TempHome) -> (Env, Paths) {
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    (env, paths)
}

fn options(
    env: Env,
    paths: Paths,
    port: u16,
    takeover: bool,
) -> (ServeOptions, Arc<Mutex<Vec<String>>>) {
    let lines = Arc::new(Mutex::new(Vec::new()));
    let sink = Arc::clone(&lines);
    let opts = ServeOptions {
        env,
        paths,
        port,
        version: "test".into(),
        takeover,
        by: "test".into(),
        signal: Arc::default(),
        log: Arc::new(move |l| sink.lock().expect("lines").push(l)),
        output: None,
    };
    (opts, lines)
}

async fn get_health(port: u16) -> Option<String> {
    let mut stream = AsyncStream::connect(("127.0.0.1", port)).await.ok()?;
    let request = format!(
        "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(request.as_bytes()).await.ok()?;
    let mut buffer = Vec::new();
    stream.read_to_end(&mut buffer).await.ok()?;
    Some(String::from_utf8_lossy(&buffer).into_owned())
}

async fn wait_for_health(port: u16) -> String {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if let Some(text) = get_health(port).await
            && text.contains("huddle-router")
        {
            return text;
        }
        assert!(Instant::now() < deadline, "the daemon never answered");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

#[tokio::test]
async fn the_daemon_serves_at_once_with_no_provider_and_lists_none() {
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let port = free_port();
    let (opts, _lines) = options(env, paths, port, false);
    let (stop_tx, stop_rx) = watch::channel(false);
    let task = tokio::spawn(run(opts, stop_rx));
    let health = wait_for_health(port).await;
    assert!(health.contains("\"providers\":[]"), "{health}");
    stop_tx.send(true).expect("stop");
    assert_eq!(
        task.await.expect("join").expect("serve"),
        ServeOutcome::Stopped
    );
}

#[tokio::test]
async fn the_record_lands_before_the_first_health_answer_and_goes_on_stop() {
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let port = free_port();
    let record = paths.run_dir.join("router.toml");
    let (opts, _lines) = options(env, paths.clone(), port, false);
    let (stop_tx, stop_rx) = watch::channel(false);
    let task = tokio::spawn(run(opts, stop_rx));
    wait_for_health(port).await;
    let parsed: toml::Table =
        toml::from_str(&fs::read_to_string(&record).expect("record")).expect("toml");
    assert_eq!(
        parsed["pid"].as_integer(),
        Some(i64::from(std::process::id()))
    );
    assert_eq!(parsed["port"].as_integer(), Some(i64::from(port)));
    stop_tx.send(true).expect("stop");
    task.await.expect("join").expect("serve");
    assert!(!record.exists(), "a clean stop removes the record");
}

#[tokio::test]
async fn a_second_run_says_already_served_and_the_first_keeps_serving() {
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let port = free_port();
    let (opts_a, _lines_a) = options(env.clone(), paths.clone(), port, false);
    let (stop_a, stop_rx_a) = watch::channel(false);
    let task_a = tokio::spawn(run(opts_a, stop_rx_a));
    wait_for_health(port).await;
    let (opts_b, _lines_b) = options(env, paths, port, false);
    let (_stop_b, stop_rx_b) = watch::channel(false);
    assert_eq!(
        run(opts_b, stop_rx_b).await.expect("second"),
        ServeOutcome::AlreadyServed {
            pid: std::process::id()
        }
    );
    assert!(get_health(port).await.is_some(), "the first keeps serving");
    stop_a.send(true).expect("stop");
    task_a.await.expect("join").expect("serve");
}

#[tokio::test]
async fn a_held_port_is_a_named_error() {
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let (opts, _lines) = options(env, paths, port, false);
    let (_stop, stop_rx) = watch::channel(false);
    match run(opts, stop_rx).await {
        Err(ServeError::Bind { port: bound, .. }) => assert_eq!(bound, port),
        other => panic!("want Bind, got {other:?}"),
    }
}

#[tokio::test]
async fn a_bad_anthropic_url_ends_the_run_before_anything_is_taken() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[
        ("HOME", root.as_str()),
        ("HUDDLE_ROUTER_ANTHROPIC_URL", "not a url"),
    ]);
    let paths = Paths::from_env(&env).expect("paths");
    let (opts, _lines) = options(env, paths, free_port(), false);
    let (_stop, stop_rx) = watch::channel(false);
    assert!(matches!(
        run(opts, stop_rx).await,
        Err(ServeError::BadAnthropicUrl(_))
    ));
}

#[tokio::test]
async fn enabling_a_provider_adds_its_lane_within_one_poll() {
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let port = free_port();
    let (opts, lines) = options(env, paths.clone(), port, false);
    let (stop_tx, stop_rx) = watch::channel(false);
    let task = tokio::spawn(run(opts, stop_rx));
    wait_for_health(port).await;
    fs::create_dir_all(&paths.config_dir).expect("mkdir");
    fs::write(
        paths.config_dir.join("providers.toml"),
        "providers = [\"zai\"]\n",
    )
    .expect("write");
    let deadline = Instant::now() + Duration::from_secs(6);
    loop {
        let health = get_health(port).await.unwrap_or_default();
        if health.contains("\"providers\":[\"zai\"]") {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "the lane never arrived: {health}"
        );
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert!(
        lines
            .lock()
            .expect("lines")
            .iter()
            .any(|l| l == "huddle-router: now serving zai"),
        "{:?}",
        lines.lock().expect("lines")
    );
    stop_tx.send(true).expect("stop");
    task.await.expect("join").expect("serve");
}

#[tokio::test]
async fn a_port_is_held_while_the_backlog_absorbs_many_dials_before_any_answer() {
    // macOS clamps a listen queue to kern.ipc.somaxconn (128), so 100 is the count this
    // queue deterministically absorbs. The dial loop blocks this thread, so the daemon cannot
    // accept while it runs: every dial lands in the kernel queue, and none is refused.
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let port = free_port();
    let (opts, _lines) = options(env, paths, port, false);
    let (stop_tx, stop_rx) = watch::channel(false);
    let task = tokio::spawn(run(opts, stop_rx));
    wait_for_health(port).await;
    let dials: Vec<_> = (0..100)
        .map(|_| {
            TcpStream::connect_timeout(
                &format!("127.0.0.1:{port}").parse().expect("addr"),
                Duration::from_secs(5),
            )
        })
        .collect();
    let failed = dials.iter().find(|dial| dial.is_err());
    if let Some(Err(error)) = failed {
        panic!("every dial connects: {error}");
    }
    drop(dials);
    stop_tx.send(true).expect("stop");
    task.await.expect("join").expect("serve");
}

#[tokio::test]
async fn a_signal_during_a_drain_ends_the_drain_at_once() {
    // The handover path is exercised by the daemon update e2e; this pins the stop path's budget
    // ending quickly.
    let home = TempHome::new();
    let (env, paths) = fixture(&home);
    let port = free_port();
    let (opts, _lines) = options(env, paths, port, false);
    let (stop_tx, stop_rx) = watch::channel(false);
    let task = tokio::spawn(run(opts, stop_rx));
    wait_for_health(port).await;
    let started = Instant::now();
    stop_tx.send(true).expect("stop");
    task.await.expect("join").expect("serve");
    assert!(
        started.elapsed() < Duration::from_secs(5),
        "a clean stop with nothing in flight is fast"
    );
}
