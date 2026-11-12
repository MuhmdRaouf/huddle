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
        home.root().join("r/huddle/router.toml").exists(),
        "the router record is in the runtime folder"
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

#[test]
fn t12_install_sets_the_folder_and_file_modes_and_puts_runtime_files_in_the_runtime_dir() {
    use std::os::unix::fs::PermissionsExt;
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);

    let out = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(out.status.code(), Some(0));
    assert!(wait_for_port(port), "the router answers on the port");

    let mode_of = |rel: &str| {
        fs::metadata(home.root().join(rel))
            .unwrap_or_else(|e| panic!("{rel}: {e}"))
            .permissions()
            .mode()
            & 0o777
    };
    for dir in [
        ".config/huddle",
        ".local/share/huddle",
        ".local/state/huddle",
        "r/huddle",
    ] {
        assert_eq!(mode_of(dir), 0o755, "{dir}");
    }
    assert_eq!(mode_of(".local/share/huddle/ca"), 0o700);
    for file in [
        ".config/huddle/config.toml",
        ".config/huddle/providers.toml",
        ".local/state/huddle/huddle.db",
    ] {
        assert_eq!(mode_of(file), 0o644, "{file}");
    }
    for file in [
        ".local/share/huddle/ca/huddle-ca-key.pem",
        ".local/state/huddle/settings-ledger.toml",
    ] {
        assert_eq!(mode_of(file), 0o600, "{file}");
    }
    let run = home.root().join("r/huddle");
    assert!(run.join("router.lock").exists());
    assert!(run.join("handover.sock").exists());
    assert!(!home.root().join(".local/state/huddle/run").exists());
}

fn huddle_on_path(home: &TempHome, port: u16, path: &str, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .env("PATH", path)
        .output()
        .expect("run huddle")
}

fn stable_link(home: &TempHome) -> std::path::PathBuf {
    home.root().join(".local/share/huddle/bin/huddle")
}

fn user_link(home: &TempHome) -> std::path::PathBuf {
    home.root().join(".local/bin/huddle")
}

fn on_path(home: &TempHome) -> String {
    format!("{}:/usr/bin", home.root().join(".local/bin").display())
}

#[test]
fn k4_setup_links_huddle_into_the_user_bin_folder() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);

    let out = huddle_on_path(&home, port, &on_path(&home), &["setup", "--install"]);
    assert_eq!(out.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(stdout.contains("linked ~/.local/bin/huddle"), "{stdout}");
    assert!(!stdout.contains("not on your PATH"), "{stdout}");
    assert_eq!(
        fs::read_link(user_link(&home)).expect("link"),
        stable_link(&home)
    );
}

#[test]
fn k4_a_second_setup_leaves_the_link_alone() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let path = on_path(&home);

    huddle_on_path(&home, port, &path, &["setup", "--install"]);
    let second = huddle_on_path(&home, port, &path, &["setup", "--install"]);
    let stdout = String::from_utf8_lossy(&second.stdout);
    assert!(!stdout.contains("linked"), "{stdout}");
    assert!(!stdout.contains("left as it is"), "{stdout}");
    assert_eq!(
        fs::read_link(user_link(&home)).expect("link"),
        stable_link(&home)
    );
}

#[test]
fn k4_setup_never_replaces_a_file_that_is_not_its_link() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    fs::create_dir_all(home.root().join(".local/bin")).expect("mkdir");
    fs::write(user_link(&home), b"#!/bin/sh\necho mine\n").expect("write");

    let out = huddle_on_path(&home, port, &on_path(&home), &["setup", "--install"]);
    assert_eq!(out.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        stdout.contains("~/.local/bin/huddle exists and is not huddle's link; left as it is."),
        "{stdout}"
    );
    assert!(!stdout.contains("linked ~/.local/bin/huddle"), "{stdout}");
    assert_eq!(
        fs::read(user_link(&home)).expect("read"),
        b"#!/bin/sh\necho mine\n"
    );
}

#[test]
fn k4_setup_says_when_the_user_bin_folder_is_not_on_path() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);

    let out = huddle_on_path(&home, port, "/usr/bin", &["setup", "--install"]);
    let stdout = String::from_utf8_lossy(&out.stdout);
    let expected = format!(
        "~/.local/bin is not on your PATH; run huddle as {}",
        stable_link(&home).display()
    );
    assert!(stdout.contains(&expected), "{stdout}");
}

#[test]
fn setup_goes_on_when_the_user_bin_folder_cannot_be_written() {
    use std::os::unix::fs::PermissionsExt;
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let local = home.root().join(".local");
    fs::create_dir_all(local.join("bin")).expect("bin");
    fs::set_permissions(local.join("bin"), fs::Permissions::from_mode(0o555)).expect("chmod");

    let out = huddle_on_path(&home, port, &on_path(&home), &["setup", "--install"]);
    fs::set_permissions(local.join("bin"), fs::Permissions::from_mode(0o755)).expect("chmod back");

    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        stdout.contains("could not link ~/.local/bin/huddle:"),
        "{stdout}"
    );
}
