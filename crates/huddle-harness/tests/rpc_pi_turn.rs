use huddle_harness::rpc_common::{
    PlainDecoder, RpcConn, RpcErrorKind, parse_frame, shared_counter,
};
use huddle_harness::rpc_pi::{PiRun, Tally, fold_frame, handshake, is_benign_stderr, output};
use huddle_harness::table::RunEvent;
use huddle_procs::stream::{Limits, Spec, Stdin, spawn_streaming};
use huddle_testkit::temp_home::TempHome;
use std::path::Path;
use std::time::Duration;

const STATE: &str = r#"{"type":"response","id":"c1","command":"get_state","success":true,"data":{"sessionId":"00000000-0000-7000-8000-0000000000a1"}}"#;
const ACK: &str = r#"{"type":"response","id":"c2","command":"prompt","success":true,"data":{"disposition":"started"}}"#;
const END: &str = r#"{"type":"agent_settled"}"#;

fn assistant(text: &str, stop: &str) -> String {
    format!(
        r#"{{"type":"message_end","message":{{"role":"assistant","content":[{{"type":"text","text":"{text}"}}],"api":"anthropic-messages","provider":"zai","model":"glm-5.3","usage":{{"input":100,"output":20,"cacheRead":50,"cacheWrite":0,"cost":{{"total":0.0001}}}},"stopReason":"{stop}"}},"messageId":"m"}}"#
    )
}

fn peer(after_prompt: &[String], cwd: &Path) -> PiRun {
    let mut script = format!("read one; printf '%s\\n' '{STATE}'; read prompt_line\n");
    for line in after_prompt {
        script.push_str(&format!("printf '%s\\n' '{line}'\n"));
    }
    script.push_str("cat >/dev/null\nexit 0\n");
    let stream = spawn_streaming(
        Spec {
            program: "/bin/sh".into(),
            args: vec!["-c".into(), script],
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
    let mut conn = RpcConn::new(stream, PlainDecoder, "pi", shared_counter());
    let done = handshake(&mut conn, Duration::from_secs(10)).expect("handshake");
    PiRun::new(conn, done, &mut |_| {})
}

#[test]
fn a_turn_ends_at_agent_settled_and_tallies_the_message() {
    let home = TempHome::new();
    let mut run = peer(
        &[ACK.into(), assistant("Done.", "stop"), END.into()],
        home.root(),
    );
    let mut events = Vec::new();
    run.prompt("go", &mut |e| events.push(e.clone()))
        .expect("turn");
    assert_eq!(run.final_text(), "Done.");
    assert_eq!(run.tally().turns, 1);
    assert_eq!(run.tally().model.as_deref(), Some("zai/glm-5.3"));
    assert_eq!(run.tally().input_tokens, 100);
    assert!(events.contains(&RunEvent::AssistantText("Done.".into())));
    assert_eq!(run.close(&mut |_| {}).code, Some(0));
}

#[test]
fn a_provider_failure_arrives_on_the_message_stream() {
    let home = TempHome::new();
    let error = r#"{"type":"message_end","message":{"role":"assistant","content":[],"provider":"zai","model":"glm-5.3","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}},"stopReason":"error","errorMessage":"401 token expired"},"messageId":"m"}"#;
    let mut run = peer(&[ACK.into(), error.to_string(), END.into()], home.root());
    let mut events = Vec::new();
    run.prompt("go", &mut |e| events.push(e.clone()))
        .expect("turn");
    assert!(run.failed_stop());
    assert!(events.contains(&RunEvent::Error {
        message: "401 token expired".into(),
        status: None
    }));
    let out = output("S", run.tally(), 5);
    assert_eq!(out.failure.as_deref(), Some("401 token expired"));
    assert_eq!(out.stats.api_error_status, Some(401));
    run.close(&mut |_| {});
}

#[test]
fn a_textless_stop_recovers_with_get_last_assistant_text() {
    let home = TempHome::new();
    let empty_stop = r#"{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop"},"messageId":"m"}"#;
    let text_reply = r#"{"type":"response","id":"c3","command":"get_last_assistant_text","success":true,"data":{"text":"recovered"}}"#;
    let mut run = peer(
        &[
            ACK.into(),
            empty_stop.to_string(),
            END.into(),
            text_reply.to_string(),
        ],
        home.root(),
    );
    run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(run.final_text(), "recovered");
    run.close(&mut |_| {});
}

#[test]
fn a_refused_prompt_is_an_error_value() {
    let home = TempHome::new();
    let refused =
        r#"{"type":"response","id":"c2","command":"prompt","success":false,"error":"busy"}"#;
    let mut run = peer(&[refused.to_string()], home.root());
    let err = run.prompt("go", &mut |_| {}).expect_err("refused");
    assert_eq!(err.kind, RpcErrorKind::Rejected);
    assert_eq!(err.message, "busy");
    run.close(&mut |_| {});
}

#[test]
fn dying_mid_turn_fails_the_turn() {
    let home = TempHome::new();
    let script = format!(
        "read one; printf '%s\\n' '{STATE}'; read prompt_line; printf '%s\\n' '{ACK}'; exit 5\n"
    );
    let stream = spawn_streaming(
        Spec {
            program: "/bin/sh".into(),
            args: vec!["-c".into(), script],
            cwd: home.root().to_path_buf(),
            env: vec![("PATH".into(), "/usr/bin:/bin".into())],
            stdin: Stdin::Piped,
            stdout_log: None,
            benign_stderr: None,
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
    let mut conn = RpcConn::new(stream, PlainDecoder, "pi", shared_counter());
    let done = handshake(&mut conn, Duration::from_secs(10)).expect("handshake");
    let mut run = PiRun::new(conn, done, &mut |_| {});
    let err = run.prompt("go", &mut |_| {}).expect_err("exited");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert!(err.message.starts_with("pi ended"));
}

#[test]
fn an_extension_confirm_is_answered_and_the_turn_continues() {
    let home = TempHome::new();
    // The peer waits for the reply line before finishing the turn.
    let script = format!(
        "read one; printf '%s\\n' '{STATE}'; read prompt_line
printf '%s\\n' '{ACK}'
printf '%s\\n' '{{\"type\":\"extension_ui_request\",\"id\":\"e1\",\"method\":\"confirm\"}}'
read reply
printf '%s\\n' '{{\"type\":\"extension_ui_request\",\"id\":\"e2\",\"method\":\"input\"}}'
read reply2
[ \"$reply\" = '{{\"type\":\"extension_ui_response\",\"id\":\"e1\",\"confirmed\":true}}' ] || exit 31
[ \"$reply2\" = '{{\"type\":\"extension_ui_response\",\"id\":\"e2\",\"cancelled\":true}}' ] || exit 32
printf '%s\\n' '{assistant_line}'
printf '%s\\n' '{END}'
cat >/dev/null",
        assistant_line = assistant("ok", "stop")
    );
    let stream = spawn_streaming(
        Spec {
            program: "/bin/sh".into(),
            args: vec!["-c".into(), script],
            cwd: home.root().to_path_buf(),
            env: vec![("PATH".into(), "/usr/bin:/bin".into())],
            stdin: Stdin::Piped,
            stdout_log: None,
            benign_stderr: None,
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
    let mut conn = RpcConn::new(stream, PlainDecoder, "pi", shared_counter());
    let done = handshake(&mut conn, Duration::from_secs(10)).expect("handshake");
    let mut run = PiRun::new(conn, done, &mut |_| {});
    run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(run.final_text(), "ok");
    assert_eq!(run.close(&mut |_| {}).code, Some(0));
}

#[test]
fn usage_falls_back_to_the_cumulative_message_update() {
    let mut tally = Tally::default();
    tally.apply(&parse_frame(r#"{"type":"message_update","usage":{"input":100,"output":20,"cacheRead":50,"cacheWrite":3,"cost":{"total":0.5}}}"#).expect("f"));
    tally.apply(&parse_frame(r#"{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"stopReason":"stop"}}"#).expect("f"));
    assert_eq!(tally.input_tokens, 100);
    assert_eq!(tally.cache_write_tokens, 3);
    assert!((tally.cost_usd - 0.5).abs() < 1e-9);

    let mut tally = Tally::default();
    tally.apply(&parse_frame(r#"{"type":"message_update","usage":{"input":1}}"#).expect("f"));
    tally.apply(&parse_frame(r#"{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input":7},"stopReason":"stop"}}"#).expect("f"));
    assert_eq!(tally.input_tokens, 7, "the message's own number wins");
}

#[test]
fn fold_covers_pis_frames() {
    let f = |t: &str| parse_frame(t).expect("frame");
    assert_eq!(fold_frame(&f(r#"{"type":"agent_settled"}"#)), vec![]);
    assert_eq!(
        fold_frame(&f(
            r#"{"type":"tool_execution_update","toolCallId":"c","toolName":"bash","partialResult":{}}"#
        )),
        vec![]
    );
    assert_eq!(
        fold_frame(&f(
            r#"{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"x"}}"#
        )),
        vec![RunEvent::TextDelta("x".into())]
    );
    assert_eq!(
        fold_frame(&f(
            r#"{"type":"tool_execution_start","toolName":"find","args":{"n":1},"intent":"nope"}"#
        )),
        vec![RunEvent::ToolUse {
            name: "find".into(),
            summary: "".into()
        }]
    );
    assert_eq!(
        fold_frame(&f(r#"{"type":"tool_execution_end","isError":"true"}"#)),
        vec![RunEvent::ToolResult { is_error: false }]
    );
    assert_eq!(
        fold_frame(&f(
            r#"{"type":"response","command":"get_state","success":true,"data":{"sessionId":"S"}}"#
        )),
        vec![RunEvent::Session("S".into())]
    );
    assert_eq!(
        fold_frame(&f(
            r#"{"type":"response","command":"prompt","success":false}"#
        )),
        vec![RunEvent::Error {
            message: "pi refused prompt".into(),
            status: None
        }]
    );
    assert_eq!(
        fold_frame(&f(
            r#"{"type":"auto_retry_start","attempt":2,"maxAttempts":3,"errorMessage":"500 down"}"#
        )),
        vec![RunEvent::ApiRetry {
            attempt: 2,
            max_attempts: 3,
            status: Some(500)
        }]
    );
}

#[test]
fn output_reports_an_unfinished_run() {
    let mut tally = Tally::default();
    tally.last_stop_reason = "toolUse".into();
    let out = output("S", &tally, 1);
    assert_eq!(
        out.failure.as_deref(),
        Some("worker stopped without finishing (stopReason toolUse)")
    );
    assert_eq!(out.session_id.as_deref(), Some("S"));
    tally.last_stop_reason = "stop".into();
    tally.final_text = "t".into();
    assert_eq!(output("S", &tally, 1).failure, None);
}

use huddle_harness::rpc_common::abort_soft_stop;
use huddle_procs::stream::{Forced, StopHandle};
use huddle_testkit::fake_harness::{FakeHarness, Mode, RpcEnd, RpcTurn};

fn fake_run(
    home: &TempHome,
    turns: Vec<RpcTurn>,
    end: RpcEnd,
    stderr: &str,
) -> (FakeHarness, PiRun, StopHandle) {
    let fake = FakeHarness::install(
        home,
        "pi",
        Mode::PiRpc {
            session: "P1".into(),
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
            output_cap: 67_108_864,
        },
    )
    .expect("spawn");
    let stop = stream.stop_handle();
    let mut conn = RpcConn::new(stream, PlainDecoder, "pi", counter);
    let done = handshake(&mut conn, Duration::from_secs(10)).expect("handshake");
    let run = PiRun::new(conn, done, &mut |_| {});
    (fake, run, stop)
}

#[test]
fn a_whole_turn_runs_against_the_testkit_fake() {
    let home = TempHome::new();
    let (fake, mut run, _stop) = fake_run(
        &home,
        vec![RpcTurn {
            text: "Done.".into(),
        }],
        RpcEnd::Normal,
        "",
    );
    run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(run.final_text(), "Done.");
    assert_eq!(run.session_id(), "P1");
    assert_eq!(run.close(&mut |_| {}).code, Some(0));
    assert_eq!(
        fake.calls().expect("calls")[0].stdin[1],
        r#"{"id":"c2","type":"prompt","message":"go"}"#
    );
}

#[test]
fn a_fake_crash_mid_turn_fails_the_turn() {
    let home = TempHome::new();
    let (_fake, mut run, _stop) = fake_run(
        &home,
        vec![RpcTurn { text: "x".into() }],
        RpcEnd::CrashMidTurn,
        "boom\n",
    );
    let err = run.prompt("go", &mut |_| {}).expect_err("crash");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(err.message, "pi ended (exit code 3): boom");
}

#[test]
fn the_soft_stop_settles_a_hanging_turn_as_aborted() {
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
    run.prompt("go", &mut |_| {}).expect("turn");
    assert_eq!(run.tally().last_stop_reason, "aborted");
    let exit = run.close(&mut |_| {});
    stopper.join().expect("join");
    assert_eq!(exit.forced, Some(Forced::Stopped));
    assert_eq!(
        output("P1", run.tally(), 1).failure.as_deref(),
        Some("worker stopped without finishing (stopReason aborted)")
    );
}
