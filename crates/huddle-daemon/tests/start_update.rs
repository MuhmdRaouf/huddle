use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::start::{RouterRecord, StartOptions, may_take_over, same_binary, start};
use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    io::{Read, Write},
    net::TcpListener,
    os::unix::fs::symlink,
    thread,
    time::{Duration, Instant},
};

const GOOD: &str =
    "{\"ok\":true,\"name\":\"huddle-router\",\"providers\":[\"zai\"],\"components\":[]}";

fn home_env(home: &TempHome) -> Env {
    Env::with(&[("HOME", home.root().to_str().expect("utf8"))])
}

fn paths(home: &TempHome) -> Paths {
    Paths::from_env(&home_env(home)).expect("paths")
}

fn fake_daemon() -> u16 {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    thread::spawn(move || {
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
    port
}

fn write_record(paths: &Paths, pid: u32, port: u16, executable: &str) {
    let dir = paths.run_dir.clone();
    fs::create_dir_all(&dir).expect("mkdir");
    let record = RouterRecord {
        pid,
        port,
        version: "1".into(),
        executable: executable.to_string(),
        started: "t".into(),
        worker: 0,
        restarts: 0,
        last_restart: String::new(),
    };
    fs::write(
        dir.join("router.toml"),
        toml::to_string(&record).expect("toml"),
    )
    .expect("write");
}

fn script(home: &TempHome, name: &str, body: &str) -> std::path::PathBuf {
    let path = home.root().join(name);
    write_executable(&path, &format!("#!/bin/sh\n{body}\n")).expect("write");
    path
}

#[test]
fn a_record_naming_this_binary_is_the_quiet_line() {
    let home = TempHome::new();
    let paths = paths(&home);
    let env = home_env(&home);
    let port = fake_daemon();
    let exe = script(&home, "huddle", "touch never");
    write_record(&paths, 1234, port, exe.to_str().expect("utf8"));
    let line = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: exe,
        budget: Duration::from_secs(1),
    })
    .expect("start");
    assert_eq!(
        line,
        format!("huddle-router: http://127.0.0.1:{port} (zai)")
    );
}

#[test]
fn a_record_naming_another_binary_is_taken_over() {
    let home = TempHome::new();
    let paths = paths(&home);
    let env = home_env(&home);
    let port = fake_daemon();
    write_record(&paths, 1234, port, "/old/huddle");
    let args = home.root().join("args");
    let record_dir = paths.run_dir.clone();
    let new_record = toml::to_string(&RouterRecord {
        pid: 5678,
        port,
        version: "2".into(),
        executable: "/new/huddle".into(),
        started: "t".into(),
        worker: 0,
        restarts: 0,
        last_restart: String::new(),
    })
    .expect("toml");
    let exe = script(
        &home,
        "huddle",
        &format!(
            "echo \"$@\" > '{}'\ncat > '{}/router.toml' <<'EOF'\n{new_record}\nEOF",
            args.display(),
            record_dir.display()
        ),
    );
    let line = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: exe,
        budget: Duration::from_secs(1),
    })
    .expect("start");
    assert_eq!(line, "huddle-router: updated (pid 1234 \u{2192} 5678)");
    assert_eq!(
        fs::read_to_string(args).expect("args").trim(),
        "router serve --takeover --by router start"
    );
}

#[test]
fn a_takeover_nobody_answers_names_the_old_pid_and_the_log() {
    let home = TempHome::new();
    let paths = paths(&home);
    let env = home_env(&home);
    let port = fake_daemon();
    write_record(&paths, 1234, port, "/old/huddle");
    let exe = script(&home, "huddle", "exit 0");
    let exe_text = exe.display().to_string();
    let error = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: exe,
        budget: Duration::from_secs(1),
    })
    .expect_err("never handed over");
    assert_eq!(
        error.to_string(),
        format!(
            "huddle-router: the daemon (pid 1234) never handed over to {exe_text} (its log: {})",
            paths.state_dir.join("router.log").display()
        )
    );
}

#[test]
fn a_binary_the_installed_link_does_not_name_never_takes_over() {
    let home = TempHome::new();
    let paths = paths(&home);
    let env = home_env(&home);
    let port = fake_daemon();
    fs::create_dir_all(&paths.bin_dir).expect("mkdir");
    let link_target = script(&home, "huddle-2", "touch never");
    symlink(&link_target, paths.bin_dir.join("huddle")).expect("symlink");
    write_record(&paths, 1234, port, "/other/huddle");
    let old_session = script(&home, "huddle-old", "touch '/nonexistent/never'");
    let line = start(&StartOptions {
        env: &env,
        paths: &paths,
        port,
        executable: old_session,
        budget: Duration::from_secs(1),
    })
    .expect("start");
    assert_eq!(
        line,
        format!("huddle-router: http://127.0.0.1:{port} (zai)")
    );
}

#[test]
fn may_take_over_when_no_installed_link_exists_or_it_names_the_starter() {
    let home = TempHome::new();
    let paths = paths(&home);
    let starter = script(&home, "huddle-2", "");
    assert!(may_take_over(&paths, &starter), "no installed link");
    fs::create_dir_all(&paths.bin_dir).expect("mkdir");
    symlink(&starter, paths.bin_dir.join("huddle")).expect("symlink");
    assert!(may_take_over(&paths, &starter));
    let other = script(&home, "huddle-1", "");
    assert!(!may_take_over(&paths, &other));
}

#[test]
fn same_binary_compares_resolved_files() {
    let home = TempHome::new();
    let target = script(&home, "real", "");
    let link = home.root().join("link");
    symlink(&target, &link).expect("symlink");
    assert!(same_binary(&target, &target));
    assert!(same_binary(&link, &target));
    let other = script(&home, "other", "");
    assert!(!same_binary(&target, &other));
    assert!(!same_binary(&home.root().join("gone"), &target));
    assert!(same_binary(
        &home.root().join("gone"),
        &home.root().join("gone")
    ));
}

#[test]
fn the_deadline_for_a_takeover_is_ten_seconds_by_default() {
    // Pins the constant the spawn loop uses. A change to the wait must be a decision.
    let _ = Instant::now();
    assert_eq!(huddle_daemon::start::UPDATE_WAIT, Duration::from_secs(10));
}
