use huddle_harness::table::{ExitInfo, OneShotReader, RunEvent, WireOutput};
use huddle_harness::wire_stream_json::StreamJsonReader;

const INIT: &str = r#"{"type":"system","subtype":"init","session_id":"s-1","model":"glm-5.3"}"#;
const SAID: &str = r#"{"type":"assistant","message":{"content":[{"type":"text","text":"still "},{"type":"text","text":"here"}]}}"#;
const TOOL_USE: &str = r#"{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"zebra":1,"file_path":"/a"}}]}}"#;
const TOOL_RESULT: &str = r#"{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true}]}}"#;
const RETRY: &str =
    r#"{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"error_status":429}"#;
const RESULT: &str = r#"{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"num_turns":3,"result":"{\"summary\":\"done\"}","session_id":"s-1","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}"#;
const ERROR_RESULT: &str = r#"{"type":"result","subtype":"success","is_error":true,"result":"Credit balance is too low","num_turns":1,"duration_ms":10,"total_cost_usd":0,"usage":{}}"#;

fn run(lines: &[&str], exit: &ExitInfo) -> (WireOutput, Vec<RunEvent>) {
    let mut reader: Box<dyn OneShotReader> = Box::new(StreamJsonReader::new());
    let mut events = Vec::new();
    for line in lines {
        reader.feed_line(line, &mut events);
    }
    (reader.finish(exit), events)
}

#[test]
fn the_result_line_is_the_report_and_init_gives_the_session() {
    let (out, events) = run(
        &[INIT, SAID, TOOL_USE, TOOL_RESULT, RETRY, RESULT],
        &ExitInfo::exited(0),
    );
    assert_eq!(out.text, "{\"summary\":\"done\"}");
    assert_eq!(out.session_id.as_deref(), Some("s-1"));
    assert_eq!(out.failure, None);
    assert_eq!(out.stats.turns, 3);
    assert_eq!(out.stats.duration_ms, 1234);
    assert_eq!(out.stats.input_tokens, 1);
    assert_eq!(out.stats.cache_write_tokens, 4);
    assert_eq!(out.stats.model.as_deref(), Some("glm-5.3"));
    assert!((out.stats.cost_usd - 0.01).abs() < 1e-9);
    assert_eq!(
        events,
        vec![
            RunEvent::Session("s-1".into()),
            RunEvent::AssistantText("still here".into()),
            RunEvent::ToolUse {
                name: "Read".into(),
                summary: "/a".into()
            },
            RunEvent::ToolResult { is_error: true },
            RunEvent::ApiRetry {
                attempt: 1,
                max_attempts: 10,
                status: Some(429)
            },
        ]
    );
}

#[test]
fn an_error_result_is_a_failure_with_claudes_own_text() {
    let (out, _) = run(&[INIT, ERROR_RESULT], &ExitInfo::exited(1));
    assert_eq!(out.failure.as_deref(), Some("Credit balance is too low"));
    assert_eq!(out.text, "Credit balance is too low");
}

#[test]
fn no_result_line_is_a_failure() {
    let (out, _) = run(&[INIT, SAID], &ExitInfo::exited(0));
    assert_eq!(
        out.failure.as_deref(),
        Some("claude ended without a result (exit 0)")
    );
    assert_eq!(out.text, "");
    let (out, _) = run(
        &[],
        &ExitInfo {
            code: None,
            signal: Some("SIGKILL".into()),
            stderr_tail: String::new(),
            forced: None,
            session_missing: false,
        },
    );
    assert_eq!(
        out.failure.as_deref(),
        Some("claude ended without a result (SIGKILL)")
    );
}

#[test]
fn a_forced_run_says_stopped() {
    let exit = ExitInfo {
        code: None,
        signal: Some("SIGTERM".into()),
        stderr_tail: String::new(),
        forced: Some("stopped".into()),
        session_missing: false,
    };
    let (out, _) = run(&[INIT, RESULT], &exit);
    assert_eq!(out.failure.as_deref(), Some("claude was stopped (stopped)"));
}

#[test]
fn stray_lines_and_odd_shapes_are_skipped() {
    let odd = r#"{"type":"result","is_error":"maybe"}"#;
    let (out, events) = run(
        &["", "not json", "[1]", odd, r#"{"type":"mystery"}"#],
        &ExitInfo::exited(0),
    );
    assert_eq!(
        out.failure.as_deref(),
        Some("claude ended without a result (exit 0)")
    );
    assert!(events.is_empty());
}
