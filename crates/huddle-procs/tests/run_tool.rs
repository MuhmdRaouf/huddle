use huddle_procs::stream::{DEFAULT_TOOL_OUTPUT, ToolSpec, run_tool, tool_version};
use huddle_testkit::temp_home::TempHome;
use std::path::PathBuf;
use std::time::{Duration, Instant};

fn sh(script: &str) -> ToolSpec {
    ToolSpec {
        program: PathBuf::from("/bin/sh"),
        args: vec!["-c".into(), script.into()],
        cwd: None,
        env: Some(vec![("PATH".into(), "/usr/bin:/bin".into())]),
        stdin: None,
        timeout: None,
        max_output: DEFAULT_TOOL_OUTPUT,
    }
}

#[test]
fn it_collects_stdout_stderr_and_the_code() {
    let out = run_tool(sh("echo out; echo err 1>&2; exit 4")).expect("run");
    assert_eq!(out.code, Some(4));
    assert_eq!(out.stdout, b"out\n");
    assert_eq!(out.stderr, b"err\n");
}

#[test]
fn stdin_is_written_then_closed() {
    let mut spec = sh("cat");
    spec.stdin = Some(b"hello".to_vec());
    let out = run_tool(spec).expect("run");
    assert_eq!(out.stdout, b"hello");
}

#[test]
fn a_timeout_sends_sigterm() {
    // The child is the sleeper itself: a wrapper shell would fork the sleep and
    // survive the SIGTERM, leaving the sleep to hold the output pipes.
    let mut spec = sh("exec sleep 30");
    spec.timeout = Some(Duration::from_millis(100));
    let started = Instant::now();
    let out = run_tool(spec).expect("run");
    assert_eq!(out.signal.as_deref(), Some("SIGTERM"));
    // The scheduler bounds the abort, not the tool: the run ends on the
    // SIGTERM, long before the child's own thirty seconds would end it.
    assert!(started.elapsed() < Duration::from_secs(25));
}

#[test]
fn past_the_budget_the_tool_is_killed() {
    let mut spec = sh(r#"while :; do printf 'xxxxxxxx'; done"#);
    spec.max_output = 4096;
    let out = run_tool(spec).expect("run");
    assert_eq!(out.signal.as_deref(), Some("SIGKILL"));
}

#[test]
fn a_missing_tool_is_an_error_value() {
    let mut spec = sh("exit 0");
    spec.program = PathBuf::from("/nonexistent/tool");
    assert!(run_tool(spec).is_err());
}

#[test]
fn tool_version_takes_the_first_line() {
    let home = TempHome::new();
    let bin = home.root().join("vtool");
    std::fs::write(&bin, "#!/bin/sh\necho omp/18.6.1\necho second\n").expect("write");
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).expect("chmod");
    let got =
        tool_version(&bin, &[], &["--version".to_string()], home.root(), None).expect("version");
    assert_eq!(got, "omp/18.6.1");
}

#[test]
fn tool_version_failures_are_exact() {
    let home = TempHome::new();
    let bad = home.root().join("badtool");
    std::fs::write(&bad, "#!/bin/sh\nexit 1\n").expect("write");
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(&bad, std::fs::Permissions::from_mode(0o755)).expect("chmod");
    let err =
        tool_version(&bad, &[], &["--version".to_string()], home.root(), None).expect_err("fails");
    assert_eq!(err, format!("command failed: {} --version", bad.display()));

    let silent = home.root().join("silent");
    std::fs::write(&silent, "#!/bin/sh\nexit 0\n").expect("write");
    std::fs::set_permissions(&silent, std::fs::Permissions::from_mode(0o755)).expect("chmod");
    let err = tool_version(&silent, &[], &["--version".to_string()], home.root(), None)
        .expect_err("fails");
    assert_eq!(err, "printed no version");

    let missing = home.root().join("nope");
    assert!(tool_version(&missing, &[], &["--version".to_string()], home.root(), None).is_err());
}
