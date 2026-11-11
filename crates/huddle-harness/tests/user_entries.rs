use huddle_harness::entries::builtin;
use huddle_harness::table::{Access, ReportKind, SessionOwner, Transport, Wire};
use huddle_harness::user_entries::{UserEntriesError, all_entries, load};
use huddle_testkit::temp_home::TempHome;
use std::fs;

const AIDER: &str = "[[harnesses]]\nid = \"aider\"\nlabel = \"Aider\"\ncommand = \"aider\"\nargs = [\"--yes\"]\nprompt_flag = \"--message\"\n";

fn path_in(home: &TempHome, text: &str) -> std::path::PathBuf {
    let path = home.root().join("harnesses.toml");
    fs::write(&path, text).expect("write");
    path
}

#[test]
fn a_missing_file_is_no_entries() {
    let home = TempHome::new();
    assert_eq!(load(&home.root().join("harnesses.toml")), Ok(vec![]));
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
    let text =
        "[[harnesses]]\nid = \"mytool\"\ncommand = \"/opt/bin/mytool\"\nprompt = \"stdin\"\n";
    let loaded = load(&path_in(&home, text)).expect("load");
    assert_eq!(loaded[0].label, "mytool");
    assert_eq!(loaded[0].run.prompt.transport, Transport::Stdin);
    assert_eq!(loaded[0].run.prompt.flag, None);
    assert_eq!(loaded[0].run.args, Vec::<String>::new());
}

#[test]
fn bad_files_and_ids_are_values() {
    let home = TempHome::new();
    let err = load(&path_in(&home, "[[harnesses]")).expect_err("parse");
    assert!(matches!(err, UserEntriesError::Parse { .. }));

    let err = load(&path_in(
        &home,
        "[[harnesses]]\nid = \"Bad_Id\"\ncommand = \"x\"\n",
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
        "[[harnesses]]\nid = \"omp\"\ncommand = \"x\"\n",
    ))
    .expect_err("clash");
    assert_eq!(
        err,
        UserEntriesError::ClashesWithBuiltin { id: "omp".into() }
    );

    let err = load(&path_in(
        &home,
        "[[harnesses]]\nid = \"a1\"\ncommand = \"x\"\n\n[[harnesses]]\nid = \"a1\"\ncommand = \"y\"\n",
    ))
    .expect_err("dup");
    assert_eq!(err, UserEntriesError::Duplicate { id: "a1".into() });

    let err = load(&path_in(
        &home,
        "[[harnesses]]\nid = \"a1\"\ncommand = \"\"\n",
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

#[test]
fn t2_user_entries_load_from_harnesses_toml() {
    let home = TempHome::new();
    let path = home.root().join("harnesses.toml");
    fs::write(
        &path,
        "[[harnesses]]\nid = \"aider\"\nlabel = \"Aider\"\ncommand = \"aider\"\nargs = [\"--yes\"]\nprompt_flag = \"--message\"\n",
    )
    .expect("write");
    let loaded = load(&path).expect("load");
    assert_eq!(loaded.len(), 1);
    assert_eq!(loaded[0].id, "aider");
    assert_eq!(loaded[0].run.args, vec!["--yes"]);
    assert_eq!(loaded[0].run.prompt.flag.as_deref(), Some("--message"));
}

#[test]
fn t2_a_broken_harnesses_toml_is_a_parse_error_naming_the_file() {
    let home = TempHome::new();
    let path = home.root().join("harnesses.toml");
    fs::write(&path, "[[harnesses]\n").expect("write");
    let text = load(&path).unwrap_err().to_string();
    assert!(
        text.starts_with(&format!("{} is not valid: ", path.display())),
        "{text}"
    );
}
