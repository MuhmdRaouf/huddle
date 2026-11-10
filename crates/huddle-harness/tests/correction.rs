use huddle_harness::correction::*;
use huddle_harness::rpc_common::{RpcError, RpcErrorKind};
use serde_json::json;

const GOOD: &str = r#"{"summary":"did it","files":[{"path":"a.rs","why":"fix"}],"open_items":[]}"#;

struct FakeDriver {
    answers: Vec<&'static str>,
    prompts: Vec<String>,
    failed_after: Option<usize>,
    last: String,
}

impl TurnDriver for FakeDriver {
    fn prompt(&mut self, text: &str) -> Result<TurnSpan, RpcError> {
        self.prompts.push(text.to_string());
        if self.answers.is_empty() {
            return Err(RpcError {
                kind: RpcErrorKind::Exited,
                message: "died".into(),
                command: None,
            });
        }
        self.last = self.answers.remove(0).to_string();
        Ok(TurnSpan {
            sent_at_ms: 100,
            ended_at_ms: 200 + self.prompts.len() as u64,
        })
    }
    fn final_text(&self) -> String {
        self.last.clone()
    }
    fn failed(&self) -> bool {
        self.failed_after.is_some_and(|n| self.prompts.len() >= n)
    }
}

fn driver(answers: Vec<&'static str>) -> FakeDriver {
    FakeDriver {
        answers,
        prompts: vec![],
        failed_after: None,
        last: String::new(),
    }
}

#[test]
fn a_valid_report_is_accepted_on_the_first_turn() {
    let mut d = driver(vec![GOOD]);
    let turns = run_report_turns(&mut d, "task").expect("turns");
    assert_eq!(d.prompts, vec!["task"]);
    assert_eq!(
        turns.value,
        Some(serde_json::from_str::<serde_json::Value>(GOOD).expect("json"))
    );
    assert_eq!(turns.started_at_ms, 100);
    assert_eq!(turns.ended_at_ms, 201);
}

#[test]
fn an_invalid_report_draws_up_to_two_corrections_with_the_exact_prompt() {
    let mut d = driver(vec![r#"{"summary":1}"#, r#"{"summary":"x"}"#, GOOD]);
    let turns = run_report_turns(&mut d, "task").expect("turns");
    assert_eq!(d.prompts.len(), 3);
    assert_eq!(
        d.prompts[1],
        "Your report was invalid: summary: Invalid input: expected string, received number; files: Invalid input: expected array, received undefined; open_items: Invalid input: expected array, received undefined. Reply with only the corrected JSON object."
    );
    assert_eq!(
        turns.value,
        Some(serde_json::from_str::<serde_json::Value>(GOOD).expect("json"))
    );
}

#[test]
fn after_two_corrections_the_last_value_stands() {
    let mut d = driver(vec![r#"{"a":1}"#, r#"{"b":2}"#, r#"{"c":3}"#]);
    let turns = run_report_turns(&mut d, "task").expect("turns");
    assert_eq!(d.prompts.len(), 3);
    assert_eq!(turns.value, Some(json!({"c":3})));
}

#[test]
fn a_failed_turn_gets_no_correction() {
    let mut d = driver(vec![r#"{"summary":1}"#]);
    d.failed_after = Some(1);
    let turns = run_report_turns(&mut d, "task").expect("turns");
    assert_eq!(d.prompts.len(), 1);
    assert!(turns.value.is_some());
}

#[test]
fn a_crash_on_a_correction_turn_is_the_drivers_error() {
    let mut d = driver(vec![r#"{"a":1}"#]);
    let err = run_report_turns(&mut d, "task").expect_err("crash");
    assert_eq!(err.kind, RpcErrorKind::Exited);
    assert_eq!(d.prompts.len(), 2);
}

#[test]
fn extraction_reads_the_whole_text_then_a_fence_then_the_outer_braces() {
    assert_eq!(extract_report(r#"{"a":1}"#), Some(json!({"a":1})));
    assert_eq!(
        extract_report("Here:\n```json\n{\"a\":2}\n```\nbye"),
        Some(json!({"a":2}))
    );
    assert_eq!(
        extract_report("Report follows: {\"a\":3} ok"),
        Some(json!({"a":3}))
    );
    assert_eq!(extract_report("no braces at all"), None);
    assert_eq!(extract_report("[1,2]"), None);
    assert_eq!(extract_report(""), None);
}

#[test]
fn problems_name_every_field_in_declaration_order() {
    assert_eq!(problems(None), vec!["no structured report"]);
    assert_eq!(problems(Some(&json!(null))), vec!["no structured report"]);
    assert_eq!(
        problems(Some(&json!("x"))),
        vec!["(root): Invalid input: expected object, received string"]
    );

    let good = json!({"summary":"s","files":[{"path":"p","why":"w"}],"open_items":["o"]});
    assert_eq!(problems(Some(&good)), Vec::<String>::new());

    let bad_file = json!({"summary":"s","files":[{"path":"p","why":1}],"open_items":[]});
    assert_eq!(
        problems(Some(&bad_file)),
        vec!["files.0.why: Invalid input: expected string, received number"]
    );

    let not_object = json!({"summary":"s","files":[true],"open_items":[]});
    assert_eq!(
        problems(Some(&not_object)),
        vec!["files.0: Invalid input: expected object, received boolean"]
    );

    let extra = json!({"summary":"s","files":[],"open_items":[],"zeta":1,"alpha":2});
    assert_eq!(
        problems(Some(&extra)),
        vec![r#"(root): Unrecognized keys: "zeta", "alpha""#]
    );

    let bad_item = json!({"summary":"s","files":[],"open_items":[3]});
    assert_eq!(
        problems(Some(&bad_item)),
        vec!["open_items.0: Invalid input: expected string, received number"]
    );

    let extra_in_file =
        json!({"summary":"s","files":[{"path":"p","why":"w","z":1,"a":2}],"open_items":[]});
    assert_eq!(
        problems(Some(&extra_in_file)),
        vec![r#"files.0: Unrecognized keys: "z", "a""#]
    );
}

#[test]
fn the_correction_prompt_is_exact() {
    assert_eq!(
        correction_prompt(&["a: b".to_string(), "c".to_string()]),
        "Your report was invalid: a: b; c. Reply with only the corrected JSON object."
    );
    assert_eq!(MAX_CORRECTIONS, 2);
}
