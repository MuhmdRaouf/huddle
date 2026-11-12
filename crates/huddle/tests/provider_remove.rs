use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    os::unix::fs::PermissionsExt,
    process::{Command, Output, Stdio},
};

fn huddle(home: &TempHome, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_NO_DAEMON", "1")
        .stdin(Stdio::null())
        .output()
        .expect("run")
}

fn write_key(home: &TempHome) -> std::path::PathBuf {
    let dir = home.root().join(".config/huddle/keys");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("chmod");
    let file = dir.join("zai");
    fs::write(&file, "ZAI_API_KEY=abcdefghijklmnop\n").expect("write");
    fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).expect("chmod");
    file
}

const ODD: &str = "{\n  \"keep\" : [1,  2]\n}\n";

fn settings(home: &TempHome) -> std::path::PathBuf {
    home.root().join(".claude/settings.json")
}

#[test]
fn removing_the_provider_restores_settings_byte_for_byte() {
    let home = TempHome::new();
    write_key(&home);
    fs::create_dir_all(settings(&home).parent().expect("parent")).expect("mkdir");
    fs::write(settings(&home), ODD).expect("write");
    // HUDDLE_NO_DAEMON keeps this test off a real daemon: setup reports not ready, but enables first.
    let setup = huddle(&home, &["provider", "zai", "setup"]);
    assert!(
        String::from_utf8_lossy(&setup.stdout).contains("added GLM 5.3"),
        "{setup:?}"
    );
    assert_ne!(fs::read_to_string(settings(&home)).expect("read"), ODD);

    let removed = huddle(&home, &["provider", "zai", "remove"]);
    assert_eq!(removed.status.code(), Some(0), "{removed:?}");
    let stdout = String::from_utf8_lossy(&removed.stdout).into_owned();
    assert!(stdout.starts_with("zai removed.\n"), "{stdout}");
    assert!(
        stdout.contains("took glm-5.3 and glm-5.3-flash out of /model"),
        "{stdout}"
    );
    assert_eq!(fs::read_to_string(settings(&home)).expect("read"), ODD);
    let providers =
        fs::read_to_string(home.root().join(".config/huddle/providers.toml")).expect("providers");
    assert!(!providers.contains("zai"), "{providers}");
    assert!(
        !home.root().join(".config/huddle/keys/zai").exists(),
        "the stored key is gone"
    );
}

#[test]
fn a_settings_file_changed_since_keeps_the_users_changes() {
    let home = TempHome::new();
    write_key(&home);
    fs::create_dir_all(settings(&home).parent().expect("parent")).expect("mkdir");
    fs::write(settings(&home), "{}\n").expect("write");
    huddle(&home, &["provider", "zai", "setup"]);
    let mut doc: serde_json::Value =
        serde_json::from_slice(&fs::read(settings(&home)).expect("read")).expect("json");
    doc["userAdded"] = serde_json::json!(true);
    fs::write(
        settings(&home),
        serde_json::to_vec_pretty(&doc).expect("json"),
    )
    .expect("write");

    let removed = huddle(&home, &["provider", "zai", "remove"]);
    assert_eq!(removed.status.code(), Some(0), "{removed:?}");
    let after: serde_json::Value =
        serde_json::from_slice(&fs::read(settings(&home)).expect("read")).expect("json");
    assert_eq!(after["userAdded"], true);
    assert!(after.get("modelPicker").is_none(), "{after}");
}

#[test]
fn removing_a_provider_never_set_up_is_quiet() {
    let home = TempHome::new();
    let removed = huddle(&home, &["provider", "kimi", "remove"]);
    assert_eq!(removed.status.code(), Some(0), "{removed:?}");
    assert_eq!(String::from_utf8_lossy(&removed.stdout), "kimi removed.\n");
}
