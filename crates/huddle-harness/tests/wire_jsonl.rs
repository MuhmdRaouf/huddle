//! The opencode JSONL reader: one JSON event per stdout line, folded to events and one report.

use huddle_harness::table::{ExitInfo, OneShotReader, RunEvent, WireOutput};
use huddle_harness::wire_jsonl::JsonlReader;

const STEP_START: &str = r#"{"type":"step_start","timestamp":1791316930000,"sessionID":"ses_00000002","part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_00000002","type":"step-start"}}"#;
const TOOL: &str = r#"{"type":"tool_use","timestamp":1791316930328,"sessionID":"ses_00000002","part":{"type":"tool","tool":"bash","callID":"c1","state":{"status":"completed","input":{"command":"echo hi"},"output":"hi\n","title":"echo hi","time":{"start":1,"end":2}},"id":"p","sessionID":"ses_00000002","messageID":"m"}}"#;
const STEP_FINISH_TOOLS: &str = r#"{"type":"step_finish","timestamp":1791316930450,"sessionID":"ses_00000002","part":{"id":"p","reason":"tool-calls","type":"step-finish","tokens":{"total":328,"input":120,"output":8,"reasoning":0,"cache":{"write":0,"read":200}},"cost":0.001}}"#;
const TEXT: &str = r#"{"type":"text","timestamp":1791316930900,"sessionID":"ses_00000002","part":{"id":"p","type":"text","text":"{\"kind\":\"file\"}","time":{"start":1,"end":2}}}"#;
const STEP_FINISH_STOP: &str = r#"{"type":"step_finish","timestamp":1791316930950,"sessionID":"ses_00000002","part":{"id":"p","reason":"stop","type":"step-finish","tokens":{"total":336,"input":20,"output":16,"reasoning":0,"cache":{"write":4,"read":300}},"cost":0.002}}"#;
const ERROR: &str = r#"{"type":"error","timestamp":1791316927300,"sessionID":"ses_00000002","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_1"}}}"#;

fn run(lines: &[&str], exit: &ExitInfo) -> (WireOutput, Vec<RunEvent>) {
    let mut reader: Box<dyn OneShotReader> = Box::new(JsonlReader::new());
    let mut events = Vec::new();
    for line in lines {
        reader.feed_line(line, &mut events);
    }
    (reader.finish(exit), events)
}

#[test]
fn a_tool_run_tallies_the_steps_and_takes_the_last_text() {
    let (out, events) = run(
        &[STEP_START, TOOL, STEP_FINISH_TOOLS, TEXT, STEP_FINISH_STOP],
        &ExitInfo::exited(0),
    );
    assert_eq!(out.text, "{\"kind\":\"file\"}");
    assert_eq!(out.session_id.as_deref(), Some("ses_00000002"));
    assert_eq!(out.failure, None);
    assert_eq!(out.stats.turns, 2);
    assert_eq!(out.stats.input_tokens, 140);
    assert_eq!(out.stats.output_tokens, 24);
    assert_eq!(out.stats.cache_read_tokens, 500);
    assert_eq!(out.stats.cache_write_tokens, 4);
    assert!((out.stats.cost_usd - 0.003).abs() < 1e-9);
    assert_eq!(out.stats.duration_ms, 950);
    assert_eq!(
        events,
        vec![
            RunEvent::Session("ses_00000002".into()),
            RunEvent::ToolUse {
                name: "bash".into(),
                summary: "echo hi".into()
            },
            RunEvent::ToolResult { is_error: false },
            RunEvent::AssistantText("{\"kind\":\"file\"}".into()),
        ]
    );
}

#[test]
fn a_tool_with_no_title_summarises_with_the_first_input_value_in_source_order() {
    let line = r#"{"type":"tool_use","sessionID":"s","part":{"type":"tool","tool":"read","state":{"status":"running","input":{"zebra":1,"alpha":"file.txt","mid":"later"}}}}"#;
    let (_, events) = run(&[line], &ExitInfo::exited(1));
    assert_eq!(
        events,
        vec![RunEvent::ToolUse {
            name: "read".into(),
            summary: "file.txt".into()
        }]
    );
}

#[test]
fn an_error_event_fails_the_run_even_after_a_finished_step() {
    let (out, _) = run(
        &[STEP_START, ERROR, TEXT, STEP_FINISH_STOP],
        &ExitInfo::exited(0),
    );
    assert_eq!(
        out.failure.as_deref(),
        Some("Unexpected server error. Check server logs for details.")
    );
    assert_eq!(out.text, "{\"kind\":\"file\"}");
}

#[test]
fn no_finished_step_is_a_failure_even_on_exit_zero() {
    let (out, _) = run(&[STEP_START, TEXT], &ExitInfo::exited(0));
    assert_eq!(
        out.failure.as_deref(),
        Some("opencode exited without a finished step (exit 0)")
    );

    let exit = ExitInfo {
        code: None,
        signal: Some("SIGABRT".into()),
        stderr_tail: String::new(),
        forced: None,
        session_missing: false,
    };
    let (out, _) = run(&[STEP_START], &exit);
    assert_eq!(
        out.failure.as_deref(),
        Some("opencode exited without a finished step (SIGABRT)")
    );
}

#[test]
fn a_forced_exit_yields_no_result_and_says_stopped() {
    let exit = ExitInfo {
        code: None,
        signal: Some("SIGTERM".into()),
        stderr_tail: String::new(),
        forced: Some("timeout".into()),
        session_missing: false,
    };
    let (out, _) = run(&[STEP_START, TEXT, STEP_FINISH_STOP], &exit);
    assert_eq!(
        out.failure.as_deref(),
        Some("opencode was stopped (timeout)")
    );
}

#[test]
fn lines_that_are_not_events_are_skipped_and_error_name_is_the_fallback() {
    let named = r#"{"type":"error","error":{"name":"BrokenThing"}}"#;
    let bare = r#"{"type":"error"}"#;
    let (out, _) = run(
        &["garbage", "", "[1,2]", "null", named],
        &ExitInfo::exited(0),
    );
    assert_eq!(out.failure.as_deref(), Some("BrokenThing"));
    let (out, _) = run(&[bare], &ExitInfo::exited(0));
    assert_eq!(out.failure.as_deref(), Some("opencode reported an error"));
}
