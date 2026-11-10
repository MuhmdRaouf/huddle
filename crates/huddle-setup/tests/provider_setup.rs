use huddle_catalog::providers::provider_by_id;
use huddle_config::{env::Env, paths::Paths};
use huddle_setup::{
    model_entries::{apply, backup_path, ledger_path},
    provider_setup::enable,
    settings::{SettingsError, settings_path},
};
use huddle_testkit::temp_home::TempHome;
use std::fs;

fn fixture() -> (TempHome, Paths) {
    let home = TempHome::new();
    let root = home.root().to_string_lossy().into_owned();
    let paths = Paths::from_env(&Env::with(&[("HOME", root.as_str())])).expect("paths");
    (home, paths)
}

fn doc(paths: &Paths) -> serde_json::Value {
    serde_json::from_slice(&fs::read(settings_path(paths)).expect("read")).expect("json")
}

#[test]
fn a_provider_gets_its_models_and_effort_in_a_fresh_settings_file() {
    let (_home, paths) = fixture();
    let zai = provider_by_id("zai").expect("zai");
    let outcome = apply(&paths, zai).expect("apply");
    assert!(outcome.changed);
    assert_eq!(
        outcome.lines,
        vec![
            "added GLM 5.3 (glm-5.3) to /model".to_string(),
            "added GLM 5.3 Flash (glm-5.3-flash) to /model".to_string(),
            "set GLM 5.3 default effort to xhigh".to_string(),
            "set GLM 5.3 Flash default effort to xhigh".to_string(),
        ]
    );
    let doc = doc(&paths);
    let options = doc["modelPicker"]["options"].as_array().expect("options");
    assert_eq!(
        options[0],
        serde_json::json!({"model": "glm-5.3", "label": "GLM 5.3", "behavesAs": "claude-opus-5-5"})
    );
    assert_eq!(options[1]["model"], "glm-5.3-flash");
    assert_eq!(doc["modelSettings"]["glm-5.3"]["effortLevel"], "xhigh");
    assert!(ledger_path(&paths, "zai").exists());
}

#[test]
fn a_catalog_model_with_no_behaves_as_writes_no_key() {
    let (_home, paths) = fixture();
    let kimi = provider_by_id("kimi").expect("kimi");
    apply(&paths, kimi).expect("apply");
    let options = doc(&paths)["modelPicker"]["options"]
        .as_array()
        .expect("options")
        .clone();
    assert_eq!(
        options[1],
        serde_json::json!({"model": "kimi-k2.6", "label": "Kimi K2.6"})
    );
}

#[test]
fn a_second_run_changes_nothing() {
    let (_home, paths) = fixture();
    let zai = provider_by_id("zai").expect("zai");
    apply(&paths, zai).expect("first");
    let settings_first = fs::read(settings_path(&paths)).expect("read");
    let ledger_first = fs::read(ledger_path(&paths, "zai")).expect("read");
    let outcome = apply(&paths, zai).expect("second");
    assert!(!outcome.changed);
    assert_eq!(
        fs::read(settings_path(&paths)).expect("read"),
        settings_first
    );
    assert_eq!(
        fs::read(ledger_path(&paths, "zai")).expect("read"),
        ledger_first
    );
}

#[test]
fn an_existing_option_keeps_its_row_and_gets_its_label_fixed() {
    let (_home, paths) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    fs::write(
        settings_path(&paths),
        r#"{"modelPicker":{"options":[{"model":"glm-5.3","label":"old","mine":true}]}}"#,
    )
    .expect("write");
    let zai = provider_by_id("zai").expect("zai");
    apply(&paths, zai).expect("apply");
    let doc = doc(&paths);
    let row = &doc["modelPicker"]["options"][0];
    assert_eq!(row["label"], "GLM 5.3");
    assert_eq!(row["mine"], true);
}

#[test]
fn available_models_is_appended_only_when_it_exists() {
    let (_home, paths) = fixture();
    let zai = provider_by_id("zai").expect("zai");
    apply(&paths, zai).expect("apply");
    assert!(doc(&paths).get("availableModels").is_none());
    let (_home_two, paths_two) = fixture();
    fs::create_dir_all(&paths_two.claude_dir).expect("mkdir");
    fs::write(
        settings_path(&paths_two),
        r#"{"availableModels":["sonnet"]}"#,
    )
    .expect("write");
    let outcome = apply(&paths_two, zai).expect("apply");
    assert!(
        outcome
            .lines
            .iter()
            .any(|line| line == "added glm-5.3 to availableModels")
    );
    assert_eq!(
        doc(&paths_two)["availableModels"],
        serde_json::json!(["sonnet", "glm-5.3", "glm-5.3-flash"])
    );
}

#[test]
fn a_user_effort_is_never_overwritten() {
    let (_home, paths) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    fs::write(
        settings_path(&paths),
        r#"{"modelSettings":{"glm-5.3":{"effortLevel":"low"}}}"#,
    )
    .expect("write");
    let zai = provider_by_id("zai").expect("zai");
    apply(&paths, zai).expect("apply");
    assert_eq!(
        doc(&paths)["modelSettings"]["glm-5.3"]["effortLevel"],
        "low"
    );
    assert_eq!(
        doc(&paths)["modelSettings"]["glm-5.3-flash"]["effortLevel"],
        "xhigh"
    );
}

#[test]
fn a_wrong_typed_picker_is_an_error_and_writes_nothing() {
    let (_home, paths) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    fs::write(settings_path(&paths), r#"{"modelPicker":"nope"}"#).expect("write");
    let zai = provider_by_id("zai").expect("zai");
    match apply(&paths, zai) {
        Err(SettingsError::Shape { what, .. }) => {
            assert_eq!(what, "modelPicker must be an object; nothing changed")
        }
        other => panic!("want Shape, got {:?}", other.map(|o| o.changed)),
    }
    assert_eq!(
        fs::read_to_string(settings_path(&paths)).expect("read"),
        r#"{"modelPicker":"nope"}"#
    );
}

#[test]
fn the_first_write_keeps_a_private_backup() {
    let (_home, paths) = fixture();
    fs::create_dir_all(&paths.claude_dir).expect("mkdir");
    fs::write(settings_path(&paths), "{\"keep\":1}\n").expect("write");
    let zai = provider_by_id("zai").expect("zai");
    apply(&paths, zai).expect("apply");
    assert_eq!(
        fs::read_to_string(backup_path(&paths, "zai")).expect("backup"),
        "{\"keep\":1}\n"
    );
}

#[test]
fn enable_records_the_lane_and_the_models() {
    let (_home, paths) = fixture();
    let zai = provider_by_id("zai").expect("zai");
    let enabled = enable(&paths, zai).expect("enable");
    assert!(enabled.changed);
    let providers = fs::read_to_string(paths.router_dir.join("providers.json")).expect("providers");
    assert!(providers.contains("\"zai\""), "{providers}");
}
