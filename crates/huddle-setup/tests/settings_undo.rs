use huddle_catalog::providers::provider_by_id;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_setup::env_file::RouterTarget;
use huddle_setup::model_entries::{RemoveOutcome, apply, remove};
use huddle_setup::settings::{apply_proxy, backup_path, ledger_path, pristine_path, settings_path};
use huddle_setup::undo::{UndoOutcome, undo};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;

fn fixture() -> (TempHome, Paths, RouterTarget) {
    let home = TempHome::new();
    let root = home.root().to_string_lossy().into_owned();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let target = RouterTarget {
        port: 18999,
        router_url: "http://127.0.0.1:18999".into(),
        ca_pem: paths.ca_dir.join("ca.pem").to_string_lossy().into_owned(),
        process_https_proxy: None,
        ca_error: None,
    };
    (home, paths, target)
}

const ODD_FORMAT: &str = "{\n      \"a\":1,\n\t\"list\" : [1,   2],\n \"num\": 1.10\n}\n\n";

#[test]
fn undo_restores_an_unusually_formatted_file_byte_for_byte() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, ODD_FORMAT).expect("write");

    apply_proxy(&paths, &target).expect("patch");
    assert_ne!(fs::read_to_string(&settings).expect("read"), ODD_FORMAT);

    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: true }
    );
    assert_eq!(fs::read_to_string(&settings).expect("read"), ODD_FORMAT);
    assert!(!ledger_path(&paths).exists());
    assert!(!backup_path(&paths).exists());
}

#[test]
fn undo_deletes_a_settings_file_huddle_created() {
    let (_home, paths, target) = fixture();
    let settings = settings_path(&paths);

    apply_proxy(&paths, &target).expect("patch");
    assert!(settings.exists());

    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: true }
    );
    assert!(!settings.exists());
}

#[test]
fn undo_with_no_ledger_reports_nothing() {
    let (_home, paths, _target) = fixture();
    assert_eq!(undo(&paths).expect("undo"), UndoOutcome::Nothing);
}

#[test]
fn a_user_edit_after_the_patch_stays_and_huddles_keys_go() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, "{\"theme\":\"dark\"}\n").expect("write");
    apply_proxy(&paths, &target).expect("patch");

    let mut doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    doc["userAdded"] = serde_json::json!(true);
    fs::write(&settings, serde_json::to_string(&doc).expect("ser")).expect("write");

    let outcome = undo(&paths).expect("undo");
    assert_eq!(outcome, UndoOutcome::Restored { exact: false });
    let after: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    assert_eq!(after["userAdded"], true);
    assert_eq!(after["theme"], "dark");
    assert!(after.get("env").is_none(), "{after}");
}

#[test]
fn a_result_equal_to_the_backup_gets_the_backups_own_bytes() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, ODD_FORMAT).expect("write");
    apply_proxy(&paths, &target).expect("patch");

    // Something else re-serialized the file: same content, different bytes, hash no longer matches.
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    fs::write(&settings, serde_json::to_string(&doc).expect("ser")).expect("write");

    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: true }
    );
    assert_eq!(fs::read_to_string(&settings).expect("read"), ODD_FORMAT);
}

#[test]
fn a_foreign_proxy_set_after_the_patch_is_left_alone() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, "{\"env\":{\"NO_PROXY\":\"corp\"}}\n").expect("write");
    apply_proxy(&paths, &target).expect("patch");

    let mut doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    doc["env"]["HTTPS_PROXY"] = serde_json::json!("http://corp:3128");
    fs::write(&settings, serde_json::to_string(&doc).expect("ser")).expect("write");

    assert_eq!(undo(&paths).expect("undo"), UndoOutcome::NotOurs);
    let after: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    assert_eq!(after["env"]["HTTPS_PROXY"], "http://corp:3128");
    assert!(!ledger_path(&paths).exists());
}

/// The order a real machine sees: the provider sets up first, the install patch follows.
/// The bytes the user had before either patch must come back.
#[test]
fn provider_then_install_then_remove_then_undo_restores_the_original_bytes() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, ODD_FORMAT).expect("write");
    let zai = provider_by_id("zai").expect("zai");

    apply(&paths, zai).expect("apply");
    let pristine = pristine_path(&paths);
    let mode = fs::metadata(&pristine)
        .expect("pristine")
        .permissions()
        .mode()
        & 0o777;
    assert_eq!(mode, 0o600);
    apply_proxy(&paths, &target).expect("patch");
    assert_eq!(
        fs::read(&pristine).expect("pristine"),
        ODD_FORMAT.as_bytes(),
        "the first writer's copy stands"
    );
    assert!(matches!(
        remove(&paths, zai).expect("remove"),
        RemoveOutcome::Removed { .. }
    ));
    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: true }
    );
    assert_eq!(fs::read(&settings).expect("read"), ODD_FORMAT.as_bytes());
    assert!(!ledger_path(&paths).exists());
    assert!(!backup_path(&paths).exists());
    assert!(!pristine.exists());
}

/// A settings.json huddle created from nothing had no user bytes to keep, so no pristine copy
/// exists and the undo removes the file whole.
#[test]
fn a_settings_file_huddle_created_saves_no_pristine_copy() {
    let (_home, paths, target) = fixture();
    apply_proxy(&paths, &target).expect("patch");
    assert!(!pristine_path(&paths).exists());
    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: true }
    );
    assert!(!settings_path(&paths).exists());
    assert!(!pristine_path(&paths).exists());
}

/// The other order: the install patch first, the provider on top. The existing restore path
/// already handles it; the pristine copy must not change its answer.
#[test]
fn install_then_provider_then_remove_then_undo_restores_the_original_bytes() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, ODD_FORMAT).expect("write");
    let zai = provider_by_id("zai").expect("zai");

    apply_proxy(&paths, &target).expect("patch");
    apply(&paths, zai).expect("apply");
    assert!(matches!(
        remove(&paths, zai).expect("remove"),
        RemoveOutcome::Removed { .. }
    ));
    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: true }
    );
    assert_eq!(fs::read(&settings).expect("read"), ODD_FORMAT.as_bytes());
    assert!(!ledger_path(&paths).exists());
    assert!(!backup_path(&paths).exists());
}

/// A change of the user's own between the patches is not huddle's to take back: the file keeps
/// its shape and their key, and loses only huddle's keys.
#[test]
fn a_user_change_between_the_patches_stays_after_undo() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(&settings, ODD_FORMAT).expect("write");
    let zai = provider_by_id("zai").expect("zai");

    apply(&paths, zai).expect("apply");
    apply_proxy(&paths, &target).expect("patch");
    let mut doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    doc["userAdded"] = serde_json::json!(true);
    fs::write(&settings, serde_json::to_string(&doc).expect("ser")).expect("write");

    assert!(matches!(
        remove(&paths, zai).expect("remove"),
        RemoveOutcome::Removed { .. }
    ));
    assert_eq!(
        undo(&paths).expect("undo"),
        UndoOutcome::Restored { exact: false }
    );
    let after: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    assert_eq!(after["userAdded"], true);
    assert_eq!(after["a"], 1);
    assert!(after.get("env").is_none(), "{after}");
    assert!(after.get("modelPicker").is_none(), "{after}");
    assert_ne!(fs::read(&settings).expect("read"), ODD_FORMAT.as_bytes());
}
