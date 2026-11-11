use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::net::{SocketAddr, TcpStream};
use std::process::{Command, Output};
use std::thread;
use std::time::{Duration, Instant};

fn huddle(home: &TempHome, port: u16, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .output()
        .expect("run huddle")
}

fn wait_for_port(port: u16) -> bool {
    let addr: SocketAddr = format!("127.0.0.1:{port}").parse().expect("addr");
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok() {
            return true;
        }
        thread::sleep(Duration::from_millis(50));
    }
    false
}

struct StopOnDrop<'a>(&'a TempHome, u16);
impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        let _ = huddle(self.0, self.1, &["router", "stop"]);
    }
}

#[test]
fn install_starts_the_router_patches_settings_and_says_restart_once() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let settings = home.root().join(".claude/settings.json");

    let first = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        first.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&first.stderr)
    );
    let stdout = String::from_utf8_lossy(&first.stdout);
    assert!(
        stdout.contains("Restart Claude Code once so it picks up the proxy settings."),
        "{stdout}"
    );

    assert!(wait_for_port(port), "the router answers on the port");

    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("settings")).expect("json");
    assert_eq!(
        doc["env"]["HTTPS_PROXY"],
        format!("http://127.0.0.1:{port}")
    );
    let ca = doc["env"]["NODE_EXTRA_CA_CERTS"].as_str().expect("ca path");
    assert!(ca.ends_with("/huddle-ca.pem"), "{ca}");
    assert!(
        std::path::Path::new(ca).exists(),
        "the CA file exists: {ca}"
    );
    let before = fs::read(&settings).expect("settings");

    let second = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        second.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&second.stderr)
    );
    assert!(!String::from_utf8_lossy(&second.stdout).contains("Restart Claude Code"));
    assert_eq!(
        fs::read(&settings).expect("settings"),
        before,
        "a second install changes nothing"
    );
}

#[test]
fn t1_install_keeps_the_ca_and_the_router_record_in_the_xdg_folders() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let settings = home.root().join(".claude/settings.json");

    let out = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(out.status.code(), Some(0));
    assert!(wait_for_port(port), "the router answers on the port");

    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("settings")).expect("json");
    let ca = doc["env"]["NODE_EXTRA_CA_CERTS"].as_str().expect("ca path");
    assert_eq!(
        std::path::Path::new(ca),
        home.root().join(".local/share/huddle/ca/huddle-ca.pem")
    );
    assert!(
        home.root()
            .join(".local/state/huddle/run/router.toml")
            .exists(),
        "the router record is in the state folder"
    );
    assert!(
        !home.root().join(".agents").exists(),
        "nothing under .agents"
    );
}

#[test]
fn a_bad_port_is_refused_before_anything_changes() {
    let home = TempHome::new();
    let out = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["setup", "--install"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", "not-a-port")
        .output()
        .expect("run huddle");
    assert_eq!(out.status.code(), Some(1));
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(stderr.starts_with("huddle setup: "), "{stderr}");
    assert!(stderr.contains("is not a port"), "{stderr}");
    assert!(!home.root().join(".claude/settings.json").exists());
}

#[test]
fn t2_install_writes_config_toml_with_defaults_and_never_rewrites_it() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let config = home.root().join(".config/huddle/config.toml");

    let first = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(first.status.code(), Some(0));
    let text = fs::read_to_string(&config).expect("config.toml");
    assert!(text.contains("keep_days = 90"), "{text}");
    assert!(text.contains('#'), "{text}");

    fs::write(&config, "[history]\nkeep_days = 7\n").expect("edit");
    let second = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(second.status.code(), Some(0));
    assert_eq!(
        fs::read_to_string(&config).expect("read"),
        "[history]\nkeep_days = 7\n"
    );
}
