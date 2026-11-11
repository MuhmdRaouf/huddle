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

fn port_open(port: u16) -> bool {
    let addr: SocketAddr = format!("127.0.0.1:{port}").parse().expect("addr");
    TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok()
}

fn wait_closed(port: u16) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if !port_open(port) {
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
fn uninstall_restores_settings_byte_for_byte_and_stops_the_router() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let claude_dir = home.root().join(".claude");
    fs::create_dir_all(&claude_dir).expect("mkdir");
    let settings = claude_dir.join("settings.json");
    let original = "{\n      \"theme\":   \"dark\"\n}\n";
    fs::write(&settings, original).expect("write");

    let install = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        install.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&install.stderr)
    );
    assert_ne!(fs::read_to_string(&settings).expect("read"), original);

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(stdout.contains("restored settings.json"), "{stdout}");
    assert_eq!(fs::read_to_string(&settings).expect("read"), original);
    assert!(wait_closed(port), "the router stopped");
    assert!(
        !home
            .root()
            .join(".local/state/huddle/settings-ledger.toml")
            .exists()
    );
}

#[test]
fn uninstall_twice_says_nothing_to_undo_and_exits_zero() {
    let home = TempHome::new();
    let port = free_port();

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        stdout.contains("nothing to undo in settings.json"),
        "{stdout}"
    );
}

#[test]
fn t2_install_leaves_toml_ledgers_and_uninstall_restores_the_bytes() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let claude_dir = home.root().join(".claude");
    fs::create_dir_all(&claude_dir).expect("mkdir");
    let settings = claude_dir.join("settings.json");
    let original =
        "{\n  \"env\": {\"HTTPS_PROXY\": \"http://corp:3128\"},\n  \"theme\":   \"dark\"\n}\n";
    fs::write(&settings, original).expect("write");
    let state = home.root().join(".local/state/huddle");

    let install = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(install.status.code(), Some(0));
    assert!(state.join("settings-ledger.toml").exists());
    assert!(!state.join("settings-ledger.json").exists());
    assert!(state.join("settings.json.pristine").exists());

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(fs::read_to_string(&settings).expect("read"), original);
    assert!(!state.join("settings-ledger.toml").exists());
}
