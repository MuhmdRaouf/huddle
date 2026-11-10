use huddle_harness::entries::find;
use huddle_harness::rpc_common::{
    Decoded, FrameDecoder, RpcConn, RpcError, RpcErrorKind, parse_frame, shared_counter,
};
use huddle_harness::rpc_omp::{
    OmpDecoder, event_filter_json, expected_tools, handshake, is_benign_stderr,
};
use huddle_harness::table::Access;
use huddle_procs::group::group_alive;
use huddle_procs::stream::{Limits, Spec, Stdin, spawn_streaming};
use huddle_testkit::temp_home::TempHome;
use std::path::Path;
use std::thread::sleep;
use std::time::{Duration, Instant};

const READY: &str = r#"{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1,2],"maxFrameBytes":1048576,"maxReassembledFrameBytes":67108864}"#;

fn omp_conn(script: &str, cwd: &Path) -> RpcConn<OmpDecoder> {
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
            output_cap: 1_048_576,
        },
    )
    .expect("spawn");
    RpcConn::new(stream, OmpDecoder::new(), "omp", shared_counter())
}

fn happy_script() -> String {
    format!(
        r#"printf '%s\n' '{ready}'
printf '%s\n' '{{"type":"advisor_cost_changed"}}'
read one; read two; read three
[ "$one" = '{{"id":"c1","type":"negotiate_protocol","protocolVersion":2}}' ] || exit 21
[ "$three" = '{{"id":"c3","type":"get_state"}}' ] || exit 23
printf '%s\n' '{{"id":"c1","type":"response","command":"negotiate_protocol","success":true,"data":{{"protocolVersion":2}}}}'
printf '%s\n' '{{"id":"c2","type":"response","command":"set_event_filter","success":true,"data":{{}}}}'
printf '%s\n' '{{"id":"c3","type":"response","command":"get_state","success":true,"data":{{"sessionId":"01a1110d-3edc-7325-936a-37783f0797ed","dumpTools":[{{"name":"read"}},{{"name":"grep"}},{{"name":"glob"}},{{"name":"extra"}}]}}}}'
cat >/dev/null
exit 0"#,
        ready = READY
    )
}

#[test]
fn the_handshake_negotiates_v2_and_returns_the_session() {
    let home = TempHome::new();
    let mut conn = omp_conn(&happy_script(), home.root());
    let expected = expected_tools(&find("omp").expect("omp"), Access::Readonly);
    assert_eq!(expected, vec!["read", "grep", "glob"]);
    let done = handshake(&mut conn, &expected, Duration::from_secs(10)).expect("handshake");
    assert_eq!(
        done.session_id.as_deref(),
        Some("01a1110d-3edc-7325-936a-37783f0797ed")
    );
    conn.end_input();
    drop(conn);
}

#[test]
fn the_filter_command_is_exact_and_ready_needs_protocol_two() {
    assert!(
        event_filter_json()
            .starts_with(r#"{"type":"set_event_filter","events":["message_update","#)
    );
    assert!(event_filter_json().ends_with(r#""notice"],"messageUpdates":"delta"}"#));

    let home = TempHome::new();
    let script = r#"printf '%s\n' '{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1]}'
cat >/dev/null"#;
    let mut conn = omp_conn(script, home.root());
    let err = handshake(&mut conn, &[], Duration::from_secs(10)).expect_err("protocol 1");
    assert_eq!(err.kind, RpcErrorKind::Protocol);
    assert_eq!(err.message, "omp offers RPC protocol 1; 2 is required");
}

#[test]
fn a_ready_without_the_required_members_is_skipped_until_the_timeout() {
    let home = TempHome::new();
    let script = r#"printf '%s\n' '{"type":"ready"}'
sleep 30"#;
    let mut conn = omp_conn(script, home.root());
    let err = handshake(&mut conn, &[], Duration::from_millis(100)).expect_err("timeout");
    assert_eq!(err.kind, RpcErrorKind::Timeout);
    assert_eq!(
        err.message,
        "omp did not complete the RPC handshake within 100 ms"
    );
}

#[test]
fn a_missing_tool_fails_and_kills_the_peer() {
    let home = TempHome::new();
    let script = format!(
        r#"printf '%s\n' '{ready}'
read one; read two; read three
printf '%s\n' '{{"id":"c1","type":"response","command":"negotiate_protocol","success":true}}'
printf '%s\n' '{{"id":"c2","type":"response","command":"set_event_filter","success":true}}'
printf '%s\n' '{{"id":"c3","type":"response","command":"get_state","success":true,"data":{{"sessionId":"s","dumpTools":[{{"name":"read"}}]}}}}'
sleep 30"#,
        ready = READY
    );
    let mut conn = omp_conn(&script, home.root());
    let pid = conn.pid();
    let err = handshake(
        &mut conn,
        &["read".into(), "write".into()],
        Duration::from_secs(10),
    )
    .expect_err("tools");
    assert_eq!(err.kind, RpcErrorKind::Protocol);
    assert_eq!(
        err.message,
        "omp lacks the expected tools [write]; it exposes [read]"
    );
    let deadline = Instant::now() + Duration::from_secs(5);
    while group_alive(pid) && Instant::now() < deadline {
        sleep(Duration::from_millis(25));
    }
    assert!(
        !group_alive(pid),
        "the peer the handshake gave up on is dead"
    );
}

#[test]
fn an_omp_that_exits_before_ready_reports_its_stderr() {
    let home = TempHome::new();
    let script = r#"echo 'Error: Session "x" not found.' 1>&2; exit 1"#;
    let mut conn = omp_conn(script, home.root());
    let err = handshake(&mut conn, &[], Duration::from_secs(10)).expect_err("exit");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(
        err.message,
        "omp ended (exit code 1): Error: Session \"x\" not found."
    );
}

#[test]
fn a_timeout_kills_the_peer() {
    let home = TempHome::new();
    let mut conn = omp_conn("sleep 30", home.root());
    let pid = conn.pid();
    let err = handshake(&mut conn, &[], Duration::from_millis(100)).expect_err("timeout");
    assert_eq!(err.kind, RpcErrorKind::Timeout);
    let deadline = Instant::now() + Duration::from_secs(5);
    while group_alive(pid) && Instant::now() < deadline {
        sleep(Duration::from_millis(25));
    }
    assert!(!group_alive(pid));
}

#[test]
fn benign_stderr_is_the_still_starting_line() {
    assert!(is_benign_stderr("Still starting after 10s"));
    assert!(is_benign_stderr("Still starting after 3s (loading)"));
    assert!(!is_benign_stderr("Still starting after xs"));
    assert!(!is_benign_stderr("Still starting after 10snot"));
    assert!(!is_benign_stderr("something else"));
}

fn chunk(id: &str, index: u64, count: u64, byte_length: u64, data: &str) -> String {
    format!(
        r#"{{"type":"rpc_chunk","chunkId":"{id}","index":{index},"count":{count},"byteLength":{byte_length},"data":"{data}"}}"#
    )
}

fn push(decoder: &mut OmpDecoder, line: &str) -> Decoded {
    decoder.push(line)
}

fn decoded_error(decoded: Decoded) -> String {
    match decoded {
        Decoded::Error { reason } => reason,
        _ => panic!("expected an error"),
    }
}

// Standard base64 of the ASCII text "{\"type\":\"big\",\"x\":\"" + 'a' repeated, split on 3-byte groups.
fn b64(input: &[u8]) -> String {
    const A: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for group in input.chunks(3) {
        let mut buf = [0u8; 3];
        buf[..group.len()].copy_from_slice(group);
        let n = (u32::from(buf[0]) << 16) | (u32::from(buf[1]) << 8) | u32::from(buf[2]);
        out.push(A[(n >> 18) as usize & 63] as char);
        out.push(A[(n >> 12) as usize & 63] as char);
        out.push(if group.len() > 1 {
            A[(n >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if group.len() > 2 {
            A[n as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

#[test]
fn chunks_reassemble_into_one_frame_after_negotiation() {
    let frame = r#"{"type":"big","x":"hello world"}"#;
    let bytes = frame.as_bytes();
    let mut decoder = OmpDecoder::new();
    decoder.negotiated(Some(10.0));
    let (left, right) = bytes.split_at(bytes.len() / 2);
    assert!(matches!(
        push(
            &mut decoder,
            &chunk("rpc-1", 0, 2, bytes.len() as u64, &b64(left))
        ),
        Decoded::Partial
    ));
    match push(
        &mut decoder,
        &chunk("rpc-1", 1, 2, bytes.len() as u64, &b64(right)),
    ) {
        Decoded::Frame(f) => {
            assert_eq!(f.kind, "big");
            assert_eq!(f.text, frame);
        }
        _ => panic!("frame"),
    }
}

#[test]
fn a_chunk_before_negotiation_or_a_malformed_sequence_is_a_stream_error() {
    let mut decoder = OmpDecoder::new();
    let line = chunk("rpc-1", 0, 2, 20, "QUJD");
    assert_eq!(
        decoded_error(push(&mut decoder, &line)),
        "rpc chunk before protocol negotiation"
    );

    let mut decoder = OmpDecoder::new();
    decoder.negotiated(Some(10.0));
    assert_eq!(
        decoded_error(push(&mut decoder, &chunk("", 0, 2, 20, "QUJD"))),
        "invalid rpc chunk metadata"
    );
    assert_eq!(
        decoded_error(push(&mut decoder, &chunk("r", 0, 1, 20, "QUJD"))),
        "invalid rpc chunk metadata"
    );
    assert_eq!(
        decoded_error(push(&mut decoder, &chunk("r", 0, 2, 5, "QUJD"))),
        "invalid rpc chunk metadata"
    );
    assert_eq!(
        decoded_error(push(&mut decoder, &chunk("r", 1, 2, 20, "QUJD"))),
        "rpc chunk sequence must start at index 0"
    );
    assert_eq!(
        decoded_error(push(&mut decoder, &chunk("r", 0, 2, 20, "not base64!"))),
        "invalid rpc chunk data"
    );
}

#[test]
fn another_frame_inside_a_sequence_and_a_short_sequence_are_errors() {
    let mut decoder = OmpDecoder::new();
    decoder.negotiated(Some(10.0));
    let frame = r#"{"type":"big","pad":"0123456789"}"#;
    let bytes = frame.as_bytes();
    let (left, _) = bytes.split_at(bytes.len() / 2);
    assert!(matches!(
        push(
            &mut decoder,
            &chunk("r", 0, 2, bytes.len() as u64, &b64(left))
        ),
        Decoded::Partial
    ));
    assert_eq!(
        decoded_error(push(&mut decoder, r#"{"type":"notice"}"#)),
        "rpc chunk sequence interrupted"
    );

    let mut decoder = OmpDecoder::new();
    decoder.negotiated(Some(10.0));
    assert!(matches!(
        push(&mut decoder, &chunk("r", 0, 2, 30, &b64(b"abcde"))),
        Decoded::Partial
    ));
    assert_eq!(
        decoded_error(push(&mut decoder, &chunk("r", 1, 2, 30, &b64(b"fghij")))),
        "rpc chunk sequence length mismatch"
    );
}

#[test]
fn a_stray_line_inside_a_sequence_is_malformed_not_fatal() {
    let mut decoder = OmpDecoder::new();
    decoder.negotiated(Some(10.0));
    assert!(matches!(
        push(&mut decoder, "not json"),
        Decoded::Malformed { .. }
    ));
    assert!(matches!(push(&mut decoder, ""), Decoded::Partial));
}

#[test]
fn parse_frame_rejects_an_rpc_chunk_shape_only_through_the_decoder() {
    // A chunk is an ordinary frame to parse_frame; only the decoder gives it meaning.
    assert_eq!(
        parse_frame(r#"{"type":"rpc_chunk"}"#).expect("frame").kind,
        "rpc_chunk"
    );
    let _: RpcError = RpcError {
        kind: RpcErrorKind::Closed,
        message: String::new(),
        command: None,
    };
}

use huddle_testkit::fake_harness::{FakeHarness, Mode, RpcEnd};

fn fake_conn(fake: &FakeHarness, cwd: &Path) -> RpcConn<OmpDecoder> {
    let stream = spawn_streaming(
        Spec {
            program: fake.path().to_path_buf(),
            args: vec!["--mode".into(), "rpc".into(), "--no-ui".into()],
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
            output_cap: 1_048_576,
        },
    )
    .expect("spawn");
    RpcConn::new(stream, OmpDecoder::new(), "omp", shared_counter())
}

#[test]
fn the_handshake_runs_against_the_testkit_fake_omp() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "omp",
        Mode::OmpRpc {
            session: "S1".into(),
            turns: vec![],
            end: RpcEnd::Normal,
        },
        0,
        "",
    )
    .expect("fake");
    let mut conn = fake_conn(&fake, home.root());
    let expected = expected_tools(&find("omp").expect("omp"), Access::Write);
    let done = handshake(&mut conn, &expected, Duration::from_secs(10)).expect("handshake");
    assert_eq!(done.session_id.as_deref(), Some("S1"));
    conn.end_input();
    assert_eq!(conn.stream().wait().code, Some(0));

    let calls = fake.calls().expect("calls");
    let stdin = &calls[0].stdin;
    assert_eq!(
        stdin[0],
        r#"{"id":"c1","type":"negotiate_protocol","protocolVersion":2}"#
    );
    assert_eq!(
        stdin[1],
        format!(r#"{{"id":"c2",{}"#, &event_filter_json()[1..])
    );
    assert_eq!(stdin[2], r#"{"id":"c3","type":"get_state"}"#);
    assert_eq!(stdin.len(), 3, "no set_host_tools and nothing else");
}

#[test]
fn a_fake_omp_that_dies_after_ready_is_an_exit_not_a_protocol_failure() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "omp",
        Mode::OmpRpc {
            session: "S1".into(),
            turns: vec![],
            end: RpcEnd::DieAfterReady,
        },
        0,
        "boom\n",
    )
    .expect("fake");
    let mut conn = fake_conn(&fake, home.root());
    let err = handshake(&mut conn, &[], Duration::from_secs(10)).expect_err("exit");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(err.message, "omp ended (exit code 1): boom");
}
