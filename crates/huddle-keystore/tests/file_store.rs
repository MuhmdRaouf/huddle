use huddle_config::{env::Env, paths::Paths};
use huddle_keystore::{
    file::FileStore,
    port::{
        Keystore, KeystoreError, SecretString, assert_valid_key, is_valid_key, platform_default,
        service_name,
    },
};
use huddle_testkit::temp_home::TempHome;
use std::{fs, os::unix::fs::PermissionsExt};

fn put(dir: &std::path::Path, name: &str, text: &str, mode: u32) -> std::path::PathBuf {
    fs::create_dir_all(dir).expect("mkdir");
    fs::set_permissions(dir, fs::Permissions::from_mode(0o700)).expect("chmod dir");
    let path = dir.join(name);
    fs::write(&path, text).expect("write");
    fs::set_permissions(&path, fs::Permissions::from_mode(mode)).expect("chmod");
    path
}

/// The key file at huddle's own location.
fn write_env(home: &TempHome, provider: &str, text: &str, mode: u32) -> std::path::PathBuf {
    put(
        &home.root().join(".config/huddle/keys"),
        provider,
        text,
        mode,
    )
}

/// The key file of the removed provider plugin.
fn write_old_env(home: &TempHome, provider: &str, text: &str) -> std::path::PathBuf {
    let dir = home.root().join(format!(".config/{provider}-plugin-cc"));
    put(&dir, "env", text, 0o600)
}

fn store(home: &TempHome) -> FileStore {
    FileStore::new(
        home.root().join(".config/huddle"),
        home.root().to_path_buf(),
    )
}

fn mode_of(path: &std::path::Path) -> u32 {
    fs::metadata(path).expect("stat").permissions().mode() & 0o777
}

#[test]
fn a_missing_file_or_line_reads_as_no_key() {
    let home = TempHome::new();
    assert!(store(&home).get("zai").expect("get").is_none());
    write_env(&home, "zai", "OTHER=1\n", 0o600);
    assert!(store(&home).get("zai").expect("get").is_none());
}

#[test]
fn a_key_line_reads_with_export_and_quotes() {
    let home = TempHome::new();
    write_env(
        &home,
        "zai",
        "export ZAI_API_KEY=\"abcdefghijklmnop\"\n",
        0o600,
    );
    assert_eq!(
        store(&home).get("zai").expect("get").expect("key").expose(),
        "abcdefghijklmnop"
    );
}

#[test]
fn qwen_also_reads_dashscope() {
    let home = TempHome::new();
    write_env(&home, "qwen", "DASHSCOPE_API_KEY=abcdefghijklmnop\n", 0o600);
    assert_eq!(
        store(&home)
            .get("qwen")
            .expect("get")
            .expect("key")
            .expose(),
        "abcdefghijklmnop"
    );
}

#[test]
fn a_loose_key_file_is_refused_with_its_mode() {
    let home = TempHome::new();
    write_env(&home, "zai", "ZAI_API_KEY=abcdefghijklmnop\n", 0o644);
    match store(&home).get("zai") {
        Err(KeystoreError::InsecureFile { mode, .. }) => assert_eq!(mode & 0o777, 0o644),
        other => panic!("want InsecureFile, got {:?}", other.map(|k| k.is_some())),
    }
}

#[test]
fn a_set_writes_a_private_line_in_a_private_directory() {
    let home = TempHome::new();
    let key = SecretString::new("abcdefghijklmnop".to_string());
    store(&home).set("zai", &key).expect("set");
    let path = store(&home).path_for("zai");
    assert_eq!(
        fs::read_to_string(&path).expect("read"),
        "ZAI_API_KEY=abcdefghijklmnop\n"
    );
    assert_eq!(
        fs::metadata(&path).expect("stat").permissions().mode() & 0o777,
        0o600
    );
    assert_eq!(
        fs::metadata(path.parent().expect("dir"))
            .expect("stat")
            .permissions()
            .mode()
            & 0o777,
        0o700
    );
}

#[test]
fn a_set_replaces_the_key_line_and_keeps_every_other_line() {
    let home = TempHome::new();
    let path = write_env(
        &home,
        "zai",
        "OTHER=1\nZAI_API_KEY=oldoldoldoldoldold\n\n",
        0o600,
    );
    store(&home)
        .set("zai", &SecretString::new("newnewnewnewnewnew".to_string()))
        .expect("set");
    assert_eq!(
        fs::read_to_string(&path).expect("read"),
        "OTHER=1\nZAI_API_KEY=newnewnewnewnewnew\n"
    );
}

#[test]
fn a_set_refuses_a_key_of_the_wrong_shape_without_naming_it() {
    let home = TempHome::new();
    match store(&home).set("zai", &SecretString::new("short".to_string())) {
        Err(error) => {
            assert!(matches!(error, KeystoreError::BadShape));
            assert!(!error.to_string().contains("short"));
        }
        Ok(()) => panic!("a short key must be refused"),
    }
}

#[test]
fn a_delete_removes_the_file_when_only_blank_lines_remain() {
    let home = TempHome::new();
    let path = write_env(&home, "zai", "ZAI_API_KEY=abcdefghijklmnop\n\n", 0o600);
    store(&home).delete("zai").expect("delete");
    assert!(!path.exists());
}

#[test]
fn a_delete_keeps_other_lines() {
    let home = TempHome::new();
    let path = write_env(
        &home,
        "zai",
        "OTHER=1\nZAI_API_KEY=abcdefghijklmnop\n",
        0o600,
    );
    store(&home).delete("zai").expect("delete");
    assert_eq!(fs::read_to_string(&path).expect("read"), "OTHER=1\n");
}

#[test]
fn a_set_then_delete_round_trip_leaves_no_key() {
    let home = TempHome::new();
    let key = SecretString::new("abcdefghijklmnop".to_string());
    store(&home).set("kimi", &key).expect("set");
    assert_eq!(
        store(&home)
            .get("kimi")
            .expect("get")
            .expect("key")
            .expose(),
        "abcdefghijklmnop"
    );
    store(&home).delete("kimi").expect("delete");
    assert!(store(&home).get("kimi").expect("get").is_none());
}

#[test]
fn the_key_shape_accepts_16_to_512_safe_characters() {
    assert!(is_valid_key(&"a".repeat(16)));
    assert!(is_valid_key(&"a.b_c-d".repeat(40)));
    assert!(!is_valid_key(&"a".repeat(15)));
    assert!(!is_valid_key(&"a".repeat(513)));
    assert!(!is_valid_key("abcdefghijklmnop q"));
    assert!(assert_valid_key("abcdefghijklmnop").is_ok());
}

#[test]
fn the_service_is_provider_plugin_cc() {
    assert_eq!(service_name("zai"), "zai-plugin-cc");
}

#[test]
fn a_secret_string_never_shows_in_debug() {
    let secret = SecretString::new("abcdefghijklmnop".to_string());
    assert_eq!(format!("{secret:?}"), "***");
    assert_eq!(format!("{secret}"), "***");
}

#[test]
fn a_home_that_is_not_the_login_account_uses_only_the_key_file() {
    let home = TempHome::new();
    let env = Env::with(&[("HOME", home.root().to_str().expect("utf8"))]);
    let paths = Paths::from_env(&env).expect("paths");
    let keystore = platform_default(&paths);
    let key = SecretString::new("abcdefghijklmnop".to_string());
    keystore.set("zai", &key).expect("set");
    assert_eq!(
        fs::read_to_string(
            FileStore::new(paths.config_dir.clone(), paths.home.clone()).path_for("zai")
        )
        .expect("read"),
        "ZAI_API_KEY=abcdefghijklmnop\n"
    );
}

#[test]
fn b5_a_key_is_stored_under_huddle_keys() {
    let home = TempHome::new();
    let key = SecretString::new("abcdefghijklmnop".to_string());
    store(&home).set("zai", &key).expect("set");
    let path = home.root().join(".config/huddle/keys/zai");
    assert_eq!(store(&home).path_for("zai"), path);
    assert_eq!(
        fs::read_to_string(&path).expect("read"),
        "ZAI_API_KEY=abcdefghijklmnop\n"
    );
    assert_eq!(mode_of(&path), 0o600);
    assert_eq!(mode_of(path.parent().expect("dir")), 0o700);
}

#[test]
fn b5_an_old_key_file_moves_on_first_read() {
    let home = TempHome::new();
    let old = write_old_env(&home, "zai", "ZAI_API_KEY=abcdefghijklmnop\n");
    let got = store(&home).get("zai").expect("get").expect("key");
    assert_eq!(got.expose(), "abcdefghijklmnop");
    let new = store(&home).path_for("zai");
    assert_eq!(
        fs::read_to_string(&new).expect("read"),
        "ZAI_API_KEY=abcdefghijklmnop\n"
    );
    assert_eq!(mode_of(&new), 0o600);
    assert_eq!(mode_of(new.parent().expect("dir")), 0o700);
    assert!(!old.exists());
    assert!(!old.parent().expect("dir").exists());
}

#[test]
fn b5_a_failed_move_keeps_the_old_file_and_returns_the_key() {
    let home = TempHome::new();
    let old = write_old_env(&home, "zai", "ZAI_API_KEY=abcdefghijklmnop\n");
    // A keys folder nobody can write to makes the move fail.
    let keys = home.root().join(".config/huddle/keys");
    fs::create_dir_all(&keys).expect("mkdir");
    fs::set_permissions(&keys, fs::Permissions::from_mode(0o500)).expect("chmod");
    let got = store(&home).get("zai").expect("get").expect("key");
    assert_eq!(got.expose(), "abcdefghijklmnop");
    assert_eq!(
        fs::read_to_string(&old).expect("old stays"),
        "ZAI_API_KEY=abcdefghijklmnop\n"
    );
}

#[test]
fn b5_remove_deletes_both_paths() {
    let home = TempHome::new();
    let old = write_old_env(&home, "zai", "ZAI_API_KEY=abcdefghijklmnop\n");
    let new = write_env(&home, "zai", "ZAI_API_KEY=abcdefghijklmnop\n", 0o600);
    store(&home).delete("zai").expect("delete");
    assert!(!new.exists());
    assert!(!old.exists());
    assert!(!old.parent().expect("dir").exists());
}
