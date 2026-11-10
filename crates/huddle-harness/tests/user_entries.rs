use huddle_harness::entries::builtin;
use huddle_harness::table::{Access, ReportKind, SessionOwner, Transport, Wire};
use huddle_harness::user_entries::{UserEntriesError, all_entries, load};
use huddle_testkit::temp_home::TempHome;
use std::fs;

const AIDER: &str = r#"{"harnesses":[{"id":"aider","label":"Aider","command":"aider","args":["--yes"],"prompt_flag":"--message"}]}"#;

fn path_in(home: &TempHome, text: &str) -> std::path::PathBuf {
    let path = home.root().join("harnesses.json");
    fs::write(&path, text).expect("write");
    path
}

#[test]
fn a_missing_file_is_no_entries() {
    let home = TempHome::new();
    assert_eq!(load(&home.root().join("harnesses.json")), Ok(vec![]));
}

#[test]
fn a_user_entry_is_text_wire_write_only() {
    let home = TempHome::new();
    let loaded = load(&path_in(&home, AIDER)).expect("load");
    assert_eq!(loaded.len(), 1);
    let entry = &loaded[0];
    assert_eq!(entry.id, "aider");
    assert_eq!(entry.label, "Aider");
    assert_eq!(entry.binary.names, vec!["aider"]);
    assert_eq!(entry.run.args, vec!["--yes"]);
    assert_eq!(entry.run.prompt.transport, Transport::Argv);
    assert_eq!(entry.run.prompt.flag.as_deref(), Some("--message"));
    assert_eq!(entry.wire, Wire::Text);
    assert_eq!(entry.caps.report, ReportKind::FreeText);
    assert_eq!(entry.run.session.owner, SessionOwner::None);
    assert!(!entry.check.verified);
    assert_eq!(entry.check.version, vec!["--version"]);
    assert_eq!(entry.run.accepted_access(), vec![Access::Write]);
}

#[test]
fn defaults_fill_label_args_prompt_and_version() {
    let home = TempHome::new();
    let text = r#"{"harnesses":[{"id":"mytool","command":"/opt/bin/mytool","prompt":"stdin"}]}"#;
    let loaded = load(&path_in(&home, text)).expect("load");
    assert_eq!(loaded[0].label, "mytool");
    assert_eq!(loaded[0].run.prompt.transport, Transport::Stdin);
    assert_eq!(loaded[0].run.prompt.flag, None);
    assert_eq!(loaded[0].run.args, Vec::<String>::new());
}

#[test]
fn bad_files_and_ids_are_values() {
    let home = TempHome::new();
    let err = load(&path_in(&home, "not json")).expect_err("parse");
    assert!(matches!(err, UserEntriesError::Parse { .. }));

    let err = load(&path_in(
        &home,
        r#"{"harnesses":[{"id":"Bad_Id","command":"x"}]}"#,
    ))
    .expect_err("id");
    assert_eq!(
        err,
        UserEntriesError::BadId {
            id: "Bad_Id".into()
        }
    );
    assert_eq!(
        err.to_string(),
        "harness id Bad_Id must be lowercase letters, digits and dashes, starting with a letter or digit"
    );

    let err = load(&path_in(
        &home,
        r#"{"harnesses":[{"id":"omp","command":"x"}]}"#,
    ))
    .expect_err("clash");
    assert_eq!(
        err,
        UserEntriesError::ClashesWithBuiltin { id: "omp".into() }
    );

    let err = load(&path_in(
        &home,
        r#"{"harnesses":[{"id":"a1","command":"x"},{"id":"a1","command":"y"}]}"#,
    ))
    .expect_err("dup");
    assert_eq!(err, UserEntriesError::Duplicate { id: "a1".into() });

    let err = load(&path_in(
        &home,
        r#"{"harnesses":[{"id":"a1","command":""}]}"#,
    ))
    .expect_err("empty");
    assert!(matches!(err, UserEntriesError::Parse { .. }));
}

#[test]
fn all_entries_lists_builtins_then_user_entries() {
    let home = TempHome::new();
    let path = path_in(&home, AIDER);
    let all = all_entries(&path).expect("all");
    assert_eq!(all.len(), builtin().len() + 1);
    assert_eq!(all[0].id, builtin()[0].id);
    assert_eq!(all.last().expect("last").id, "aider");
}
