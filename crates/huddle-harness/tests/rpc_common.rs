//! The shared RPC layer's own tests: a real `/bin/sh` child speaks the frames.

use huddle_harness::rpc_common::*;
use huddle_procs::stream::{Limits, ProcExit, Spec, Stdin, spawn_streaming};
use huddle_testkit::temp_home::TempHome;
use std::path::Path;
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
            benign_stderr: None,
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
    RpcConn::new(stream, PlainDecoder, "pi", shared_counter())
}

#[test]
fn frames_parse_and_reasons_are_exact() {
    let frame = parse_frame(r#"{"type":"agent_start","x":1}"#).expect("frame");
    assert_eq!(frame.kind, "agent_start");
    assert_eq!(frame.text, r#"{"type":"agent_start","x":1}"#);
    assert_eq!(frame.bool_member("x"), None);
    assert_eq!(frame.count_member("x"), 1.0);
    assert_eq!(frame.count_member("missing"), 0.0);

    assert_eq!(parse_frame("not json").unwrap_err(), "not JSON");
    assert_eq!(parse_frame("[1,2]").unwrap_err(), "not an RPC frame");
    assert_eq!(
        parse_frame(r#"{"type":1}"#).unwrap_err(),
        "not an RPC frame"
    );
    assert_eq!(
        parse_frame(r#"{"no":"type"}"#).unwrap_err(),
        "not an RPC frame"
    );
}

#[test]
fn the_plain_decoder_strips_cr_and_skips_blank_lines() {
    let mut d = PlainDecoder;
    assert!(matches!(d.push(""), Decoded::Partial));
    assert!(matches!(d.push("\r"), Decoded::Partial));
    assert!(matches!(d.push("stray"), Decoded::Malformed { .. }));
    match d.push("{\"type\":\"agent_settled\"}\r") {
        Decoded::Frame(f) => assert_eq!(f.kind, "agent_settled"),
        _ => panic!("frame"),
    }
}

#[test]
fn text_of_joins_text_parts_and_as_response_reads_the_shape() {
    let parts = serde_json::json!([{"type":"text","text":"a"},{"type":"toolCall"},{"type":"text","text":"b"}]);
    assert_eq!(text_of(Some(&parts)), "ab");
    assert_eq!(text_of(None), "");

    let frame = parse_frame(r#"{"id":"c1","type":"response","command":"get_state","success":true,"data":{"sessionId":"s"}}"#).expect("frame");
    let response = as_response(&frame).expect("response");
    assert_eq!(response.id.as_deref(), Some("c1"));
    assert_eq!(response.command, "get_state");
    assert!(response.success);
    assert_eq!(response.data, Some(serde_json::json!({"sessionId":"s"})));

    let not = parse_frame(r#"{"type":"response","command":"x"}"#).expect("frame");
    assert_eq!(as_response(&not), None);
}

#[test]
fn with_id_puts_the_id_first_and_describe_exit_names_the_end() {
    assert_eq!(
        with_id("c9", r#"{"type":"abort"}"#),
        r#"{"id":"c9","type":"abort"}"#
    );
    let exit = ProcExit {
        code: Some(1),
        signal: None,
        stderr_tail: "no such session\n".into(),
        forced: None,
    };
    assert_eq!(
        describe_exit("omp", &exit),
        "omp ended (exit code 1): no such session"
    );
    let killed = ProcExit {
        code: None,
        signal: Some("SIGTERM".into()),
        stderr_tail: String::new(),
        forced: None,
    };
    assert_eq!(describe_exit("pi", &killed), "pi ended (signal SIGTERM)");
}

#[test]
fn reported_model_prefixes_the_provider_once() {
    let message = serde_json::json!({"provider":"zai","model":"glm-5.3"});
    assert_eq!(reported_model(&message).as_deref(), Some("zai/glm-5.3"));
    let already = serde_json::json!({"provider":"zai","model":"zai/glm-5.3"});
    assert_eq!(reported_model(&already).as_deref(), Some("zai/glm-5.3"));
    assert_eq!(reported_model(&serde_json::json!({"provider":"zai"})), None);
}

#[test]
fn a_real_exchange_matches_responses_by_id_in_any_order() {
    let home = TempHome::new();
    let script = r#"
read one; read two
printf '%s\n' '{"id":"c2","type":"response","command":"get_entries","success":true,"data":{"n":2}}'
printf '%s\n' '{"type":"note"}'
printf '%s\n' 'this is not json'
printf '%s\n' '{"id":"c1","type":"response","command":"get_state","success":true,"data":{"sessionId":"S"}}'
cat >/dev/null
"#;
    let mut c = conn(script, home.root());
    let first = c.next_id();
    let second = c.next_id();
    assert_eq!(first, "c1");
    assert_eq!(second, "c2");
    c.send(&first, r#"{"type":"get_state"}"#).expect("send");
    c.send(&second, r#"{"type":"get_entries"}"#).expect("send");
    let mut other = Vec::new();
    let answers = c
        .await_responses(
            &[first.clone(), second.clone()],
            Some(Instant::now() + Duration::from_secs(10)),
            &mut |f| other.push(f.kind.clone()),
        )
        .expect("answers");
    assert_eq!(answers[0].data, Some(serde_json::json!({"sessionId":"S"})));
    assert_eq!(answers[1].data, Some(serde_json::json!({"n":2})));
    assert!(other.contains(&"note".to_string()));
    c.end_input();
    assert!(matches!(
        c.send("c3", r#"{"type":"abort"}"#),
        Err(RpcError {
            kind: RpcErrorKind::Closed,
            ..
        })
    ));
}

#[test]
fn a_refusal_and_an_exit_are_errors_with_their_kind() {
    let home = TempHome::new();
    let refused = r#"read one
printf '%s\n' '{"id":"c1","type":"response","command":"prompt","success":false,"error":"No API key found for zai."}'
cat >/dev/null"#;
    let mut c = conn(refused, home.root());
    let id = c.next_id();
    c.send(&id, r#"{"type":"prompt","message":"hi"}"#)
        .expect("send");
    let err = c
        .await_responses(
            &[id],
            Some(Instant::now() + Duration::from_secs(10)),
            &mut |_| {},
        )
        .expect_err("rejected");
    assert_eq!(err.kind, RpcErrorKind::Rejected);
    assert_eq!(err.message, "No API key found for zai.");
    assert_eq!(err.command.as_deref(), Some("prompt"));

    let gone = r#"exit 3"#;
    let mut c = conn(gone, home.root());
    let id = c.next_id();
    let err = c
        .await_responses(
            &[id],
            Some(Instant::now() + Duration::from_secs(10)),
            &mut |_| {},
        )
        .expect_err("exited");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(err.message, "pi ended (exit code 3)");
}
