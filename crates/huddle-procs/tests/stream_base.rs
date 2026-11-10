use huddle_procs::group::{group_alive, signal_group, signal_name};
use huddle_procs::stream::{Limits, NextLine, Spec, Stdin, Streaming, spawn_streaming};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::time::Duration;

fn limits() -> Limits {
    Limits {
        grace: Duration::from_millis(200),
        timeout: None,
        output_cap: u64::MAX,
    }
}

fn spec(script: &str, stdin: Stdin, cwd: PathBuf) -> Spec {
    Spec {
        program: "/bin/sh".into(),
        args: vec!["-c".into(), script.into()],
        cwd,
        env: vec![("PATH".into(), "/usr/bin:/bin".into())],
        stdin,
        stdout_log: None,
        benign_stderr: None,
        reap_group: false,
        soft_stop: None,
    }
}

fn collect(stream: &mut Streaming) -> Vec<String> {
    let mut out = Vec::new();
    loop {
        match stream.next_line(Some(Duration::from_secs(10))) {
            NextLine::Line(line) => out.push(line),
            NextLine::Eof => return out,
            NextLine::Timeout => panic!("no line within 10 s"),
        }
    }
}

#[test]
fn stdout_arrives_as_lines_and_is_logged_byte_for_byte() {
    let home = TempHome::new();
    let log = home.root().join("nest/run.log");
    let mut cmd = spec(
        "printf 'a\\r\\nb\\nlast'",
        Stdin::Text(String::new()),
        home.root().to_path_buf(),
    );
    cmd.stdout_log = Some(log.clone());
    let mut stream = spawn_streaming(cmd, limits()).expect("spawn");
    assert_eq!(collect(&mut stream), vec!["a\r", "b", "last"]);
    let exit = stream.wait();
    assert_eq!(exit.code, Some(0));
    assert_eq!(fs::read_to_string(&log).expect("log"), "a\r\nb\nlast");
    assert_eq!(
        fs::metadata(&log).expect("meta").permissions().mode() & 0o777,
        0o600
    );
}

#[test]
fn the_child_leads_its_own_process_group() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        spec("sleep 30", Stdin::Piped, home.root().to_path_buf()),
        limits(),
    )
    .expect("spawn");
    let pid = stream.pid();
    // SAFETY: getpgid only reads the pid's group id.
    assert_eq!(unsafe { libc::getpgid(pid as i32) }, pid as i32);
    assert!(group_alive(pid));
    assert!(signal_group(pid, libc::SIGKILL));
    assert!(matches!(
        stream.next_line(Some(Duration::from_secs(10))),
        NextLine::Eof
    ));
    let exit = stream.wait();
    assert_eq!(exit.code, None);
    assert_eq!(exit.signal.as_deref(), Some("SIGKILL"));
}

#[test]
fn stdin_takes_lines_and_end_input_closes_it() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        spec("cat", Stdin::Piped, home.root().to_path_buf()),
        limits(),
    )
    .expect("spawn");
    assert!(stream.write_line("one"));
    assert!(stream.write_line("two"));
    stream.end_input();
    assert!(!stream.write_line("three"));
    assert_eq!(collect(&mut stream), vec!["one", "two"]);
    assert_eq!(stream.wait().code, Some(0));
}

#[test]
fn a_large_stdin_text_never_blocks_the_start() {
    let home = TempHome::new();
    let big = "x".repeat(2 * 1024 * 1024);
    let mut stream = spawn_streaming(
        spec("cat", Stdin::Text(big.clone()), home.root().to_path_buf()),
        limits(),
    )
    .expect("spawn");
    assert_eq!(collect(&mut stream), vec![big]);
    assert_eq!(stream.wait().code, Some(0));
}

#[test]
fn the_stderr_tail_leaves_out_benign_lines_and_stays_short() {
    fn benign(line: &str) -> bool {
        line.starts_with("Still starting")
    }
    let home = TempHome::new();
    let script = r#"echo "Still starting after 3s" 1>&2; echo real-error 1>&2; exit 3"#;
    let mut cmd = spec(script, Stdin::Piped, home.root().to_path_buf());
    cmd.benign_stderr = Some(benign);
    let stream = spawn_streaming(cmd, limits()).expect("spawn");
    let exit = stream.wait();
    assert_eq!(exit.code, Some(3));
    assert_eq!(exit.stderr_tail.trim(), "real-error");

    let long = spec(
        r#"i=0; while [ $i -lt 5000 ]; do printf 'x'; i=$((i+1)); done 1>&2"#,
        Stdin::Piped,
        home.root().to_path_buf(),
    );
    let stream = spawn_streaming(long, limits()).expect("spawn");
    assert_eq!(stream.wait().stderr_tail.chars().count(), 4000);
}

#[test]
fn a_start_failure_is_a_value() {
    let home = TempHome::new();
    let mut cmd = spec("exit 0", Stdin::Piped, home.root().to_path_buf());
    cmd.program = "/nonexistent/binary".into();
    assert!(spawn_streaming(cmd, limits()).is_err());
}

#[test]
fn a_log_that_cannot_be_opened_stops_the_start() {
    let home = TempHome::new();
    let blocker = home.root().join("blocker");
    fs::write(&blocker, "x").expect("write");
    let mut cmd = spec("exit 0", Stdin::Piped, home.root().to_path_buf());
    cmd.stdout_log = Some(blocker.join("run.log"));
    assert!(spawn_streaming(cmd, limits()).is_err());
}

#[test]
fn signal_names_read_like_node() {
    assert_eq!(signal_name(libc::SIGTERM), "SIGTERM");
    assert_eq!(signal_name(libc::SIGKILL), "SIGKILL");
    assert_eq!(signal_name(0), "SIG0");
}

#[test]
fn a_write_after_exit_returns_false() {
    let home = TempHome::new();
    let stream = spawn_streaming(
        spec("exit 0", Stdin::Piped, home.root().to_path_buf()),
        limits(),
    )
    .expect("spawn");
    let _ = stream.wait();
    let mut wrote = true;
    for _ in 0..100 {
        wrote = stream.write_line("x");
        if !wrote {
            break;
        }
        std::thread::sleep(Duration::from_millis(5));
    }
    assert!(!wrote);
}
