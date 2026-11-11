use huddle_config::providers_file::{
    DEFAULT_PROVIDERS, ProvidersFileError, providers_path, read_enabled, set_enabled,
};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::Path;

const KNOWN: [&str; 5] = ["zai", "kimi", "deepseek", "minimax", "qwen"];

fn write_file(dir: &Path, text: &str) {
    fs::create_dir_all(dir).expect("mkdir");
    fs::write(providers_path(dir), text).expect("write");
}

#[test]
fn a_missing_file_is_no_providers() {
    let home = TempHome::new();
    assert_eq!(
        read_enabled(home.root(), &KNOWN).expect("read"),
        Vec::<String>::new()
    );
}

#[test]
fn names_come_back_in_the_compiled_order_and_unknown_names_drop() {
    let home = TempHome::new();
    write_file(
        home.root(),
        "providers = [\"qwen\", \"zai\", \"madeup\", \"kimi\"]\n",
    );
    assert_eq!(
        read_enabled(home.root(), &KNOWN).expect("read"),
        vec!["zai", "kimi", "qwen"]
    );
}

#[test]
fn a_malformed_file_is_an_error_naming_the_file() {
    let home = TempHome::new();
    write_file(home.root(), "providers = [");
    let err = read_enabled(home.root(), &KNOWN).unwrap_err();
    let text = err.to_string();
    let path = providers_path(home.root());
    assert!(
        text.starts_with(&format!("{}: not valid providers.toml (", path.display())),
        "{text}"
    );
    assert!(matches!(err, ProvidersFileError::Invalid { .. }));
}

#[test]
fn a_wrong_shaped_file_is_an_error() {
    let home = TempHome::new();
    write_file(home.root(), "providers = \"zai\"\n");
    assert!(matches!(
        read_enabled(home.root(), &KNOWN),
        Err(ProvidersFileError::Invalid { .. })
    ));
}

#[test]
fn a_read_error_other_than_missing_names_the_file() {
    let home = TempHome::new();
    fs::create_dir_all(providers_path(home.root())).expect("mkdir as a directory");
    let err = read_enabled(home.root(), &KNOWN).unwrap_err();
    assert!(matches!(err, ProvidersFileError::Read { .. }), "{err:?}");
}

#[test]
fn enabling_and_disabling_round_trip() {
    let home = TempHome::new();
    set_enabled(home.root(), "zai", true).expect("on");
    set_enabled(home.root(), "kimi", true).expect("on");
    assert_eq!(
        read_enabled(home.root(), &KNOWN).expect("read"),
        vec!["zai", "kimi"]
    );
    set_enabled(home.root(), "zai", false).expect("off");
    assert_eq!(
        read_enabled(home.root(), &KNOWN).expect("read"),
        vec!["kimi"]
    );
}

#[test]
fn an_enable_keeps_lanes_and_any_other_key_the_user_added() {
    let home = TempHome::new();
    write_file(
        home.root(),
        "providers = [\"zai\"]\nnote = \"keep me\"\n\n[lanes.zai]\nmodel = \"glm-5.3\"\n",
    );
    set_enabled(home.root(), "kimi", true).expect("on");
    let table: toml::Table =
        toml::from_str(&fs::read_to_string(providers_path(home.root())).expect("read"))
            .expect("toml");
    assert_eq!(table["note"].as_str(), Some("keep me"));
    assert_eq!(table["lanes"]["zai"]["model"].as_str(), Some("glm-5.3"));
    let providers: Vec<&str> = table["providers"]
        .as_array()
        .expect("array")
        .iter()
        .filter_map(toml::Value::as_str)
        .collect();
    assert_eq!(providers, ["zai", "kimi"]);
}

#[test]
fn a_write_that_changes_nothing_leaves_the_file_alone() {
    let home = TempHome::new();
    set_enabled(home.root(), "zai", true).expect("on");
    let before = fs::metadata(providers_path(home.root()))
        .expect("meta")
        .modified()
        .expect("mtime");
    std::thread::sleep(std::time::Duration::from_millis(20));
    set_enabled(home.root(), "zai", true).expect("no-op");
    let after = fs::metadata(providers_path(home.root()))
        .expect("meta")
        .modified()
        .expect("mtime");
    assert_eq!(before, after);
}

#[test]
fn concurrent_enables_all_land() {
    let home = TempHome::new();
    let dir = home.root().to_path_buf();
    let threads: Vec<_> = KNOWN
        .iter()
        .map(|name| {
            let dir = dir.clone();
            let name = name.to_string();
            std::thread::spawn(move || set_enabled(&dir, &name, true).expect("on"))
        })
        .collect();
    for t in threads {
        t.join().expect("join");
    }
    assert_eq!(read_enabled(&dir, &KNOWN).expect("read").len(), 5);
}

#[test]
fn a_lock_held_past_the_wait_is_a_typed_error() {
    let home = TempHome::new();
    fs::create_dir_all(home.root()).expect("mkdir");
    let lock = fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(home.root().join("providers.toml.lock"))
        .expect("lock file");
    lock.lock().expect("take lock");
    let err = set_enabled(home.root(), "zai", true).unwrap_err();
    assert!(matches!(err, ProvidersFileError::Locked { .. }), "{err:?}");
}

#[test]
fn t2_providers_are_toml_and_an_enable_keeps_the_users_keys() {
    let home = TempHome::new();
    write_file(
        home.root(),
        "providers = [\"zai\"]\nnote = \"keep me\"\n\n[lanes.zai]\nmodel = \"glm-5.3\"\n",
    );
    set_enabled(home.root(), "kimi", true).expect("on");

    assert!(providers_path(home.root()).ends_with("providers.toml"));
    let table: toml::Table =
        toml::from_str(&fs::read_to_string(providers_path(home.root())).expect("read"))
            .expect("toml");
    assert_eq!(table["note"].as_str(), Some("keep me"));
    assert_eq!(table["lanes"]["zai"]["model"].as_str(), Some("glm-5.3"));
    assert_eq!(
        read_enabled(home.root(), &KNOWN).expect("read"),
        vec!["zai", "kimi"]
    );
}

#[test]
fn t2_a_broken_providers_toml_names_the_file() {
    let home = TempHome::new();
    write_file(home.root(), "providers = [");
    let text = read_enabled(home.root(), &KNOWN).unwrap_err().to_string();
    assert!(
        text.starts_with(&format!(
            "{}: not valid providers.toml (",
            providers_path(home.root()).display()
        )),
        "{text}"
    );
}

#[test]
fn an_enable_edits_the_file_in_place_and_keeps_comments_and_order() {
    let home = TempHome::new();
    let dir = home.root().join("cfg");
    let hand = "\
# my lanes

# zai first, always
providers = [\"zai\"] # keep this

[notes]
why = \"cheap tokens\"
";
    write_file(&dir, hand);

    set_enabled(&dir, "kimi", true).expect("enable");
    let text = fs::read_to_string(providers_path(&dir)).expect("read");
    assert_eq!(text, hand.replace("[\"zai\"]", "[\"zai\", \"kimi\"]"));

    set_enabled(&dir, "kimi", false).expect("disable");
    assert_eq!(
        fs::read_to_string(providers_path(&dir)).expect("read"),
        hand
    );
}

#[test]
fn a_first_enable_starts_from_the_default_file() {
    let home = TempHome::new();
    let dir = home.root().join("cfg");

    set_enabled(&dir, "zai", true).expect("enable");
    assert_eq!(
        fs::read_to_string(providers_path(&dir)).expect("read"),
        DEFAULT_PROVIDERS.replace("providers = []", "providers = [\"zai\"]")
    );
}
