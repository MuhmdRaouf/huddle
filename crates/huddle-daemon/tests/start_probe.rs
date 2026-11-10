use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::start::{
    DEFAULT_BUDGET, HealthAnswer, NO_DAEMON_ENV, StartError, StartOptions, health,
    router_key_check, start,
};
use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    io::{Read, Write},
    net::TcpListener,
    thread,
    time::{Duration, Instant},
};

fn home_env(home: &TempHome) -> Env {
    Env::with(&[("HOME", home.root().to_str().expect("utf8"))])
}

fn paths(home: &TempHome) -> Paths {
    Paths::from_env(&home_env(home)).expect("paths")
}

fn enable_zai(paths: &Paths) {
    fs::create_dir_all(&paths.router_dir).expect("mkdir");
    fs::write(
        paths.router_dir.join("providers.json"),
        "{\"providers\":[\"zai\"]}",
    )
    .expect("write");
}

/// One in-process daemon: a listener that answers every connection with the given health
/// answer, so the probe reads it as huddle-router without any process of its own.
fn fake_daemon(body: &'static str, status: &'static str) -> u16 {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { return };
            let mut buffer = [0u8; 2048];
            let _ = stream.read(&mut buffer);
            let reply = format!(
                "HTTP/1.1 {status}\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            );
            let _ = stream.write_all(reply.as_bytes());
        }
    });
    port
}

fn write_script(home: &TempHome, body: &str) -> std::path::PathBuf {
    let script = home.root().join("fake-huddle");
    write_executable(&script, &format!("#!/bin/sh\n{body}\n")).expect("write");
    script
}

const GOOD: &str =
    "{\"ok\":true,\"name\":\"huddle-router\",\"providers\":[\"zai\"],\"components\":[]}";

#[test]
fn health_reads_only_the_daemons_own_answer() {
    assert_eq!(
        health(fake_daemon(GOOD, "200 OK"), Duration::from_millis(300)),
        Some(HealthAnswer {
            name: "huddle-router".into(),
            providers: vec!["zai".into()]
        })
    );
    assert_eq!(
        health(
            fake_daemon("{\"name\":\"another\",\"providers\":[]}", "200 OK"),
            Duration::from_millis(300)
        ),
        None
    );
    assert_eq!(
        health(fake_daemon(GOOD, "500 Oops"), Duration::from_millis(300)),
        None
    );
    assert_eq!(
        health(
            fake_daemon("not json", "200 OK"),
            Duration::from_millis(300)
        ),
        None
    );
    let free = TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    assert_eq!(health(free, Duration::from_millis(300)), None);
}

#[test]
fn a_daemon_that_answers_is_named_and_not_started_over() {
    let home = TempHome::new();
    let paths = paths(&home);
    let marker = home.root().join("spawned");
    let script = write_script(&home, &format!("touch '{}'", marker.display()));
    let env = home_env(&home);
    let port = fake_daemon(GOOD, "200 OK");
    let line = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: script,
        budget: DEFAULT_BUDGET,
    })
    .expect("start");
    assert_eq!(
        line,
        format!("huddle-router: http://127.0.0.1:{port} (zai)")
    );
    assert!(
        !marker.exists(),
        "a running daemon must not be started over"
    );
}

#[test]
fn a_start_with_no_provider_still_starts_the_daemon() {
    let home = TempHome::new();
    let paths = paths(&home);
    let args = home.root().join("args");
    let script = write_script(&home, &format!("echo \"$@\" > '{}'", args.display()));
    let env = home_env(&home);
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let seen = args.clone();
    thread::spawn(move || {
        let deadline = Instant::now() + Duration::from_secs(10);
        while !seen.exists() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(10));
        }
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { return };
            let mut buffer = [0u8; 2048];
            let _ = stream.read(&mut buffer);
            let body =
                "{\"ok\":true,\"name\":\"huddle-router\",\"providers\":[],\"components\":[]}";
            let reply = format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            );
            let _ = stream.write_all(reply.as_bytes());
        }
    });
    let line = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: script,
        budget: Duration::from_secs(8),
    })
    .expect("start");
    assert_eq!(line, format!("huddle-router: http://127.0.0.1:{port}"));
    assert_eq!(
        fs::read_to_string(&args).expect("args").trim(),
        "router serve"
    );
}

#[test]
fn a_malformed_providers_file_is_an_error_before_any_spawn() {
    let home = TempHome::new();
    let paths = paths(&home);
    fs::create_dir_all(&paths.router_dir).expect("mkdir");
    fs::write(paths.router_dir.join("providers.json"), "not json").expect("write");
    let marker = home.root().join("spawned");
    let script = write_script(&home, &format!("touch '{}'", marker.display()));
    let env = home_env(&home);
    let free = TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    let error = start(&StartOptions {
        env: &env,
        paths: &paths,
        port: free,
        executable: script,
        budget: DEFAULT_BUDGET,
    })
    .expect_err("malformed");
    assert!(matches!(error, StartError::ProvidersFile(_)));
    assert!(!marker.exists());
}

#[test]
fn a_start_spawns_the_binary_and_waits_for_its_health() {
    let home = TempHome::new();
    let paths = paths(&home);
    enable_zai(&paths);
    let args_file = home.root().join("args");
    let script = write_script(
        &home,
        &format!(
            "echo \"$@\" > '{}'; echo \"dir=$(pwd -P)\" >> '{}'; echo log-line",
            args_file.display(),
            args_file.display()
        ),
    );
    let env = home_env(&home);
    // The daemon answers only after the script has run.
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let args_seen = args_file.clone();
    thread::spawn(move || {
        // The script writes the args file in two lines, and the test reads it whole: health
        // answers only once the dir line has landed.
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline
            && fs::read_to_string(&args_seen)
                .map(|text| !text.contains("dir="))
                .unwrap_or(true)
        {
            thread::sleep(Duration::from_millis(10));
        }
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { return };
            let mut buffer = [0u8; 2048];
            let _ = stream.read(&mut buffer);
            let reply = format!(
                "HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n{GOOD}",
                GOOD.len()
            );
            let _ = stream.write_all(reply.as_bytes());
        }
    });
    let line = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: script,
        budget: Duration::from_secs(8),
    })
    .expect("start");
    assert_eq!(
        line,
        format!("huddle-router: http://127.0.0.1:{port} (zai)")
    );
    let text = fs::read_to_string(&args_file).expect("args");
    assert!(text.starts_with("router serve"), "{text}");
    assert!(
        text.contains(&format!(
            "dir={}",
            paths.router_dir.canonicalize().expect("canon").display()
        )),
        "{text}"
    );
    let log = fs::read_to_string(paths.router_dir.join("router.log")).expect("log");
    assert!(log.contains("log-line"), "{log}");
}

#[test]
fn a_daemon_that_never_answers_names_its_log() {
    let home = TempHome::new();
    let paths = paths(&home);
    enable_zai(&paths);
    let script = write_script(&home, "exit 0");
    let env = home_env(&home);
    let free = TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    let error = start(&StartOptions {
        env: &env,
        paths: &paths,
        port: free,
        executable: script,
        budget: Duration::from_millis(120),
    })
    .expect_err("never answered");
    assert_eq!(
        error.to_string(),
        format!(
            "huddle-router: the daemon never answered at http://127.0.0.1:{free} (its log: {})",
            paths.router_dir.join("router.log").display()
        )
    );
}

#[test]
fn the_no_daemon_env_refuses_a_spawn() {
    let home = TempHome::new();
    let paths = paths(&home);
    enable_zai(&paths);
    let marker = home.root().join("spawned");
    let script = write_script(&home, &format!("touch '{}'", marker.display()));
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str()), (NO_DAEMON_ENV, "1")]);
    let free = TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    let error = start(&StartOptions {
        env: &env,
        paths: &paths,
        port: free,
        executable: script,
        budget: Duration::from_millis(50),
    })
    .expect_err("refused");
    assert!(matches!(error, StartError::Refused(_)));
    assert!(error.to_string().contains("HUDDLE_NO_DAEMON is set"));
    assert!(!marker.exists());
}

#[test]
fn the_key_check_waits_for_the_providers_lane() {
    let port = fake_daemon(GOOD, "200 OK");
    assert_eq!(
        router_key_check("zai", "Z.ai GLM", port, Duration::from_secs(1)).expect("serves"),
        "huddle-router serves Z.ai GLM"
    );
    assert_eq!(
        router_key_check("kimi", "Moonshot Kimi", port, Duration::from_millis(60))
            .expect_err("not kimi")
            .to_string(),
        "huddle-router serves zai, not kimi"
    );
    let free = TcpListener::bind("127.0.0.1:0")
        .expect("bind")
        .local_addr()
        .expect("addr")
        .port();
    assert_eq!(
        router_key_check("zai", "Z.ai GLM", free, Duration::from_millis(60))
            .expect_err("down")
            .to_string(),
        format!("huddle-router is not running on port {free}")
    );
}
