use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::process::{Command, Output};
use std::thread;
use std::time::{Duration, Instant};

fn huddle(home: &TempHome, port: u16, key: Option<&str>, args: &[&str]) -> Output {
    let mut cmd = Command::new(env!("CARGO_BIN_EXE_huddle"));
    cmd.args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string());
    if let Some(k) = key {
        cmd.env("ZAI_API_KEY", k);
    }
    cmd.output().expect("run huddle")
}

struct StopOnDrop<'a>(&'a TempHome, u16);
impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        // A handover can replace the daemon mid-stop, so the signal lands on
        // a dying owner while the takeover child keeps serving. The probe is
        // the port itself, not the record: the record can be absent while a
        // takeover child still serves. A refused connect ends the round.
        let address = format!("127.0.0.1:{}", self.1);
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            let _ = huddle(self.0, self.1, None, &["router", "stop"]);
            let serving = std::net::TcpStream::connect_timeout(
                &address.parse().expect("loopback address"),
                Duration::from_millis(250),
            )
            .is_ok();
            if !serving || Instant::now() >= deadline {
                return;
            }
            thread::sleep(Duration::from_millis(100));
        }
    }
}

/// Waits until the router answers status: the hook starts it detached and returns at once, so a
/// stop that runs before its record lands misses it. Then waits out the detached `router
/// start`: it lives at most its own 3 s budget, and any process of this run still alive at the
/// end fails the suite guard.
fn wait_running(home: &TempHome, port: u16) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut up = false;
    while Instant::now() < deadline {
        if huddle(home, port, None, &["router", "status"])
            .status
            .code()
            == Some(0)
        {
            up = true;
            break;
        }
        thread::sleep(Duration::from_millis(50));
    }
    // A router that starts late under load still writes its record; the home's drop stops it.
    home.settle_router(Duration::from_secs(10));
    thread::sleep(Duration::from_secs(4));
    up
}

fn set_up_state(home: &TempHome) {
    let huddle_dir = home.root().join(".local/state/huddle");
    fs::create_dir_all(&huddle_dir).expect("mkdir huddle");
    fs::write(huddle_dir.join("settings-ledger.toml"), "{}\n").expect("ledger");
    let router = home.root().join(".config/huddle");
    fs::create_dir_all(&router).expect("mkdir router");
    fs::write(router.join("providers.toml"), "providers = [\"zai\"]\n").expect("providers");
    fs::create_dir_all(home.root().join(".claude")).expect("mkdir claude");
}

fn settings(home: &TempHome) -> serde_json::Value {
    let text = fs::read_to_string(home.root().join(".claude/settings.json")).expect("settings");
    serde_json::from_str(&text).expect("json")
}

#[test]
fn a_set_up_provider_gets_its_model_entries_back_and_a_key_is_silent() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    set_up_state(&home);

    let out = huddle(
        &home,
        port,
        Some("abcdefghijklmnop"),
        &["hook", "session-start"],
    );

    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(String::from_utf8_lossy(&out.stdout), "");
    wait_running(&home, port);
    let doc = settings(&home);
    let options = doc["modelPicker"]["options"].as_array().expect("options");
    assert!(options.iter().any(|o| o["model"] == "glm-5.3"), "{doc}");
}

#[test]
fn a_missing_key_prints_one_json_object_naming_setup() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    set_up_state(&home);

    let out = huddle(&home, port, None, &["hook", "session-start"]);

    assert_eq!(out.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert_eq!(stdout.lines().count(), 1, "{stdout}");
    let doc: serde_json::Value = serde_json::from_str(stdout.trim()).expect("json");
    let msg = doc["systemMessage"].as_str().expect("systemMessage");
    assert!(msg.contains("no Z.ai GLM key found"), "{msg}");
    assert!(msg.contains("/huddle:zai:setup"), "{msg}");
    wait_running(&home, port);
}

#[test]
fn a_provider_with_no_plugin_entry_is_cleaned_up() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    set_up_state(&home);
    let plugins = home.root().join(".claude/plugins");
    fs::create_dir_all(&plugins).expect("mkdir");
    fs::write(
        plugins.join("installed_plugins.json"),
        "{\"plugins\":{\"huddle@muhmdraouf\":[{}]}}",
    )
    .expect("write");

    let out = huddle(
        &home,
        port,
        Some("abcdefghijklmnop"),
        &["hook", "session-start"],
    );

    assert_eq!(out.status.code(), Some(0));
    let enabled =
        fs::read_to_string(home.root().join(".config/huddle/providers.toml")).expect("providers");
    assert!(!enabled.contains("zai"), "{enabled}");
    wait_running(&home, port);
}
