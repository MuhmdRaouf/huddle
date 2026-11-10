use huddle_procs::detached::{DetachError, DetachSpec, start_detached};
use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    path::Path,
    thread,
    time::{Duration, Instant},
};

fn spec(home: &TempHome, script: &str) -> DetachSpec {
    DetachSpec {
        program: "/bin/sh".into(),
        args: vec!["-c".into(), script.into()],
        env: home.env(),
        log_file: None,
        cwd: home.root().to_path_buf(),
    }
}

fn wait_for_line(path: &Path) -> String {
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if let Ok(text) = fs::read_to_string(path)
            && text.ends_with('\n')
        {
            return text;
        }
        assert!(Instant::now() < deadline, "no line in {}", path.display());
        thread::sleep(Duration::from_millis(20));
    }
}

#[test]
fn a_detached_child_leads_its_own_session_and_is_not_our_child() {
    let home = TempHome::new();
    let out = home.root().join("ids");
    start_detached(&spec(
        &home,
        &format!("echo \"$$ $PPID\" > '{}'; sleep 30", out.display()),
    ))
    .expect("start");
    let line = wait_for_line(&out);
    let mut fields = line.split_whitespace();
    let pid: i32 = fields.next().expect("pid").parse().expect("pid number");
    let ppid: i32 = fields.next().expect("ppid").parse().expect("ppid number");
    assert_ne!(
        ppid,
        std::process::id() as i32,
        "the test process must not be the parent"
    );
    // SAFETY: getsid only reads the pid's session id.
    assert_eq!(
        unsafe { libc::getsid(pid) },
        pid,
        "the child leads its own session"
    );
    // SAFETY: the pid leads the session the test started, so the negative pid
    // signals its whole group: the script's shell and the sleep it forked.
    assert_eq!(unsafe { libc::kill(-pid, libc::SIGKILL) }, 0);
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        // SAFETY: kill with signal 0 only probes the group's existence.
        let group_gone = unsafe { libc::kill(-pid, 0) } != 0 || Instant::now() >= deadline;
        if group_gone {
            break;
        }
        thread::sleep(Duration::from_millis(25));
    }
    // SAFETY: kill with signal 0 only probes the group's existence.
    assert_ne!(unsafe { libc::kill(-pid, 0) }, 0, "the group is gone");
}

#[test]
fn a_detached_child_appends_its_output_to_a_private_log() {
    let home = TempHome::new();
    let log = home.root().join("out.log");
    let mut detach = spec(&home, "echo hello; echo world 1>&2");
    detach.log_file = Some(log.clone());
    start_detached(&detach).expect("start");
    let deadline = Instant::now() + Duration::from_secs(5);
    while !fs::read_to_string(&log)
        .unwrap_or_default()
        .contains("world")
    {
        assert!(Instant::now() < deadline, "no output in the log");
        thread::sleep(Duration::from_millis(20));
    }
    let text = fs::read_to_string(&log).expect("log");
    assert!(text.contains("hello"), "stdout went to the log: {text}");
    use std::os::unix::fs::PermissionsExt;
    assert_eq!(
        fs::metadata(&log).expect("stat").permissions().mode() & 0o777,
        0o600
    );
}

#[test]
fn a_detached_child_runs_in_the_directory_it_was_given_with_only_its_env() {
    let home = TempHome::new();
    let out = home.root().join("where");
    let cwd = home.root().join("work");
    fs::create_dir(&cwd).expect("mkdir");
    let mut detach = spec(
        &home,
        &format!(
            "pwd -P > '{}'; echo \"[$MARKER]\" >> '{}'",
            out.display(),
            out.display()
        ),
    );
    detach.cwd = cwd.clone();
    detach.env = vec![("PATH".to_string(), "/usr/bin:/bin".to_string())];
    start_detached(&detach).expect("start");
    let deadline = Instant::now() + Duration::from_secs(5);
    while !fs::read_to_string(&out).unwrap_or_default().contains('[') {
        assert!(Instant::now() < deadline, "no output");
        thread::sleep(Duration::from_millis(20));
    }
    let text = fs::read_to_string(&out).expect("out");
    assert!(
        text.starts_with(cwd.canonicalize().expect("canon").to_str().expect("utf8")),
        "ran in {text}"
    );
    assert!(text.contains("[]"), "MARKER leaked from the parent: {text}");
}

#[test]
fn a_program_that_is_missing_is_a_spawn_error() {
    let home = TempHome::new();
    let mut detach = spec(&home, "");
    detach.program = home.root().join("no-such-binary");
    assert!(matches!(
        start_detached(&detach),
        Err(DetachError::Spawn { .. })
    ));
}
