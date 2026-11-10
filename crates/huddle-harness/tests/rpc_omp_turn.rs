//! One omp turn against a scripted peer and the testkit fake: the ack, the frames, the tally
//! and the run's output.

use huddle_harness::rpc_common::{Frame, RpcConn, RpcErrorKind, parse_frame, shared_counter};
use huddle_harness::rpc_omp::{
    OmpDecoder, OmpRun, OmpTurn, Tally, fold_frame, handshake, is_benign_stderr, output,
};
use huddle_harness::table::RunEvent;
use huddle_procs::stream::{Limits, Spec, Stdin, spawn_streaming};
use huddle_testkit::temp_home::TempHome;
use std::path::Path;
use std::time::Duration;

fn frame(text: &str) -> Frame {
    parse_frame(text).expect("frame")
}

fn peer(after_prompt: &[&str], home: &Path) -> OmpRun {
    let mut script = String::from(
        r#"printf '%s\n' '{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1,2],"maxFrameBytes":1048576}'
read one; read two; read three
printf '%s\n' '{"id":"c1","type":"response","command":"negotiate_protocol","success":true}'
printf '%s\n' '{"id":"c2","type":"response","command":"set_event_filter","success":true}'
printf '%s\n' '{"id":"c3","type":"response","command":"get_state","success":true,"data":{"sessionId":"01a1110d"}}'
read prompt_line
"#,
    );
    for line in after_prompt {
        if *line == "EXIT:7" {
            script.push_str("exit 7\n");
        } else {
            script.push_str(&format!("printf '%s\\n' '{line}'\n"));
        }
    }
    script.push_str("cat >/dev/null\nexit 0\n");
    let stream = spawn_streaming(
        Spec {
            program: "/bin/sh".into(),
            args: vec!["-c".into(), script],
            cwd: home.to_path_buf(),
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
            output_cap: 1_048_576,
        },
    )
    .expect("spawn");
    let mut conn = RpcConn::new(stream, OmpDecoder::new(), "omp", shared_counter());
    let done = handshake(&mut conn, &[], Duration::from_secs(10)).expect("handshake");
    let mut sink = |_: &RunEvent| {};
    OmpRun::new(conn, done, &mut sink)
}

const ACK: &str = r#"{"id":"c4","type":"response","command":"prompt","success":true}"#;
const USER_END: &str = r#"{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Reply OK"}],"attribution":"user"},"messageId":"msg-1"}"#;
const ASSISTANT_END: &str = r#"{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"OK"}],"api":"anthropic-messages","provider":"zai","model":"glm-5.3","usage":{"input":3371,"output":3,"cacheRead":0,"cacheWrite":0,"totalTokens":3374,"cost":{"input":0.0047,"output":0.00001,"cacheRead":0,"cacheWrite":0,"total":0.0047326}},"stopReason":"stop"},"messageId":"msg-2"}"#;
const COMPLETED: &str = r#"{"type":"prompt_result","id":"c4","agentInvoked":true,"status":"completed","sessionSettled":true}"#;

#[test]
fn a_completed_turn_tallies_the_message_and_settles() {
    let home = TempHome::new();
    let mut run = peer(&[ACK, USER_END, ASSISTANT_END, COMPLETED], home.root());
    let mut events = Vec::new();
    let turn = run
        .prompt("Reply with the single word OK.", &mut |e| {
            events.push(e.clone())
        })
        .expect("turn");
    assert_eq!(turn.status, "completed");
    assert_eq!(run.final_text(), "OK");
    assert_eq!(run.tally().turns, 1);
    assert_eq!(run.tally().model.as_deref(), Some("zai/glm-5.3"));
    assert_eq!(run.tally().input_tokens, 3371);
    assert_eq!(run.tally().output_tokens, 3);
    assert!((run.tally().cost_usd - 0.0047326).abs() < 1e-9);
    assert!(events.contains(&RunEvent::AssistantText("OK".into())));
    let exit = run.close(&mut |_| {});
    assert_eq!(exit.code, Some(0));
}

#[test]
fn a_turn_not_settled_waits_for_session_settled() {
    let home = TempHome::new();
    let not_settled = r#"{"type":"prompt_result","id":"c4","agentInvoked":true,"status":"completed","sessionSettled":false}"#;
    let settled = r#"{"type":"session_settled"}"#;
    let mut run = peer(&[ACK, ASSISTANT_END, not_settled, settled], home.root());
    let turn = run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(turn.status, "completed");
    run.close(&mut |_| {});
}

#[test]
fn a_prompt_that_ran_no_agent_settles_at_once_with_its_error() {
    let home = TempHome::new();
    let second_ack = r#"{"id":"c4","type":"response","command":"prompt","success":false,"error":"No API key found for zai."}"#;
    let not_invoked = r#"{"type":"prompt_result","id":"c4","agentInvoked":false,"status":"error","sessionSettled":true,"error":{"message":"No API key found for zai.","retryable":false}}"#;
    let mut run = peer(&[ACK, second_ack, not_invoked], home.root());
    let turn = run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(turn.status, "error");
    assert_eq!(
        turn.error.as_ref().expect("error").message,
        "No API key found for zai."
    );
    run.close(&mut |_| {});
}

#[test]
fn a_refused_prompt_is_an_error_value() {
    let home = TempHome::new();
    let refused =
        r#"{"id":"c4","type":"response","command":"prompt","success":false,"error":"Not ready"}"#;
    let mut run = peer(&[refused], home.root());
    let err = run.prompt("go", &mut |_| {}).expect_err("refused");
    assert_eq!(err.kind, RpcErrorKind::Rejected);
    assert_eq!(err.message, "Not ready");
    run.close(&mut |_| {});
}

#[test]
fn a_crash_mid_turn_fails_the_turn_with_the_exit() {
    let home = TempHome::new();
    let mut run = peer(
        &[
            ACK,
            r#"{"type":"tool_execution_start","toolName":"bash","args":{"command":"x"}}"#,
            "EXIT:7",
        ],
        home.root(),
    );
    let err = run.prompt("go", &mut |_| {}).expect_err("crash");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert!(err.message.starts_with("omp ended"));
}

fn turn(status: &str, error: Option<(&str, Option<u16>)>) -> OmpTurn {
    OmpTurn {
        status: status.into(),
        error: error.map(|(m, s)| huddle_harness::rpc_omp::PromptError {
            message: m.into(),
            http_status: s,
        }),
        sent_at_ms: 1000,
        ended_at_ms: 2000,
    }
}

#[test]
fn output_reports_failures_and_unfinished_runs() {
    let mut tally = Tally {
        final_text: "partial".into(),
        last_stop_reason: "toolUse".into(),
        turns: 2,
        ..Default::default()
    };

    let failed = output(
        Some("s"),
        &tally,
        &turn("error", Some(("boom", Some(401)))),
        500,
    );
    assert_eq!(failed.failure.as_deref(), Some("boom"));
    assert_eq!(failed.stats.api_error_status, Some(401));
    assert_eq!(failed.stats.duration_ms, 500);
    assert_eq!(failed.session_id.as_deref(), Some("s"));

    let bare = output(None, &tally, &turn("error", None), 0);
    assert_eq!(
        bare.failure.as_deref(),
        Some("omp ended the turn in error without saying why")
    );

    let unfinished = output(None, &tally, &turn("completed", None), 0);
    assert_eq!(
        unfinished.failure.as_deref(),
        Some("worker stopped without finishing (stopReason toolUse)")
    );

    tally.last_stop_reason = "stop".into();
    let good = output(None, &tally, &turn("completed", None), 0);
    assert_eq!(good.failure, None);
    assert_eq!(good.text, "partial");

    let aborted = output(None, &tally, &turn("aborted", None), 0);
    assert_eq!(aborted.failure, None);
}

#[test]
fn fold_covers_every_frame_with_an_event() {
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"response","command":"get_state","success":true,"data":{"sessionId":"S"}}"#
        )),
        vec![RunEvent::Session("S".into())]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"response","command":"abort","success":false}"#
        )),
        vec![RunEvent::Error {
            message: "omp refused abort".into(),
            status: None
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"response","command":"prompt","success":true}"#
        )),
        vec![]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hi"}}"#
        )),
        vec![RunEvent::TextDelta("hi".into())]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"message_update","assistantMessageEvent":{"type":"text_start"}}"#
        )),
        vec![]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"401 bad"}}"#
        )),
        vec![RunEvent::Error {
            message: "401 bad".into(),
            status: None
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error"}}"#
        )),
        vec![RunEvent::Error {
            message: "omp ended an assistant message in error".into(),
            status: None
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"tool_execution_start","toolName":"read","args":{"path":"a.txt"}}"#
        )),
        vec![RunEvent::ToolUse {
            name: "read".into(),
            summary: "a.txt".into()
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"tool_execution_start","toolName":"read","args":{"n":1},"intent":"Reading"}"#
        )),
        vec![RunEvent::ToolUse {
            name: "read".into(),
            summary: "Reading".into()
        }]
    );
    assert_eq!(
        fold_frame(&frame(r#"{"type":"tool_execution_end","isError":true}"#)),
        vec![RunEvent::ToolResult { is_error: true }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"errorMessage":"429 slow"}"#
        )),
        vec![RunEvent::ApiRetry {
            attempt: 1,
            max_attempts: 10,
            status: Some(429)
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"auto_retry_start","errorMessage":"later"}"#
        )),
        vec![RunEvent::ApiRetry {
            attempt: 0,
            max_attempts: 0,
            status: None
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"prompt_result","id":"c4","agentInvoked":true,"status":"error","sessionSettled":true,"error":{"message":"401","httpStatus":401}}"#
        )),
        vec![RunEvent::Error {
            message: "401".into(),
            status: Some(401)
        }]
    );
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"prompt_result","id":"c4","agentInvoked":true,"status":"completed","sessionSettled":true}"#
        )),
        vec![]
    );
    assert_eq!(fold_frame(&frame(r#"{"type":"notice"}"#)), vec![]);
    assert_eq!(
        fold_frame(&frame(
            r#"{"type":"host_tool_call","id":"h","toolName":"submit_report"}"#
        )),
        vec![]
    );
}

use huddle_harness::rpc_common::abort_soft_stop;
use huddle_procs::stream::{Forced, StopHandle};
use huddle_testkit::fake_harness::{FakeHarness, Mode, RpcEnd, RpcTurn};

fn fake_run(
    home: &TempHome,
    turns: Vec<RpcTurn>,
    end: RpcEnd,
    stderr: &str,
) -> (FakeHarness, OmpRun, StopHandle) {
    let fake = FakeHarness::install(
        home,
        "omp",
        Mode::OmpRpc {
            session: "S1".into(),
            turns,
            end,
        },
        0,
        stderr,
    )
    .expect("fake");
    let counter = shared_counter();
    let stream = spawn_streaming(
        Spec {
            program: fake.path().to_path_buf(),
            args: vec!["--mode".into(), "rpc".into()],
            cwd: home.root().to_path_buf(),
            env: vec![("PATH".into(), "/usr/bin:/bin".into())],
            stdin: Stdin::Piped,
            stdout_log: None,
            benign_stderr: Some(is_benign_stderr),
            reap_group: false,
            soft_stop: Some(abort_soft_stop(counter.clone())),
        },
        Limits {
            grace: Duration::from_secs(5),
            timeout: None,
            output_cap: 1_048_576,
        },
    )
    .expect("spawn");
    let stop = stream.stop_handle();
    let mut conn = RpcConn::new(stream, OmpDecoder::new(), "omp", counter);
    let done = handshake(&mut conn, &[], Duration::from_secs(10)).expect("handshake");
    let run = OmpRun::new(conn, done, &mut |_| {});
    (fake, run, stop)
}

#[test]
fn a_whole_turn_runs_against_the_testkit_fake() {
    let home = TempHome::new();
    let (fake, mut run, _stop) = fake_run(
        &home,
        vec![RpcTurn { text: "OK".into() }],
        RpcEnd::Normal,
        "",
    );
    let mut events = Vec::new();
    let turn = run
        .prompt("go", &mut |e| events.push(e.clone()))
        .expect("turn");
    assert_eq!(turn.status, "completed");
    assert_eq!(run.final_text(), "OK");
    assert_eq!(run.session_id(), Some("S1"));
    assert_eq!(run.tally().model.as_deref(), Some("zai/glm-5.3"));
    assert!(events.contains(&RunEvent::TextDelta("OK".into())));
    assert_eq!(run.close(&mut |_| {}).code, Some(0));
    let calls = fake.calls().expect("calls");
    assert_eq!(
        calls[0].stdin[3],
        r#"{"id":"c4","type":"prompt","message":"go"}"#
    );
}

#[test]
fn a_fake_crash_mid_turn_fails_the_turn_with_the_exit_and_stderr() {
    let home = TempHome::new();
    let (_fake, mut run, _stop) = fake_run(
        &home,
        vec![RpcTurn { text: "x".into() }],
        RpcEnd::CrashMidTurn,
        "boom\n",
    );
    let err = run.prompt("go", &mut |_| {}).expect_err("crash");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(err.message, "omp ended (exit code 3): boom");
}

#[test]
fn the_soft_stop_aborts_a_hanging_turn() {
    let home = TempHome::new();
    let (_fake, mut run, stop) = fake_run(
        &home,
        vec![RpcTurn { text: "x".into() }],
        RpcEnd::HangUntilAbort,
        "",
    );
    let stopper = std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(200));
        stop.interrupt(Forced::Stopped);
    });
    let turn = run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(turn.status, "aborted");
    let exit = run.close(&mut |_| {});
    stopper.join().expect("join");
    assert_eq!(exit.forced, Some(Forced::Stopped));
}
