mod support;

use huddle_jobs::checks::{Checks, Verdict};
use huddle_jobs::git;
use huddle_jobs::model::Access;
use huddle_jobs::verify::{run_command, run_commands, verify_worktree};
use std::collections::BTreeSet;
use support::{rig, write};

fn env() -> Vec<(String, String)> {
    vec![]
}

fn list(items: &[&str]) -> Vec<String> {
    items.iter().map(|s| s.to_string()).collect()
}

#[test]
fn a_command_reports_its_exit_code_and_tail() {
    let r = rig();
    let ok = run_command("echo hello", &r.repo, &env());
    assert_eq!(ok.exit_code, Some(0));
    assert!(ok.tail.contains("hello"), "{}", ok.tail);
    let bad = run_command("echo oops 1>&2; exit 3", &r.repo, &env());
    assert_eq!(bad.exit_code, Some(3));
    assert!(bad.tail.contains("oops"));
    assert!(bad.failed());
}

#[test]
fn a_failed_setup_stops_everything_but_a_failed_gate_does_not() {
    let r = rig();
    let stopped = Checks {
        setup: list(&["exit 1", "echo never"]),
        gates: list(&["echo gate"]),
        ..Default::default()
    };
    assert_eq!(run_commands(&stopped, &r.repo, &env()).len(), 1);
    let all = Checks {
        gates: list(&["exit 1", "exit 0"]),
        ..Default::default()
    };
    let results = run_commands(&all, &r.repo, &env());
    assert_eq!(results.len(), 2);
    assert!(results[0].failed() && !results[1].failed());
}

#[test]
fn the_verdict_and_changed_paths_come_from_the_harness_not_the_gates() {
    let r = rig();
    let base = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "src/new.rs", "x\n");
    let checks = Checks {
        scope: list(&["src/**"]),
        gates: list(&[": > gate-output.txt"]),
        ..Default::default()
    };
    let v = verify_worktree(&checks, Access::Write, &r.repo, &base, &env()).unwrap();
    assert_eq!(v.verdict, Verdict::Pass);
    assert_eq!(
        v.changed.iter().collect::<BTreeSet<_>>(),
        [&"src/new.rs".to_string()].into_iter().collect()
    );
    assert_eq!(v.gate_touched, list(&["gate-output.txt"]));
}

#[test]
fn a_change_outside_scope_is_a_violation_and_a_failed_gate_is_gate_fail() {
    let r = rig();
    let base = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "docs/x.md", "x\n");
    let scoped = Checks {
        scope: list(&["src/**"]),
        ..Default::default()
    };
    let v = verify_worktree(&scoped, Access::Write, &r.repo, &base, &env()).unwrap();
    assert_eq!(v.verdict, Verdict::ScopeViolation);
    assert_eq!(v.scope.out_of_scope, list(&["docs/x.md"]));

    let gated = Checks {
        gates: list(&["exit 2"]),
        ..Default::default()
    };
    let v = verify_worktree(&gated, Access::Write, &r.repo, &base, &env()).unwrap();
    assert_eq!(v.verdict, Verdict::GateFail);
}

#[test]
fn a_readonly_job_that_changed_a_file_is_a_violation() {
    let r = rig();
    let base = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "x.txt", "x\n");
    let v = verify_worktree(&Checks::default(), Access::Readonly, &r.repo, &base, &env()).unwrap();
    assert!(v.scope.repo_changed);
    assert_eq!(v.verdict, Verdict::ScopeViolation);
}
