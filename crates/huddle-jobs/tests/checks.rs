use huddle_jobs::checks::*;
use huddle_jobs::git::ChangeSet;
use huddle_jobs::model::Access;
use huddle_testkit::temp_home::TempHome;
use std::os::unix::fs::PermissionsExt;

fn list(items: &[&str]) -> Vec<String> {
    items.iter().map(|s| s.to_string()).collect()
}

fn gate(code: Option<i32>, timed_out: bool) -> GateResult {
    GateResult {
        run: "t".into(),
        exit_code: code,
        timed_out,
        duration_ms: 1,
        tail: String::new(),
    }
}

#[test]
fn changed_paths_join_every_kind_sorted_once() {
    let c = ChangeSet {
        added: list(&["b"]),
        modified: list(&["a"]),
        deleted: list(&["c"]),
        untracked: list(&["a", "d"]),
    };
    assert_eq!(changed_paths(&c), list(&["a", "b", "c", "d"]));
}

#[test]
fn forbid_is_checked_before_scope() {
    let checks = Checks {
        scope: list(&["src/**"]),
        forbid: list(&["src/secret/**"]),
        ..Default::default()
    };
    let r = check_scope(
        &checks,
        Access::Write,
        &list(&["src/a.rs", "src/secret/k", "docs/x.md"]),
    );
    assert_eq!(r.forbidden, list(&["src/secret/k"]));
    assert_eq!(r.out_of_scope, list(&["docs/x.md"]));
    assert!(!r.repo_changed);
    assert!(r.violated());
}

#[test]
fn write_with_no_scope_allows_everything_not_forbidden() {
    let r = check_scope(&Checks::default(), Access::Write, &list(&["any/where.rs"]));
    assert!(!r.violated());
}

#[test]
fn readonly_and_exec_may_change_nothing() {
    for access in [Access::Readonly, Access::Exec] {
        assert!(check_scope(&Checks::default(), access, &list(&["a"])).repo_changed);
        assert!(!check_scope(&Checks::default(), access, &[]).violated());
    }
}

#[test]
fn a_gate_fails_on_a_timeout_a_signal_or_a_non_zero_exit() {
    assert!(!gate(Some(0), false).failed());
    assert!(gate(Some(1), false).failed());
    assert!(gate(None, false).failed());
    assert!(gate(Some(0), true).failed());
}

#[test]
fn a_violated_scope_beats_a_failed_gate() {
    let violated = ScopeResult {
        repo_changed: true,
        ..Default::default()
    };
    assert_eq!(
        derive_verdict(&violated, &[gate(Some(1), false)]),
        Verdict::ScopeViolation
    );
    assert_eq!(
        derive_verdict(
            &ScopeResult::default(),
            &[gate(Some(0), false), gate(Some(2), false)]
        ),
        Verdict::GateFail
    );
    assert_eq!(derive_verdict(&ScopeResult::default(), &[]), Verdict::Pass);
    assert_eq!(Verdict::GateFail.as_str(), "gate_fail");
}

#[test]
fn sidecars_round_trip_with_private_modes_and_a_missing_file_is_empty() {
    let home = TempHome::new();
    let dir = home.root().join("checks-job");
    std::fs::create_dir_all(&dir).unwrap();
    assert_eq!(read_checks(&dir).unwrap(), Checks::default());
    assert_eq!(read_verification(&dir, 1).unwrap(), None);

    let checks = Checks {
        gates: list(&["cargo test --workspace"]),
        ..Default::default()
    };
    write_checks(&dir, &checks).unwrap();
    assert_eq!(read_checks(&dir).unwrap(), checks);
    let mode = std::fs::metadata(checks_path(&dir))
        .unwrap()
        .permissions()
        .mode()
        & 0o777;
    assert_eq!(mode, 0o644);

    let v = Verification {
        verdict: Verdict::Pass,
        scope: ScopeResult::default(),
        changed: list(&["a"]),
        gates: vec![gate(Some(0), false)],
        gate_touched: vec![],
    };
    write_verification(&dir, 2, &v).unwrap();
    assert_eq!(read_verification(&dir, 2).unwrap(), Some(v));
    std::fs::write(checks_path(&dir), "{").unwrap();
    assert!(read_checks(&dir).is_err());
    std::fs::remove_dir_all(&dir).unwrap();
}
