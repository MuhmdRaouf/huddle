mod e2e_support;

use e2e_support::{request, try_request, upstream};
use huddle_testkit::fake_upstream::FakeUpstream;
use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::process::{Command, Output};
use std::thread;
use std::time::{Duration, Instant};

struct Rig {
    home: TempHome,
    port: u16,
    anthropic: FakeUpstream,
    zai: FakeUpstream,
    settings: std::path::PathBuf,
}

impl Rig {
    fn new() -> Rig {
        let home = TempHome::new();
        let anthropic = upstream("{\"from\":\"anthropic\"}");
        let zai = upstream("{\"from\":\"zai\"}");
        let settings = home.root().join(".claude/settings.json");
        fs::create_dir_all(settings.parent().expect("parent")).expect("mkdir");
        Rig {
            home,
            port: free_port(),
            anthropic,
            zai,
            settings,
        }
    }

    fn run(&self, args: &[&str]) -> Output {
        Command::new(env!("CARGO_BIN_EXE_huddle"))
            .args(args)
            .env_clear()
            .envs(self.home.env())
            .env("CLAUDE_CONFIG_DIR", self.home.root().join(".claude"))
            .env("HUDDLE_ROUTER_PORT", self.port.to_string())
            .env("HUDDLE_ROUTER_ANTHROPIC_URL", self.anthropic.url())
            .env("ZAI_BASE_URL", self.zai.url())
            .env("ZAI_API_KEY", "abcdefghijklmnop")
            .output()
            .expect("run huddle")
    }

    fn wait_up(&self) {
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline {
            if try_request(self.port, "GET", "/huddle-router/health", &[], b"").is_some() {
                return;
            }
            thread::sleep(Duration::from_millis(50));
        }
        panic!("the router did not come up on port {}", self.port);
    }
}

impl Drop for Rig {
    fn drop(&mut self) {
        let _ = self.run(&["router", "stop"]);
    }
}

fn setup_all(rig: &Rig) {
    // Provider first, so the router has a lane to serve; then /huddle:setup.
    let provider = rig.run(&["provider", "zai", "setup"]);
    assert!(
        provider.status.code() == Some(0) || provider.status.code() == Some(6),
        "provider setup: {} {}",
        String::from_utf8_lossy(&provider.stdout),
        String::from_utf8_lossy(&provider.stderr)
    );
    let install = rig.run(&["setup", "--install"]);
    assert_eq!(
        install.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&install.stderr)
    );
    rig.wait_up();
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&rig.settings).expect("settings")).expect("json");
    assert_eq!(
        doc["env"]["HTTPS_PROXY"],
        format!("http://127.0.0.1:{}", rig.port)
    );
}

#[test]
fn a_set_up_provider_routes_its_model_and_anthropic_stays_untouched() {
    let rig = Rig::new();
    setup_all(&rig);

    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&rig.settings).expect("settings")).expect("json");
    let options = doc["modelPicker"]["options"].as_array().expect("options");
    assert!(options.iter().any(|o| o["model"] == "glm-5.3"), "{doc}");

    let glm = request(
        rig.port,
        "POST",
        "/v1/messages",
        &[("content-type", "application/json")],
        br#"{"model":"glm-5.3","messages":[]}"#,
    );
    assert_eq!(glm.status, 200, "{}", glm.body);
    let routed = rig.zai.requests();
    let glm_hit = routed
        .last()
        .expect("the provider answered the routed request");
    assert_eq!(glm_hit.path, "/v1/messages");
    let auth = glm_hit
        .headers
        .iter()
        .find(|(k, _)| k.eq_ignore_ascii_case("authorization"))
        .map(|(_, v)| v.as_str());
    assert_eq!(
        auth,
        Some("Bearer abcdefghijklmnop"),
        "the saved key reaches the provider"
    );

    let claude = request(
        rig.port,
        "POST",
        "/v1/messages",
        &[
            ("content-type", "application/json"),
            ("x-api-key", "client"),
        ],
        br#"{"model":"claude-opus-5-5","messages":[]}"#,
    );
    assert_eq!(claude.status, 200, "{}", claude.body);
    assert_eq!(rig.anthropic.requests().len(), 1);
    assert_eq!(
        rig.zai.requests().len(),
        routed.len(),
        "claude did not touch the provider"
    );

    let usage = rig.run(&["provider", "zai", "usage"]);
    assert_eq!(
        usage.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&usage.stderr)
    );
    assert!(String::from_utf8_lossy(&usage.stdout).contains("Z.ai GLM"));
}

#[test]
fn remove_and_uninstall_restore_settings_byte_for_byte() {
    let rig = Rig::new();
    let original = "{\n    \"theme\":  \"dark\"\n}\n";
    fs::write(&rig.settings, original).expect("write settings");
    setup_all(&rig);
    assert_ne!(fs::read_to_string(&rig.settings).expect("read"), original);

    let remove = rig.run(&["provider", "zai", "remove"]);
    assert_eq!(
        remove.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&remove.stderr)
    );
    let providers = fs::read_to_string(rig.home.root().join(".config/huddle/providers.toml"))
        .expect("providers");
    assert!(!providers.contains("zai"), "{providers}");

    let uninstall = rig.run(&["uninstall"]);
    assert_eq!(
        uninstall.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&uninstall.stderr)
    );
    assert_eq!(fs::read_to_string(&rig.settings).expect("read"), original);
}
