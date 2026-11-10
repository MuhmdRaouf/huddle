use huddle_jobs::checks::*;
use huddle_jobs::checks_render::verification_section;

fn base() -> Verification {
    Verification {
        verdict: Verdict::Pass,
        scope: ScopeResult::default(),
        changed: vec![],
        gates: vec![],
        gate_touched: vec![],
    }
}

#[test]
fn a_clean_pass_is_two_lines_and_gates_none() {
    assert_eq!(
        verification_section(&base()),
        "Verdict: pass\nScope: in scope\nGates: none run\n"
    );
}

#[test]
fn a_failed_gate_shows_its_tail_and_the_count() {
    let mut v = base();
    v.verdict = Verdict::GateFail;
    v.gates = vec![
        GateResult {
            run: "cargo test".into(),
            exit_code: Some(0),
            timed_out: false,
            duration_ms: 1,
            tail: "ok".into(),
        },
        GateResult {
            run: "lint".into(),
            exit_code: Some(3),
            timed_out: false,
            duration_ms: 1,
            tail: "bad line\nanother\n".into(),
        },
        GateResult {
            run: "slow".into(),
            exit_code: None,
            timed_out: true,
            duration_ms: 1,
            tail: String::new(),
        },
    ];
    let text = verification_section(&v);
    assert!(
        text.starts_with("Verdict: gate_fail\nScope: in scope\nGates (1/3 passed)\n"),
        "{text}"
    );
    assert!(text.contains("  pass  cargo test  exit 0\n"), "{text}");
    assert!(
        text.contains("  FAIL  lint  exit 3\n      bad line\n      another\n"),
        "{text}"
    );
    assert!(text.contains("  FAIL  slow  timed out\n"), "{text}");
    assert!(!text.contains("      ok"), "a passing gate has no tail");
}

#[test]
fn scope_lines_name_the_paths() {
    let mut v = base();
    v.verdict = Verdict::ScopeViolation;
    v.scope.forbidden = vec!["a".into()];
    v.scope.out_of_scope = vec!["b".into(), "c".into()];
    assert!(verification_section(&v).contains("Scope: 1 forbidden: a; 2 outside scope: b, c\n"));

    let mut v = base();
    v.scope.repo_changed = true;
    v.changed = vec!["x".into()];
    assert!(
        verification_section(&v)
            .contains("Scope: the job must not change anything, but it changed 1 file(s): x\n")
    );
}

#[test]
fn files_the_gates_added_are_named() {
    let mut v = base();
    v.gate_touched = vec!["dist/a.js".into()];
    assert!(verification_section(&v).ends_with("Gates also changed: dist/a.js\n"));
}
