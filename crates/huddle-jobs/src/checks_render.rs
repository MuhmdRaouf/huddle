//! Renders check results into the facts a job reports.

use crate::checks::{GateResult, ScopeResult, Verification};

fn line(out: &mut String, text: &str) {
    out.push_str(text);
    out.push('\n');
}

/// The block a job's result shows, ending with one newline.
pub fn verification_section(v: &Verification) -> String {
    let mut out = String::new();
    line(&mut out, &format!("Verdict: {}", v.verdict.as_str()));
    line(&mut out, &scope_line(&v.scope, &v.changed));
    if v.gates.is_empty() {
        line(&mut out, "Gates: none run");
    } else {
        let passed = v.gates.iter().filter(|g| !g.failed()).count();
        line(
            &mut out,
            &format!("Gates ({passed}/{} passed)", v.gates.len()),
        );
        for gate in &v.gates {
            gate_lines(&mut out, gate);
        }
    }
    if !v.gate_touched.is_empty() {
        line(
            &mut out,
            &format!("Gates also changed: {}", v.gate_touched.join(", ")),
        );
    }
    out
}

/// The scope line names the violation, or `in scope` when the job stayed inside its brief.
fn scope_line(scope: &ScopeResult, changed: &[String]) -> String {
    if scope.repo_changed {
        return format!(
            "Scope: the job must not change anything, but it changed {} file(s): {}",
            changed.len(),
            changed.join(", ")
        );
    }
    let mut parts: Vec<String> = Vec::new();
    if !scope.forbidden.is_empty() {
        parts.push(format!(
            "{} forbidden: {}",
            scope.forbidden.len(),
            scope.forbidden.join(", ")
        ));
    }
    if !scope.out_of_scope.is_empty() {
        parts.push(format!(
            "{} outside scope: {}",
            scope.out_of_scope.len(),
            scope.out_of_scope.join(", ")
        ));
    }
    if parts.is_empty() {
        "Scope: in scope".to_string()
    } else {
        format!("Scope: {}", parts.join("; "))
    }
}

/// One line per gate; a failed gate adds its trimmed tail, each line indented six spaces.
fn gate_lines(out: &mut String, gate: &GateResult) {
    let mark = if gate.failed() { "FAIL" } else { "pass" };
    let status = if gate.timed_out {
        "timed out".to_string()
    } else if let Some(code) = gate.exit_code {
        format!("exit {code}")
    } else {
        "killed".to_string()
    };
    line(out, &format!("  {mark}  {}  {status}", gate.run));
    if gate.failed() {
        for l in gate.tail.trim().lines() {
            line(out, &format!("      {l}"));
        }
    }
}
