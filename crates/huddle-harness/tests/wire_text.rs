use huddle_harness::table::{ExitInfo, OneShotReader};
use huddle_harness::wire_text::TextReader;

fn run(lines: &[&str], exit: &ExitInfo) -> huddle_harness::table::WireOutput {
    let mut reader: Box<dyn OneShotReader> = Box::new(TextReader::new());
    let mut events = Vec::new();
    for line in lines {
        reader.feed_line(line, &mut events);
    }
    assert!(events.is_empty());
    reader.finish(exit)
}

#[test]
fn all_of_stdout_is_the_answer() {
    let out = run(&["line one", "", "line three  "], &ExitInfo::exited(0));
    assert_eq!(out.text, "line one\n\nline three");
    assert_eq!(out.session_id, None);
    assert_eq!(out.failure, None);
}

#[test]
fn a_nonzero_exit_is_a_failure_with_the_tail() {
    let mut exit = ExitInfo::exited(3);
    exit.stderr_tail = "boom\n".into();
    let out = run(&["partial"], &exit);
    assert_eq!(out.text, "partial");
    assert_eq!(out.failure.as_deref(), Some("the harness exited 3: boom"));
}

#[test]
fn a_signal_exit_names_the_signal() {
    let exit = ExitInfo {
        code: None,
        signal: Some("SIGKILL".into()),
        stderr_tail: String::new(),
        forced: None,
        session_missing: false,
    };
    assert_eq!(
        run(&[], &exit).failure.as_deref(),
        Some("the harness exited SIGKILL")
    );
}

#[test]
fn a_forced_end_says_stopped() {
    let exit = ExitInfo {
        code: None,
        signal: Some("SIGTERM".into()),
        stderr_tail: String::new(),
        forced: Some("timeout".into()),
        session_missing: false,
    };
    assert_eq!(
        run(&[], &exit).failure.as_deref(),
        Some("the harness was stopped (timeout)")
    );
}

#[test]
fn silence_with_exit_zero_is_a_failure() {
    assert_eq!(
        run(&[], &ExitInfo::exited(0)).failure.as_deref(),
        Some("the harness printed nothing")
    );
    assert_eq!(
        run(&["  "], &ExitInfo::exited(0)).failure.as_deref(),
        Some("the harness printed nothing")
    );
}
