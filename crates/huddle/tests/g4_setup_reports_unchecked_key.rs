//! G4: when the provider could not be asked about the key, setup says `ready, key not checked`
//! and names the way to check the key later. Real binary, temp home, real router on a loopback
//! port, a base URL that refuses connections.

use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::net::TcpListener;
use std::process::{Command, Stdio};

const KEY: &str = "g4-test-key-0123456789abcdef";

struct World {
    home: TempHome,
    extra: Vec<(String, String)>,
}

impl World {
    fn new(base_url: &str) -> World {
        let home = TempHome::new();
        // /huddle:setup's ledger: any file at the path counts for the setup word.
        let ledger = home.root().join(".local/state/huddle/settings-ledger.toml");
        fs::create_dir_all(ledger.parent().expect("parent")).expect("mkdir");
        fs::write(&ledger, "{}").expect("ledger");
        let extra = vec![
            ("ZAI_API_KEY".to_owned(), KEY.to_owned()),
            ("ZAI_BASE_URL".to_owned(), base_url.to_owned()),
        ];
        World { home, extra }
    }

    fn command(&self) -> Command {
        let mut command = self.home.command(env!("CARGO_BIN_EXE_huddle"));
        command.envs(self.extra.iter().map(|(k, v)| (k, v)));
        command
    }
}

impl Drop for World {
    fn drop(&mut self) {
        let _ = self
            .command()
            .args(["router", "stop"])
            .stdin(Stdio::null())
            .output();
    }
}

/// A loopback port that refuses connections: bound, then released.
fn dead_base_url() -> String {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    drop(listener);
    format!("http://127.0.0.1:{port}")
}

#[test]
fn g4_unreachable_provider_says_key_not_checked() {
    let world = World::new(&dead_base_url());
    let out = world
        .command()
        .args(["provider", "zai", "setup"])
        .stdin(Stdio::null())
        .output()
        .expect("run");
    let stdout = String::from_utf8_lossy(&out.stdout).into_owned();
    assert_eq!(out.status.code(), Some(0), "{stdout}");
    assert!(
        stdout.starts_with("zai setup: ready, key not checked.\n"),
        "{stdout}"
    );
    assert!(
        stdout.contains("Run /huddle:zai:setup again when you are online to check the key.\n"),
        "{stdout}"
    );
    assert!(
        stdout.contains("key: could not reach Z.ai GLM to check the key; continuing."),
        "{stdout}"
    );
    assert!(stdout.contains("huddle-router serves Z.ai GLM"), "{stdout}");
    assert!(
        !stdout.contains(KEY),
        "the key never reaches the report: {stdout}"
    );
    let lane =
        fs::read_to_string(world.home.root().join(".config/huddle/providers.toml")).expect("lane");
    assert!(lane.contains("zai"), "{lane}");
}
