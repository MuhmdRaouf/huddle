use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_setup::agent_files::{remove_managed, sync};
use huddle_testkit::temp_home::TempHome;
use std::fs;

fn paths_for(home: &TempHome) -> Paths {
    let root = home.root().to_string_lossy().into_owned();
    Paths::from_env(&Env::with(&[("HOME", root.as_str())])).expect("paths")
}

#[test]
fn sync_writes_one_file_per_family_and_second_run_changes_nothing() {
    let home = TempHome::new();
    let paths = paths_for(&home);

    sync(&paths).expect("sync");

    let dir = paths.claude_dir.join("agents");
    for name in [
        "fable-5.1.md",
        "opus-5.5.md",
        "sonnet-5.5.md",
        "haiku-5.5.md",
    ] {
        assert!(dir.join(name).exists(), "{name}");
    }
    let opus = fs::read_to_string(dir.join("opus-5.5.md")).expect("read");
    assert_eq!(
        opus,
        "---\nname: opus-5.5\ndescription: Claude Opus 5.5\nmodel: claude-opus-5-5\nmanaged-by: huddle\n---\n\nDo the task in your prompt and report what you did.\n"
    );
    sync(&paths).expect("second sync");
    assert_eq!(
        fs::read_to_string(dir.join("opus-5.5.md")).expect("read"),
        opus
    );
}

#[test]
fn a_users_file_with_a_wanted_name_stays_untouched() {
    let home = TempHome::new();
    let paths = paths_for(&home);
    let dir = paths.claude_dir.join("agents");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::write(dir.join("opus-5.5.md"), "my own agent\n").expect("write");

    sync(&paths).expect("sync");

    assert_eq!(
        fs::read_to_string(dir.join("opus-5.5.md")).expect("read"),
        "my own agent\n"
    );
    assert!(dir.join("sonnet-5.5.md").exists());
}

#[test]
fn an_empty_file_is_written_again_and_a_managed_stale_file_is_removed() {
    let home = TempHome::new();
    let paths = paths_for(&home);
    let dir = paths.claude_dir.join("agents");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::write(dir.join("sonnet-5.5.md"), "").expect("write");
    fs::write(dir.join("opus-4.0.md"), "---\nmanaged-by: huddle\n---\n").expect("write");
    fs::write(dir.join("mine.md"), "no marker\n").expect("write");

    sync(&paths).expect("sync");

    assert!(
        fs::read_to_string(dir.join("sonnet-5.5.md"))
            .expect("read")
            .contains("managed-by: huddle")
    );
    assert!(
        !dir.join("opus-4.0.md").exists(),
        "the managed file no family wants is gone"
    );
    assert!(dir.join("mine.md").exists());
}

#[test]
fn remove_managed_leaves_user_files() {
    let home = TempHome::new();
    let paths = paths_for(&home);
    sync(&paths).expect("sync");
    let dir = paths.claude_dir.join("agents");
    fs::write(dir.join("mine.md"), "no marker\n").expect("write");

    remove_managed(&paths).expect("remove");

    assert!(!dir.join("opus-5.5.md").exists());
    assert!(dir.join("mine.md").exists());
}

#[test]
fn remove_managed_on_a_missing_folder_is_ok() {
    let home = TempHome::new();
    let paths = paths_for(&home);

    remove_managed(&paths).expect("remove");

    assert!(!paths.claude_dir.join("agents").exists());
}
