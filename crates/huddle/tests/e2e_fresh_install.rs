mod e2e_support;

use e2e_support::{request, upstream};
use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::time::{Duration, Instant};

const BODY: &[u8] = br#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}"#;
const SETTINGS_BEFORE: &str = "{\n  \"theme\": \"dark\"\n}\n";

/// Where each kind of file lives for one run.
struct Layout {
    config: PathBuf,
    data: PathBuf,
    state: PathBuf,
    /// Variables added to the child environment; the XDG ones not listed are removed.
    vars: Vec<(&'static str, String)>,
}

fn huddle(home: &TempHome, layout: &Layout, port: u16, anthropic: &str, args: &[&str]) -> Output {
    let mut cmd = Command::new(env!("CARGO_BIN_EXE_huddle"));
    cmd.args(args)
        .env_clear()
        .envs(home.env())
        .env_remove("XDG_CONFIG_HOME")
        .env_remove("XDG_DATA_HOME")
        .env_remove("XDG_STATE_HOME")
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .env("HUDDLE_ROUTER_ANTHROPIC_URL", anthropic);
    for (name, value) in &layout.vars {
        cmd.env(name, value);
    }
    cmd.output().expect("run huddle")
}

struct StopOnDrop<'a> {
    home: &'a TempHome,
    layout: &'a Layout,
    port: u16,
}

impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        huddle(self.home, self.layout, self.port, "", &["router", "stop"]);
    }
}

fn search_total(port: u16) -> u64 {
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        let reply = request(
            port,
            "GET",
            "/huddle-router/history?view=search&range=24h",
            &[],
            b"",
        );
        assert_eq!(reply.status, 200, "{}", reply.body);
        let doc: serde_json::Value = serde_json::from_str(&reply.body).expect("history json");
        let total = doc["total"].as_u64().unwrap_or(0);
        if total >= 1 || Instant::now() > deadline {
            return total;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

fn walk(home: &TempHome, layout: &Layout) {
    let port = free_port();
    let fake = upstream("{\"from\":\"anthropic\"}");
    let anthropic = fake.url();
    let settings = home.root().join(".claude/settings.json");
    fs::write(&settings, SETTINGS_BEFORE).expect("seed settings");
    let _stop = StopOnDrop { home, layout, port };

    let out = huddle(home, layout, port, &anthropic, &["setup", "--install"]);
    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );

    let defaults = fs::read_to_string(layout.config.join("config.toml")).expect("config.toml");
    assert!(defaults.contains('#'), "commented defaults: {defaults}");
    let ca = layout.data.join("ca/huddle-ca.pem");
    assert!(ca.exists());
    assert!(layout.state.exists());
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("settings")).expect("json");
    assert_eq!(
        Path::new(doc["env"]["NODE_EXTRA_CA_CERTS"].as_str().expect("ca")),
        ca
    );

    let reply = request(
        port,
        "POST",
        "/v1/messages",
        &[("content-type", "application/json")],
        BODY,
    );
    assert_eq!(reply.status, 200, "{}", reply.body);
    assert_eq!(search_total(port), 1);
    assert!(layout.state.join("huddle.db").exists());

    let out = huddle(home, layout, port, &anthropic, &["uninstall"]);
    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(
        fs::read_to_string(&settings).expect("settings"),
        SETTINGS_BEFORE
    );
    assert!(!home.root().join(".agents").exists());
}

#[test]
fn t11_a_fresh_install_walks_from_setup_to_history_to_uninstall_in_the_default_folders() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let layout = Layout {
        config: root.join(".config/huddle"),
        data: root.join(".local/share/huddle"),
        state: root.join(".local/state/huddle"),
        vars: vec![],
    };
    walk(&home, &layout);
}

#[test]
fn t11_a_fresh_install_walks_the_same_path_in_the_folders_the_huddle_variables_name() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let layout = Layout {
        config: root.join("mine/config"),
        data: root.join("mine/data"),
        state: root.join("mine/state"),
        vars: vec![
            (
                "HUDDLE_CONFIG_HOME",
                root.join("mine/config").display().to_string(),
            ),
            (
                "HUDDLE_DATA_HOME",
                root.join("mine/data").display().to_string(),
            ),
            (
                "HUDDLE_STATE_HOME",
                root.join("mine/state").display().to_string(),
            ),
        ],
    };
    walk(&home, &layout);
    assert!(!root.join(".local/state/huddle").exists());
}
