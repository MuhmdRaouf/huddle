use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::{
    fs,
    os::unix::fs::PermissionsExt,
    path::PathBuf,
    process::{Command, Output, Stdio},
};

fn huddle(home: &TempHome, port: u16, args: &[&str]) -> Output {
    huddle_with(home, port, args, &[])
}

fn huddle_with(home: &TempHome, port: u16, args: &[&str], extra: &[(&str, &str)]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .envs(extra.iter().copied())
        .stdin(Stdio::null())
        .output()
        .expect("run")
}

/// A loopback port nothing can listen on: the key check fails at once and reads unknown, so
/// the word goes on to the steps this test proves.
const UNREACHABLE: &str = "http://127.0.0.1:1";

/// An upstream that answers every request the same fixed status.
fn fake_answering(status: u16) -> FakeUpstream {
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |_, _| Step::answer(status, "{}")))
}

/// The key file the zai plugin writes: a mode-600 env file in a mode-700 directory.
fn stored_zai_key(home: &TempHome) {
    let dir = home.root().join(".config/zai-plugin-cc");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("chmod");
    let file = dir.join("env");
    fs::write(&file, "ZAI_API_KEY=abcdefghijklmnop\n").expect("write");
    fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).expect("chmod");
}

fn stop(home: &TempHome, port: u16) {
    let _ = huddle(home, port, &["router", "stop"]);
}

/// The temp home's state folder, where the settings ledger lives.
fn state_home(home: &TempHome) -> PathBuf {
    home.root().join(".local/state/huddle")
}

#[test]
fn k3_setup_without_a_key_names_the_terminal_flow() {
    let home = TempHome::new();
    let port = free_port();
    let output = huddle(&home, port, &["provider", "zai", "setup"]);
    assert_eq!(output.status.code(), Some(6), "{output:?}");
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert_eq!(
        stdout, "zai setup: needs a key.\nIn a terminal: huddle provider zai\n",
        "{stdout}"
    );
    assert!(
        !home.root().join(".config/huddle/providers.toml").exists(),
        "no key, no lane"
    );
    stop(&home, port);
}

#[test]
fn setup_with_a_key_enables_the_lane_and_serves_the_provider() {
    let home = TempHome::new();
    let port = free_port();
    stored_zai_key(&home);
    let output = huddle_with(
        &home,
        port,
        &["provider", "zai", "setup"],
        &[("ZAI_BASE_URL", UNREACHABLE)],
    );
    let stdout = String::from_utf8_lossy(&output.stdout).into_owned();
    stop(&home, port);
    // Not ready because /huddle:setup never ran here; the lane and the router line are what this proves.
    assert_eq!(output.status.code(), Some(6), "{stdout}");
    assert!(stdout.contains("key: stored"), "{stdout}");
    assert!(
        stdout.contains("added GLM 5.3 (glm-5.3) to /model"),
        "{stdout}"
    );
    assert!(stdout.contains("huddle-router serves Z.ai GLM"), "{stdout}");
    assert!(stdout.contains("run /huddle:setup first"), "{stdout}");
    let providers =
        fs::read_to_string(home.root().join(".config/huddle/providers.toml")).expect("providers");
    assert!(providers.contains("zai"), "{providers}");
}

#[test]
fn a_key_from_the_environment_is_named() {
    let home = TempHome::new();
    let port = free_port();
    let output = huddle_with(
        &home,
        port,
        &["provider", "kimi", "setup"],
        &[
            ("KIMI_API_KEY", "abcdefghijklmnop"),
            ("KIMI_BASE_URL", UNREACHABLE),
        ],
    );
    let stdout = String::from_utf8_lossy(&output.stdout).into_owned();
    stop(&home, port);
    assert!(stdout.contains("key: KIMI_API_KEY"), "{stdout}");
}

#[test]
fn a_ready_setup_says_so_and_asks_for_one_restart() {
    let home = TempHome::new();
    let port = free_port();
    stored_zai_key(&home);
    // /huddle:setup's ledger: any file at the path counts for this word.
    let ledger = state_home(&home).join("settings-ledger.toml");
    fs::create_dir_all(ledger.parent().expect("parent")).expect("mkdir");
    fs::write(&ledger, "{}").expect("write");
    let output = huddle_with(
        &home,
        port,
        &["provider", "zai", "setup"],
        &[("ZAI_BASE_URL", UNREACHABLE)],
    );
    let stdout = String::from_utf8_lossy(&output.stdout).into_owned();
    stop(&home, port);
    assert_eq!(output.status.code(), Some(0), "{stdout}");
    assert!(stdout.starts_with("zai setup: ready.\n"), "{stdout}");
    assert!(
        stdout.ends_with("Restart Claude Code once so /model shows the new models.\n"),
        "{stdout}"
    );
}

#[test]
fn a_refused_key_stops_setup_before_it_enables_anything() {
    let home = TempHome::new();
    let port = free_port();
    let fake = fake_answering(401);
    stored_zai_key(&home);
    let output = huddle_with(
        &home,
        port,
        &["provider", "zai", "setup"],
        &[("ZAI_BASE_URL", fake.url().as_str())],
    );
    let stdout = String::from_utf8_lossy(&output.stdout).into_owned();
    stop(&home, port);
    assert_eq!(output.status.code(), Some(6), "{stdout}");
    assert!(stdout.contains("refused the stored key"), "{stdout}");
    assert!(
        !home.root().join(".config/huddle/providers.toml").exists(),
        "a refused key enables nothing"
    );
}

#[test]
fn a_limited_key_keeps_setup_ready_and_names_the_billing_page() {
    let home = TempHome::new();
    let port = free_port();
    let fake = fake_answering(429);
    stored_zai_key(&home);
    // /huddle:setup's ledger: any file at the path counts for this word.
    let ledger = state_home(&home).join("settings-ledger.toml");
    fs::create_dir_all(ledger.parent().expect("parent")).expect("mkdir");
    fs::write(&ledger, "{}").expect("write");
    let output = huddle_with(
        &home,
        port,
        &["provider", "zai", "setup"],
        &[("ZAI_BASE_URL", fake.url().as_str())],
    );
    let stdout = String::from_utf8_lossy(&output.stdout).into_owned();
    stop(&home, port);
    assert_eq!(output.status.code(), Some(0), "{stdout}");
    assert!(stdout.contains("knows the key"), "{stdout}");
    assert!(
        stdout.contains("https://z.ai/manage-apikey/billing"),
        "{stdout}"
    );
}
