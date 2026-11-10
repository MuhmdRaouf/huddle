use huddle_config::providers_file::{
    ProvidersFileError, providers_path, read_enabled, set_enabled,
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
        r#"{"providers":["qwen","zai","madeup","kimi"]}"#,
    );
    assert_eq!(
        read_enabled(home.root(), &KNOWN).expect("read"),
        vec!["zai", "kimi", "qwen"]
    );
}

#[test]
fn a_malformed_file_is_an_error_naming_the_file() {
    let home = TempHome::new();
    write_file(home.root(), "not json");
    let err = read_enabled(home.root(), &KNOWN).unwrap_err();
    let text = err.to_string();
    let path = providers_path(home.root());
    assert!(
        text.starts_with(&format!("{}: not valid providers.json (", path.display())),
        "{text}"
    );
    assert!(matches!(err, ProvidersFileError::Invalid { .. }));
}

#[test]
fn a_wrong_shaped_file_is_an_error() {
    let home = TempHome::new();
    write_file(home.root(), r#"{"providers":"zai"}"#);
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
        r#"{"providers":["zai"],"lanes":{"zai":{"model":"glm-5.3"}},"note":"keep me"}"#,
    );
    set_enabled(home.root(), "kimi", true).expect("on");
    let value: serde_json::Value =
        serde_json::from_slice(&fs::read(providers_path(home.root())).expect("read"))
            .expect("json");
    assert_eq!(value["note"], "keep me");
    assert_eq!(value["lanes"]["zai"]["model"], "glm-5.3");
    assert_eq!(value["providers"], serde_json::json!(["zai", "kimi"]));
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
        .open(home.root().join("providers.json.lock"))
        .expect("lock file");
    lock.lock().expect("take lock");
    let err = set_enabled(home.root(), "zai", true).unwrap_err();
    assert!(matches!(err, ProvidersFileError::Locked { .. }), "{err:?}");
}
