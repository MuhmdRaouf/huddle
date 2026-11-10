use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    io::Write,
    os::unix::fs::PermissionsExt,
    process::{Command, Output, Stdio},
};

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

#[test]
fn a_positional_argument_after_key_is_refused() {
    let home = TempHome::new();
    let output = huddle(&home, &["provider", "zai", "key", "abc"], "");
    assert_eq!(output.status.code(), Some(1));
    assert_eq!(
        String::from_utf8_lossy(&output.stderr),
        "key: pass the key on stdin, not as an argument\n"
    );
    assert!(!home.root().join(".config/zai-plugin-cc/env").exists());
}

#[test]
fn the_key_word_stores_a_key_read_from_stdin() {
    let home = TempHome::new();
    let output = huddle(&home, &["provider", "zai", "key"], "abcdefghijklmnop\n");
    assert_eq!(output.status.code(), Some(0), "{output:?}");
    assert_eq!(
        String::from_utf8_lossy(&output.stdout),
        "Z.ai GLM key saved.\n"
    );
    let path = home.root().join(".config/zai-plugin-cc/env");
    assert_eq!(
        fs::read_to_string(&path).expect("read"),
        "ZAI_API_KEY=abcdefghijklmnop\n"
    );
    assert_eq!(
        fs::metadata(&path).expect("stat").permissions().mode() & 0o777,
        0o600
    );
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        !text.contains("abcdefghijklmnop"),
        "the key must never be echoed: {text}"
    );
}

#[test]
fn a_key_of_the_wrong_shape_is_refused() {
    let home = TempHome::new();
    let output = huddle(&home, &["provider", "kimi", "key"], "short\n");
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(
        String::from_utf8_lossy(&output.stderr),
        "the key must be 16-512 characters of letters, digits, '.', '_' or '-'\n"
    );
    assert!(!home.root().join(".config/kimi-plugin-cc/env").exists());
}

#[test]
fn an_empty_key_is_refused() {
    let home = TempHome::new();
    let output = huddle(&home, &["provider", "zai", "key"], "\n");
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(String::from_utf8_lossy(&output.stderr), "no key on stdin\n");
}

#[test]
fn an_unknown_provider_is_a_usage_error() {
    let home = TempHome::new();
    let output = huddle(&home, &["provider", "nobody", "key"], "");
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(
        String::from_utf8_lossy(&output.stderr),
        "usage: huddle provider <zai|kimi|deepseek|minimax|qwen> <key|setup|remove|usage>\n"
    );
}

#[test]
fn a_missing_word_is_a_usage_error() {
    let home = TempHome::new();
    assert_eq!(
        huddle(&home, &["provider", "zai"], "").status.code(),
        Some(2)
    );
}
