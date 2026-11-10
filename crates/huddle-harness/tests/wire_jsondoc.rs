use huddle_harness::table::{ExitInfo, OneShotReader};
use huddle_harness::wire_jsondoc::{JsonDocReader, OUTPUT_BUDGET};

const SUCCESS: &str = r#"{
  "sessionId": "sess_live_fixture",
  "traceId": "trace_fixture",
  "turnId": "turn_fixture",
  "response": "{\"summary\":\"GLM smoke ready\",\"findings\":[],\"open_items\":[]}",
  "usage": {"source":"provider","modelRequestCount":4,"inputTokens":47339,"outputTokens":735,"totalTokens":48074,"cacheReadTokens":37376,"cacheWriteTokens":0},
  "eventCount": 494,
  "projection": {"status":"idle","turnCount":1,"totalTokenCount":48074,"contextUsed":12378,"contextWindow":200000}
}"#;

fn feed(lines: &[&str], exit: &ExitInfo) -> huddle_harness::table::WireOutput {
    let mut reader: Box<dyn OneShotReader> = Box::new(JsonDocReader::new());
    let mut events = Vec::new();
    for line in lines {
        reader.feed_line(line, &mut events);
    }
    assert!(events.is_empty());
    reader.finish(exit)
}

fn lines_of(text: &str) -> Vec<&str> {
    text.split('\n').collect()
}

#[test]
fn it_reads_the_recorded_document() {
    let out = feed(&lines_of(SUCCESS), &ExitInfo::exited(0));
    assert_eq!(
        out.text,
        "{\"summary\":\"GLM smoke ready\",\"findings\":[],\"open_items\":[]}"
    );
    assert_eq!(out.session_id.as_deref(), Some("sess_live_fixture"));
    assert_eq!(out.failure, None);
    assert_eq!(out.stats.turns, 1);
    assert_eq!(out.stats.input_tokens, 47339);
    assert_eq!(out.stats.output_tokens, 735);
    assert_eq!(out.stats.cache_read_tokens, 37376);
}

#[test]
fn it_skips_warning_lines_before_the_document() {
    let mut lines = vec!["SDK warning {not JSON}", "another warning"];
    lines.extend(lines_of(SUCCESS));
    let out = feed(&lines, &ExitInfo::exited(0));
    assert_eq!(out.session_id.as_deref(), Some("sess_live_fixture"));
    assert_eq!(out.failure, None);
}

#[test]
fn a_warning_line_after_the_document_does_not_lose_it() {
    let mut lines = lines_of(SUCCESS);
    lines.push("Warning: leftover telemetry queue");
    let out = feed(&lines, &ExitInfo::exited(0));
    assert_eq!(out.session_id.as_deref(), Some("sess_live_fixture"));
    assert_eq!(out.failure, None);
}

#[test]
fn a_stray_object_line_does_not_shadow_the_document() {
    let mut lines = vec![r#"{"note":"hello"}"#];
    lines.extend(lines_of(SUCCESS));
    let out = feed(&lines, &ExitInfo::exited(0));
    assert_eq!(out.session_id.as_deref(), Some("sess_live_fixture"));
}

#[test]
fn a_nonzero_exit_wins_over_a_plausible_response() {
    let mut exit = ExitInfo::exited(2);
    exit.stderr_tail = "bad thing".into();
    let out = feed(&lines_of(SUCCESS), &exit);
    assert_eq!(out.failure.as_deref(), Some("ZCode exited 2: bad thing"));
    assert_eq!(
        out.text,
        "{\"summary\":\"GLM smoke ready\",\"findings\":[],\"open_items\":[]}"
    );
}

#[test]
fn invalid_output_with_exit_zero_is_a_failure() {
    let out = feed(&["not json at all"], &ExitInfo::exited(0));
    assert_eq!(
        out.failure.as_deref(),
        Some("ZCode returned no valid JSON response and session id")
    );

    let no_session = r#"{"response":"hi"}"#;
    let out = feed(&[no_session], &ExitInfo::exited(0));
    assert_eq!(
        out.failure.as_deref(),
        Some("ZCode returned no valid JSON response and session id")
    );

    let bad_id = r#"{"sessionId":"other_x","response":"hi"}"#;
    let out = feed(&[bad_id], &ExitInfo::exited(0));
    assert_eq!(
        out.failure.as_deref(),
        Some("ZCode returned no valid JSON response and session id")
    );
}

#[test]
fn a_failed_projection_is_a_failed_turn() {
    let doc = r#"{"sessionId":"sess_1","response":"","projection":{"status":"error"}}"#;
    let out = feed(&[doc], &ExitInfo::exited(0));
    assert_eq!(out.failure.as_deref(), Some("ZCode reported a failed turn"));
    assert_eq!(out.stats.turns, 0);
}

#[test]
fn a_forced_end_reports_no_session_and_says_stopped() {
    let exit = ExitInfo {
        code: None,
        signal: Some("SIGTERM".into()),
        stderr_tail: String::new(),
        forced: Some("stopped".into()),
        session_missing: false,
    };
    let out = feed(&lines_of(SUCCESS), &exit);
    assert_eq!(out.failure.as_deref(), Some("ZCode was stopped (stopped)"));
    assert_eq!(out.session_id, None);
}

#[test]
fn output_past_the_budget_counts_as_nothing() {
    let mut reader = JsonDocReader::new();
    let mut events = Vec::new();
    let block = "x".repeat(1_048_576);
    for _ in 0..(OUTPUT_BUDGET / 1_048_576 + 1) {
        reader.feed_line(&block, &mut events);
    }
    assert!(reader.over_budget());
    let out = Box::new(reader).finish(&ExitInfo::exited(0));
    assert_eq!(
        out.failure.as_deref(),
        Some("ZCode returned no valid JSON response and session id")
    );
}
