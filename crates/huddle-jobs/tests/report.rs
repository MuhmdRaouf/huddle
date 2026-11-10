use huddle_jobs::model::{Access, FileNote, Report};
use huddle_jobs::report::{extract_report, initial_prompt, review_fix_prompt};

fn sample() -> Report {
    Report {
        summary: "did it".into(),
        files: vec![FileNote {
            path: "a.rs".into(),
            why: "fix".into(),
        }],
        open_items: vec!["todo".into()],
    }
}

const JSON: &str =
    r#"{"summary":"did it","files":[{"path":"a.rs","why":"fix"}],"open_items":["todo"]}"#;

#[test]
fn a_bare_report_is_read() {
    assert_eq!(extract_report(JSON), Some(sample()));
}

#[test]
fn a_fenced_report_is_read() {
    let text = format!("Here it is:\n\n```json\n{JSON}\n```\nDone.");
    assert_eq!(extract_report(&text), Some(sample()));
}

#[test]
fn a_fenced_report_parses_when_prose_after_it_holds_a_brace() {
    let text = format!("Here it is:\n\n```json\n{JSON}\n```\nThe brace scan cannot win: }} see.");
    assert_eq!(extract_report(&text), Some(sample()));
}

#[test]
fn a_fenced_report_parses_in_non_ascii_prose() {
    let text = format!("التقرير هنا:\n\n```json\n{JSON}\n```\nانتهى التقرير.");
    assert_eq!(extract_report(&text), Some(sample()));
}

#[test]
fn a_report_between_braces_in_prose_is_read() {
    let text = format!("Summary follows. {JSON} That was the report.");
    assert_eq!(extract_report(&text), Some(sample()));
}

#[test]
fn trailing_data_is_not_a_report() {
    let two =
        r#"{"summary":"a","files":[],"open_items":[]} {"summary":"b","files":[],"open_items":[]}"#;
    assert_eq!(extract_report(two), None);
    let junk = format!("{JSON} junk");
    assert_eq!(extract_report(&junk), None);
}

#[test]
fn a_report_needs_every_key_and_no_others() {
    assert_eq!(extract_report(r#"{"summary":"a","files":[]}"#), None);
    assert_eq!(
        extract_report(r#"{"summary":"a","files":[],"open_items":[],"extra":1}"#),
        None
    );
    assert_eq!(
        extract_report(r#"{"summary":3,"files":[],"open_items":[]}"#),
        None
    );
    assert_eq!(extract_report(r#"[1,2]"#), None);
    assert_eq!(extract_report("plain words"), None);
}

#[test]
fn the_write_prompt_has_the_worktree_commit_secret_and_report_lines() {
    let p = initial_prompt("Fix the bug\n", Access::Write);
    assert!(
        p.starts_with("Fix the bug\n\n## huddle job\n\nYou work in an isolated git worktree"),
        "{p}"
    );
    assert!(p.contains("Do not commit, stage, stash or switch branches."));
    assert!(p.contains("Never print, log or write secrets"));
    assert!(
        p.ends_with("API keys, tokens, passwords, private keys."),
        "{p}"
    );
    assert!(
        !p.contains("Final report"),
        "the runner's full_prompt appends the report contract"
    );
}

#[test]
fn the_readonly_prompt_forbids_edits() {
    let p = initial_prompt("Audit it", Access::Readonly);
    assert!(p.contains("This is a read-only investigation"));
    assert!(p.contains("Do not create, edit or delete any file."));
    assert!(!p.contains("Leave your changes in the working tree"));
}

#[test]
fn the_exec_prompt_says_the_checkout_is_thrown_away() {
    let p = initial_prompt("Run the tests", Access::Exec);
    assert!(p.contains("throwaway checkout"));
}

#[test]
fn a_returned_prompt_fences_the_feedback_past_any_backticks_inside() {
    let p = review_fix_prompt("Also ```code``` here");
    assert!(p.starts_with("The reviewer returned your work with this feedback:\n\n````text\nAlso ```code``` here\n````\n\n"), "{p}");
    assert!(p.ends_with("End your run with the final report described in your first message."));
}
