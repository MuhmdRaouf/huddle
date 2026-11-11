use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_setup::env_file::{RouteMode, RouterTarget};
use huddle_setup::settings::{
    Ledger, SettingsError, apply_proxy, backup_path, ledger_path, settings_path, sha256_hex,
};
use huddle_testkit::temp_home::TempHome;
use std::fs;

fn fixture() -> (TempHome, Paths, RouterTarget) {
    let home = TempHome::new();
    let root = home.root().to_string_lossy().into_owned();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let target = RouterTarget {
        port: 18999,
        router_url: "http://127.0.0.1:18999".into(),
        ca_pem: paths
            .ca_dir
            .join("huddle-ca.pem")
            .to_string_lossy()
            .into_owned(),
        process_https_proxy: None,
        ca_error: None,
    };
    (home, paths, target)
}

#[test]
fn a_fresh_file_is_patched_then_a_second_run_changes_nothing() {
    let (_home, paths, target) = fixture();

    let first = apply_proxy(&paths, &target).expect("first patch");
    assert!(first.changed);
    assert_eq!(first.mode, RouteMode::Proxy);

    let settings = settings_path(&paths);
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("settings")).expect("json");
    assert_eq!(doc["env"]["HTTPS_PROXY"], "http://127.0.0.1:18999");
    assert_eq!(doc["env"]["NO_PROXY"], "localhost,127.0.0.1");
    assert_eq!(doc["env"]["ENABLE_TOOL_SEARCH"], "true");

    let bytes = fs::read(&settings).expect("settings bytes");
    let ledger_bytes = fs::read(ledger_path(&paths)).expect("ledger bytes");
    let ledger: Ledger = serde_json::from_slice(&ledger_bytes).expect("ledger json");
    assert!(ledger.created_file);
    assert_eq!(ledger.after_sha256, sha256_hex(&bytes));
    assert_eq!(ledger.before.get("HTTPS_PROXY"), Some(&None));

    let second = apply_proxy(&paths, &target).expect("second patch");
    assert!(!second.changed);
    assert_eq!(fs::read(&settings).expect("settings bytes"), bytes);
    assert_eq!(
        fs::read(ledger_path(&paths)).expect("ledger bytes"),
        ledger_bytes
    );
}

#[test]
fn an_existing_file_keeps_its_key_order_and_gets_a_backup() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(paths.claude_dir.as_path()).expect("mkdir");
    let settings = settings_path(&paths);
    let original = "{\n  \"zeta\": 1,\n  \"alpha\": {\"b\": 2, \"a\": 1}\n}\n";
    fs::write(&settings, original).expect("write");

    apply_proxy(&paths, &target).expect("patch");

    let text = fs::read_to_string(&settings).expect("read");
    let zeta = text.find("\"zeta\"").expect("zeta");
    let alpha = text.find("\"alpha\"").expect("alpha");
    let env = text.find("\"env\"").expect("env");
    assert!(zeta < alpha && alpha < env, "{text}");
    assert_eq!(
        fs::read_to_string(backup_path(&paths)).expect("backup"),
        original
    );
    assert!(text.ends_with("}\n"), "{text}");
}

#[test]
fn a_foreign_https_proxy_in_settings_uses_the_base_url_and_leaves_the_proxy_alone() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(paths.claude_dir.as_path()).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(
        &settings,
        "{\"env\":{\"HTTPS_PROXY\":\"http://corp:3128\"}}\n",
    )
    .expect("write");

    let out = apply_proxy(&paths, &target).expect("patch");

    assert_eq!(out.mode, RouteMode::BaseUrl);
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    assert_eq!(doc["env"]["HTTPS_PROXY"], "http://corp:3128");
    assert_eq!(doc["env"]["ANTHROPIC_BASE_URL"], "http://127.0.0.1:18999");
}

#[test]
fn a_malformed_or_misshapen_file_is_refused_and_left_alone() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(paths.claude_dir.as_path()).expect("mkdir");
    let settings = settings_path(&paths);

    fs::write(&settings, "{ not json").expect("write");
    assert!(matches!(
        apply_proxy(&paths, &target),
        Err(SettingsError::Json { .. })
    ));
    assert_eq!(fs::read_to_string(&settings).expect("read"), "{ not json");
    assert!(!ledger_path(&paths).exists());

    fs::write(&settings, "{\"env\":[1]}").expect("write");
    assert!(matches!(
        apply_proxy(&paths, &target),
        Err(SettingsError::Shape { .. })
    ));
    assert_eq!(
        fs::read_to_string(&settings).expect("read"),
        "{\"env\":[1]}"
    );
}

#[test]
fn the_uppercase_keys_are_written_and_the_lowercase_one_only_read() {
    let (_home, paths, target) = fixture();
    fs::create_dir_all(paths.claude_dir.as_path()).expect("mkdir");
    let settings = settings_path(&paths);
    fs::write(
        &settings,
        "{\"env\":{\"https_proxy\":\"http://corp:3128\"}}\n",
    )
    .expect("write");

    let out = apply_proxy(&paths, &target).expect("patch");

    assert_eq!(
        out.mode,
        RouteMode::BaseUrl,
        "a lowercase proxy in force is the user's own"
    );
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("read")).expect("json");
    assert_eq!(doc["env"]["https_proxy"], "http://corp:3128");
    assert!(doc["env"].get("HTTPS_PROXY").is_none());
}
