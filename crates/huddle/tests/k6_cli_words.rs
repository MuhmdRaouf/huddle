//! K6: bare `huddle`, `huddle help`, and the words that never echo what was typed.

use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    io::Write,
    process::{Command, Output, Stdio},
};

const FAKE_KEY: &str = "sk-FAKE-123456";

fn huddle(home: &TempHome, args: &[&str], stdin: &str) -> Output {
    let mut child = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn");
    child
        .stdin
        .take()
        .expect("stdin")
        .write_all(stdin.as_bytes())
        .expect("write");
    child.wait_with_output().expect("wait")
}

fn out(output: &Output) -> String {
    String::from_utf8_lossy(&output.stdout).into_owned()
}

fn err(output: &Output) -> String {
    String::from_utf8_lossy(&output.stderr).into_owned()
}

fn mark_installed(home: &TempHome) {
    let state = home.root().join(".local/state/huddle");
    fs::create_dir_all(&state).expect("state");
    fs::write(state.join("settings-ledger.toml"), "").expect("ledger");
}

fn enable(home: &TempHome, provider: &str) {
    let config = home.root().join(".config/huddle");
    fs::create_dir_all(&config).expect("config");
    fs::write(
        config.join("providers.toml"),
        format!("providers = [\"{provider}\"]\n"),
    )
    .expect("providers.toml");
}

const PROVIDER_ROWS_NO_KEYS: &str = "\
Providers
  zai       Z.ai GLM        no key
  kimi      Moonshot Kimi   no key
  deepseek  DeepSeek        no key
  minimax   MiniMax         no key
  qwen      Alibaba Qwen    no key
";

#[test]
fn k6_bare_huddle_lists_router_providers_and_next_steps() {
    let home = TempHome::new();
    mark_installed(&home);
    let output = huddle(&home, &[], "");
    assert_eq!(output.status.code(), Some(0), "{output:?}");
    let version =
        fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/../../VERSION")).expect("VERSION");
    assert_eq!(
        out(&output),
        format!(
            "huddle {}  router stopped\n\n{PROVIDER_ROWS_NO_KEYS}\n\
Next\n  \
huddle provider zai    add a provider; asks for its key\n  \
huddle status          delegated jobs\n  \
huddle help            every command\n",
            version.trim()
        )
    );
    assert_eq!(err(&output), "");

    let stored = huddle(&home, &["provider", "zai", "key"], "abcdefghijklmnop\n");
    assert_eq!(stored.status.code(), Some(0), "{stored:?}");
    enable(&home, "zai");
    let output = huddle(&home, &[], "");
    let text = out(&output);
    assert!(
        text.contains("  zai       Z.ai GLM        key stored · on\n"),
        "{text}"
    );
    assert!(
        text.contains("  kimi      Moonshot Kimi   no key\n"),
        "{text}"
    );
    assert!(
        text.contains("  huddle provider kimi   add a provider; asks for its key\n"),
        "{text}"
    );
}

#[test]
fn k6_bare_huddle_names_the_key_stored_and_off_state() {
    let home = TempHome::new();
    mark_installed(&home);
    huddle(&home, &["provider", "zai", "key"], "abcdefghijklmnop\n");
    let text = out(&huddle(&home, &[], ""));
    assert!(
        text.contains("  zai       Z.ai GLM        key stored · off\n"),
        "{text}"
    );
}

#[test]
fn k6_bare_huddle_before_setup_points_at_the_plugin_command() {
    let home = TempHome::new();
    let text = out(&huddle(&home, &[], ""));
    assert!(text.contains(PROVIDER_ROWS_NO_KEYS), "{text}");
    assert!(
        text.contains("Next\n  /huddle:setup in Claude Code   install huddle\n"),
        "{text}"
    );
}

#[test]
fn k6_bare_huddle_with_every_key_stored_offers_to_change_one() {
    let home = TempHome::new();
    mark_installed(&home);
    for provider in ["zai", "kimi", "deepseek", "minimax", "qwen"] {
        let stored = huddle(&home, &["provider", provider, "key"], "abcdefghijklmnop\n");
        assert_eq!(stored.status.code(), Some(0), "{stored:?}");
    }
    let text = out(&huddle(&home, &[], ""));
    assert!(
        text.contains("  huddle provider zai    change a provider's key or remove it\n"),
        "{text}"
    );
}

#[test]
fn k6_help_lists_every_word_but_hook() {
    let home = TempHome::new();
    for flag in ["help", "-h", "--help"] {
        let output = huddle(&home, &[flag], "");
        assert_eq!(output.status.code(), Some(0), "{flag}");
        let text = out(&output);
        for group in ["Setup", "Providers", "Delegation", "Router"] {
            assert!(text.lines().any(|line| line == group), "{group}: {text}");
        }
        for word in [
            "setup",
            "uninstall",
            "version",
            "provider",
            "run",
            "status",
            "result",
            "cancel",
            "router",
            "add-dashboard-host",
            "remove-dashboard-host",
        ] {
            assert!(
                text.lines().any(|line| {
                    line.strip_prefix("  ")
                        .and_then(|rest| rest.strip_prefix(word))
                        .is_some_and(|rest| rest.starts_with(' '))
                }),
                "{word}: {text}"
            );
        }
        assert!(!text.contains("hook"), "{text}");
    }
}

#[test]
fn k6_an_unknown_provider_action_is_never_echoed() {
    let home = TempHome::new();
    let expected = "\
huddle provider zai: unknown action (not shown, in case it is a key).
huddle never takes a key on the command line.

  huddle provider zai          set zai up; asks for the key, hidden
  huddle provider zai usage    tokens and cost
  huddle provider zai remove   turn zai off

If you pasted a key, delete that line from your shell history.
";
    for args in [
        vec!["provider", "zai", FAKE_KEY],
        vec!["provider", "zai", "key", FAKE_KEY],
    ] {
        let output = huddle(&home, &args, "");
        assert_eq!(output.status.code(), Some(2), "{args:?}");
        assert_eq!(err(&output), expected);
        assert!(!out(&output).contains(FAKE_KEY));
        assert!(!home.root().join(".config/zai-plugin-cc/env").exists());
    }
    let output = huddle(&home, &["provider", FAKE_KEY], "");
    assert_eq!(output.status.code(), Some(2));
    assert!(!out(&output).contains(FAKE_KEY) && !err(&output).contains(FAKE_KEY));
    assert!(err(&output).contains("Providers\n  zai       Z.ai GLM"));

    let output = huddle(&home, &[FAKE_KEY], "");
    assert_eq!(output.status.code(), Some(2));
    assert!(!out(&output).contains(FAKE_KEY) && !err(&output).contains(FAKE_KEY));
    assert!(err(&output).starts_with("huddle: unknown word\n"));
    assert!(err(&output).contains("\nSetup\n"));
}

#[test]
fn k6_provider_alone_shows_the_providers_block() {
    let home = TempHome::new();
    let output = huddle(&home, &["provider"], "");
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(
        out(&output),
        format!("{PROVIDER_ROWS_NO_KEYS}\n  huddle provider <name>   set it up or change it\n")
    );
}

#[test]
fn k6_output_is_plain_without_a_tty() {
    let home = TempHome::new();
    for args in [vec![], vec!["help"], vec!["provider"]] {
        let output = huddle(&home, &args, "");
        assert!(!output.stdout.contains(&0x1b), "{args:?}");
    }
}
