use huddle_harness::table::{
    Access, AccessSpec, Binary, Brief, Caps, Check, Entry, ExitInfo, FixedPath, Flag, Launcher,
    Prompt, ReportKind, Run, RunStats, Session, SessionOwner, SessionRef, Transport, Wire,
    WireOutput,
};
use std::collections::BTreeMap;
use std::time::Duration;

fn session_with(missing: Vec<Vec<String>>) -> Session {
    Session {
        owner: SessionOwner::Engine,
        new: vec![],
        resume: vec!["--resume".into(), "{id}".into()],
        missing,
    }
}

fn opencode_like_entry() -> Entry {
    let mut access = BTreeMap::new();
    access.insert(
        Access::Write,
        AccessSpec {
            args: vec![],
            env: vec![("OPENCODE_CONFIG_CONTENT".into(), "{}".into())],
        },
    );
    access.insert(
        Access::Readonly,
        AccessSpec {
            args: vec!["--mode".into(), "plan".into()],
            env: vec![],
        },
    );
    Entry {
        id: "opencode".into(),
        label: "OpenCode".into(),
        binary: Binary {
            names: vec!["opencode".into()],
            env_var: None,
            fixed: vec![FixedPath {
                os: "macos".into(),
                path: "~/opencode".into(),
            }],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: Check {
            version: vec!["--version".into()],
            install_hint: "Install OpenCode.".into(),
            login_hint: "Log in once.".into(),
            verified: true,
        },
        run: Run {
            args: vec!["run".into(), "--format".into(), "json".into()],
            cwd_flag: None,
            prompt: Prompt {
                transport: Transport::Argv,
                flag: None,
                argv_text: None,
            },
            effort: Some(Flag {
                args: vec!["--variant".into(), "{value}".into()],
                values: vec!["low".into()],
            }),
            access,
            after_access: vec!["--auto".into()],
            session: session_with(vec![vec!["session not found".into()]]),
            line_cap: 1_048_576,
            reap_group: true,
        },
        wire: Wire::Jsonl,
        caps: Caps {
            report: ReportKind::Extracted,
            notes: vec![],
        },
    }
}

#[test]
fn an_entry_is_plain_data_with_the_design_fields() {
    let entry = opencode_like_entry();
    assert_eq!(entry.id, "opencode");
    assert_eq!(entry.run.line_cap, 1_048_576);
    assert_eq!(entry.clone(), entry);
    assert_eq!(
        Wire::OmpRpc {
            handshake: Duration::from_secs(60)
        },
        Wire::OmpRpc {
            handshake: Duration::from_secs(60)
        }
    );
}

#[test]
fn access_words_round_trip_and_nothing_else_parses() {
    for access in [Access::Readonly, Access::Exec, Access::Write] {
        assert_eq!(Access::parse(access.as_str()), Some(access));
    }
    assert_eq!(Access::parse("Write"), None);
    assert_eq!(Access::parse(""), None);
}

#[test]
fn run_lists_accepted_access_in_order() {
    let entry = opencode_like_entry();
    assert_eq!(
        entry.run.accepted_access(),
        vec![Access::Readonly, Access::Write]
    );
}

#[test]
fn a_session_reads_a_missing_session_from_stderr() {
    let omp = session_with(vec![vec!["session \"".into(), "\" not found".into()]]);
    assert!(
        omp.is_missing(
            "Error: Session \"01a1\" not found.\nRun `omp --resume` without an argument"
        )
    );
    assert!(!omp.is_missing("Error: session expired"));
    assert!(!omp.is_missing(""));

    let zcode = session_with(vec![
        vec!["session".into(), "not found".into()],
        vec!["session".into(), "does not exist".into()],
        vec!["unknown session".into()],
    ]);
    assert!(zcode.is_missing("Session not found"));
    assert!(zcode.is_missing("the SESSION sess_1 does not exist"));
    assert!(zcode.is_missing("Unknown Session"));
    assert!(!zcode.is_missing("not found: session"));

    assert!(!session_with(vec![vec![]]).is_missing("anything"));
}

#[test]
fn first_string_value_keeps_the_order_the_json_wrote() {
    use huddle_harness::table::first_string_value;
    let args: serde_json::Value =
        serde_json::from_str(r#"{"zebra":1,"alpha":"file.txt","mid":"later"}"#).expect("parse");
    assert_eq!(
        first_string_value(Some(&args)),
        Some("file.txt".to_string())
    );
    let none: serde_json::Value = serde_json::from_str(r#"{"a":1}"#).expect("parse");
    assert_eq!(first_string_value(Some(&none)), None);
    assert_eq!(
        first_string_value(Some(&serde_json::json!("not an object"))),
        None
    );
    assert_eq!(first_string_value(None), None);
}

#[test]
fn exit_info_and_run_stats_have_plain_defaults() {
    let exit = ExitInfo::exited(3);
    assert_eq!(exit.code, Some(3));
    assert_eq!(exit.signal, None);
    assert_eq!(exit.forced, None);
    assert!(!exit.session_missing);
    assert!(exit.stderr_tail.is_empty());

    let stats = RunStats::default();
    assert_eq!(stats.turns, 0);
    assert_eq!(stats.model, None);

    let output = WireOutput {
        text: "t".into(),
        session_id: None,
        failure: None,
        stats,
    };
    assert_eq!(output.text, "t");

    let brief = Brief {
        prompt: "p".into(),
        access: Access::Write,
        effort: None,
        session: SessionRef::New { key: "k".into() },
    };
    assert_eq!(brief.session, SessionRef::New { key: "k".into() });
    assert_eq!(
        SessionRef::Resume { id: "a".into() },
        SessionRef::Resume { id: "a".into() }
    );
}
