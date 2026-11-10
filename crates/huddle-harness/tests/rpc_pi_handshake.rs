//! pi's handshake: one `get_state` that must name a session id, killed on every failure but an
//! exit, and the benign session-create stderr line.

use huddle_harness::rpc_common::{PlainDecoder, RpcConn, RpcErrorKind, shared_counter};
use huddle_harness::rpc_pi::{handshake, is_benign_stderr};
use huddle_procs::group::group_alive;
use huddle_procs::stream::{Limits, Spec, Stdin, spawn_streaming};
use huddle_testkit::temp_home::TempHome;
use std::path::Path;
use std::thread::sleep;
use std::time::{Duration, Instant};

fn conn(script: &str, cwd: &Path) -> RpcConn<PlainDecoder> {
    let stream = spawn_streaming(
        Spec {
            program: "/bin/sh".into(),
            args: vec!["-c".into(), script.into()],
            cwd: cwd.to_path_buf(),
            env: vec![("PATH".into(), "/usr/bin:/bin".into())],
            stdin: Stdin::Piped,
            stdout_log: None,
            benign_stderr: Some(is_benign_stderr),
            reap_group: false,
            soft_stop: None,
        },
        Limits {
            grace: Duration::from_millis(200),
            timeout: None,
            output_cap: 67_108_864,
        },
    )
    .expect("spawn");
    RpcConn::new(stream, PlainDecoder, "pi", shared_counter())
}

const STATE: &str = r#"{"type":"response","id":"c1","command":"get_state","success":true,"data":{"model":{"id":"glm-5.3","provider":"zai"},"thinkingLevel":"low","sessionFile":"s.jsonl","messageCount":0,"sessionId":"00000000-0000-7000-8000-0000000000a1"}}"#;

fn wait_gone(pid: u32) {
    let deadline = Instant::now() + Duration::from_secs(5);
    while group_alive(pid) && Instant::now() < deadline {
        sleep(Duration::from_millis(25));
    }
    assert!(!group_alive(pid));
}

#[test]
fn the_handshake_returns_the_session_id() {
    let home = TempHome::new();
    let script = format!(
        r#"read one; [ "$one" = '{{"id":"c1","type":"get_state"}}' ] || exit 21; printf '%s\n' '{STATE}'; cat >/dev/null"#
    );
    let mut c = conn(&script, home.root());
    let done = handshake(&mut c, Duration::from_secs(10)).expect("handshake");
    assert_eq!(done.session_id, "00000000-0000-7000-8000-0000000000a1");
    c.end_input();
}

#[test]
fn a_get_state_without_a_session_id_is_a_protocol_error_and_kills_pi() {
    let home = TempHome::new();
    let script = r#"read one; printf '%s\n' '{"type":"response","id":"c1","command":"get_state","success":true,"data":{"model":{}}}'; sleep 30"#;
    let mut c = conn(script, home.root());
    let pid = c.pid();
    let err = handshake(&mut c, Duration::from_secs(10)).expect_err("protocol");
    assert_eq!(err.kind, RpcErrorKind::Protocol);
    assert_eq!(err.message, "pi's get_state answer carries no sessionId");
    wait_gone(pid);
}

#[test]
fn a_timeout_kills_pi_and_names_the_wait() {
    let home = TempHome::new();
    let mut c = conn("sleep 30", home.root());
    let pid = c.pid();
    let err = handshake(&mut c, Duration::from_millis(100)).expect_err("timeout");
    assert_eq!(err.kind, RpcErrorKind::Timeout);
    assert_eq!(
        err.message,
        "pi did not complete the RPC handshake within 100 ms"
    );
    wait_gone(pid);
}

#[test]
fn a_pi_that_dies_first_reports_the_exit_and_is_not_killed_as_a_failure() {
    let home = TempHome::new();
    let mut c = conn("echo boom 1>&2; exit 2", home.root());
    let err = handshake(&mut c, Duration::from_secs(10)).expect_err("exited");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(err.message, "pi ended (exit code 2): boom");
}

#[test]
fn the_session_create_warning_is_benign() {
    assert!(is_benign_stderr(
        "Warning: No project session found with id 'abc'; creating a new session with that id."
    ));
    assert!(!is_benign_stderr(
        "Warning: No project session found with id ''; creating a new session with that id."
    ));
    assert!(!is_benign_stderr("Warning: something else"));
    assert!(!is_benign_stderr(""));
}

use huddle_testkit::fake_harness::{FakeHarness, Mode, RpcEnd};

fn fake_conn(fake: &FakeHarness, cwd: &Path) -> RpcConn<PlainDecoder> {
    let stream = spawn_streaming(
        Spec {
            program: fake.path().to_path_buf(),
            args: vec!["--mode".into(), "rpc".into()],
            cwd: cwd.to_path_buf(),
            env: vec![("PATH".into(), "/usr/bin:/bin".into())],
            stdin: Stdin::Piped,
            stdout_log: None,
            benign_stderr: Some(is_benign_stderr),
            reap_group: false,
            soft_stop: None,
        },
        Limits {
            grace: Duration::from_millis(200),
            timeout: None,
            output_cap: 67_108_864,
        },
    )
    .expect("spawn");
    RpcConn::new(stream, PlainDecoder, "pi", shared_counter())
}

#[test]
fn the_handshake_runs_against_the_testkit_fake_pi() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "pi",
        Mode::PiRpc {
            session: "P1".into(),
            turns: vec![],
            end: RpcEnd::Normal,
        },
        0,
        "",
    )
    .expect("fake");
    let mut c = fake_conn(&fake, home.root());
    let done = handshake(&mut c, Duration::from_secs(10)).expect("handshake");
    assert_eq!(done.session_id, "P1");
    c.end_input();
    assert_eq!(c.stream().wait().code, Some(0));
    assert_eq!(
        fake.calls().expect("calls")[0].stdin,
        vec![r#"{"id":"c1","type":"get_state"}"#.to_string()]
    );
}

#[test]
fn a_fake_pi_that_dies_at_once_is_an_exit() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "pi",
        Mode::PiRpc {
            session: "P1".into(),
            turns: vec![],
            end: RpcEnd::DieAfterReady,
        },
        0,
        "boom\n",
    )
    .expect("fake");
    let mut c = fake_conn(&fake, home.root());
    let err = handshake(&mut c, Duration::from_secs(10)).expect_err("exit");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(err.message, "pi ended (exit code 1): boom");
}
