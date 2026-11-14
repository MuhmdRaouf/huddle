use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::net::TcpListener;
use std::process::{Command, Stdio};

/// A set-up home with Z.ai enabled and no key, so `session-start` has a line to print.
fn set_up_state(home: &TempHome) {
    let huddle_dir = home.root().join(".local/state/huddle");
    fs::create_dir_all(&huddle_dir).expect("mkdir huddle");
    fs::write(huddle_dir.join("settings-ledger.toml"), "{}\n").expect("ledger");
    let config = home.root().join(".config/huddle");
    fs::create_dir_all(&config).expect("mkdir config");
    fs::write(config.join("providers.toml"), "providers = [\"zai\"]\n").expect("providers");
}

/// Runs a hook word with stdout wired to a pipe nobody reads, so every write fails.
fn hook_with_broken_stdout(home: &TempHome, port: u16, word: &str) -> Option<i32> {
    let (reader, writer) = std::io::pipe().expect("pipe");
    drop(reader);
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["hook", word])
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::null())
        .stdout(writer)
        .stderr(Stdio::null())
        .status()
        .expect("run huddle")
        .code()
}

#[test]
fn c2_hook_survives_a_closed_stdout() {
    let home = TempHome::new();
    set_up_state(&home);
    // A listener on the router port keeps the hook from starting a router.
    let port = free_port();
    let _listener = TcpListener::bind(("127.0.0.1", port)).expect("bind");
    assert_eq!(
        hook_with_broken_stdout(&home, port, "session-start"),
        Some(0)
    );
    // A waiting ready note gives `prompt-submit` a report to print, and taking it proves the
    // hook reached the print.
    let note = home.root().join(".local/state/huddle/key-page-zai.ready");
    fs::write(&note, "zai setup: ready.\n").expect("note");
    assert_eq!(
        hook_with_broken_stdout(&home, port, "prompt-submit"),
        Some(0)
    );
    assert!(!note.exists(), "the hook took the note and printed it");
}
