//! The attempt logs' own tests: cuts, the prompt reference, and the two files on disk.

use huddle_harness::rpc_common::*;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;

fn frame(text: &str) -> Frame {
    parse_frame(text).expect("frame")
}

#[test]
fn a_frame_that_fits_is_logged_byte_for_byte() {
    let text = r#"{"type":"message_update","messageId":"msg-2"}"#;
    assert_eq!(log_text(&frame(text)), text);
    let small = r#"{"type":"tool_execution_end","result":{"content":[],"details":{"a":1}},"isError":false}"#;
    assert_eq!(log_text(&frame(small)), small);
}

#[test]
fn an_oversized_tool_result_is_cut_to_head_and_tail() {
    let details = "x".repeat(LOG_FIELD_LIMIT + 8);
    let text = format!(
        r#"{{"type":"tool_execution_end","result":{{"content":[],"details":"{details}"}},"isError":false}}"#
    );
    let cut: serde_json::Value = serde_json::from_str(&log_text(&frame(&text))).expect("json");
    let details = &cut["result"]["details"];
    assert_eq!(
        details["$truncated"],
        serde_json::json!(LOG_FIELD_LIMIT + 10)
    );
    assert_eq!(details["head"].as_str().expect("head").len(), LOG_KEEP);
    assert_eq!(details["tail"].as_str().expect("tail").len(), LOG_KEEP);
    assert_eq!(cut["isError"], serde_json::json!(false));
}

#[test]
fn a_tool_result_message_is_cut_too_and_other_messages_are_not() {
    let body = "y".repeat(LOG_FIELD_LIMIT + 1);
    let text = format!(
        r#"{{"type":"message_end","message":{{"role":"toolResult","content":"{body}","details":{{}}}},"messageId":"m"}}"#
    );
    let cut: serde_json::Value = serde_json::from_str(&log_text(&frame(&text))).expect("json");
    assert!(cut["message"]["content"]["$truncated"].is_number());

    let assistant =
        format!(r#"{{"type":"message_end","message":{{"role":"assistant","content":"{body}"}}}}"#);
    assert_eq!(log_text(&frame(&assistant)), assistant);
}

#[test]
fn utf8_boundaries_are_respected_when_cutting() {
    let filler = "é".repeat(LOG_FIELD_LIMIT);
    let text = format!(r#"{{"type":"tool_execution_end","result":{{"details":"{filler}"}}}}"#);
    let line = log_text(&frame(&text));
    assert!(std::str::from_utf8(line.as_bytes()).is_ok());
}

#[test]
fn a_prompt_is_logged_as_a_reference_and_other_commands_as_sent() {
    let out = outbound_log_text(r#"{"id":"c4","type":"prompt","message":"secret brief"}"#);
    let value: serde_json::Value = serde_json::from_str(&out).expect("json");
    assert_eq!(
        value["message"],
        serde_json::json!({"$ref": "job.json#prompt"})
    );
    assert_eq!(value["id"], serde_json::json!("c4"));
    assert_eq!(
        outbound_log_text(r#"{"id":"c5","type":"abort"}"#),
        r#"{"id":"c5","type":"abort"}"#
    );
}

#[test]
fn logs_open_private_and_stop_on_the_first_failure() {
    let home = TempHome::new();
    let dir = home.root().join("job");
    let mut log = RpcLog::open(
        &dir.join("rpc-frames.jsonl"),
        &dir.join("rpc-commands.jsonl"),
    )
    .expect("open");
    log.inbound(&frame(r#"{"type":"a"}"#));
    log.inbound_raw("stray line");
    log.outbound(r#"{"id":"c1","type":"get_state"}"#);
    assert_eq!(log.failure(), None);
    let frames = fs::read_to_string(dir.join("rpc-frames.jsonl")).expect("frames");
    assert_eq!(frames, "{\"type\":\"a\"}\nstray line\n");
    let meta = fs::metadata(dir.join("rpc-commands.jsonl")).expect("meta");
    assert_eq!(meta.permissions().mode() & 0o777, 0o600);
}

#[test]
fn opening_under_a_file_is_an_error_naming_the_path() {
    let home = TempHome::new();
    let blocker = home.root().join("blocker");
    fs::write(&blocker, "x").expect("write");
    let err = RpcLog::open(&blocker.join("f"), &blocker.join("c"))
        .err()
        .expect("error");
    assert!(err.starts_with("cannot open log "));
}
