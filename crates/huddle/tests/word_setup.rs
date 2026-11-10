use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::path::{Path, PathBuf};
use std::process::Output;

fn fake_binary(dir: &Path, name: &str) -> PathBuf {
    let path = dir.join(name);
    write_executable(&path, "#!/bin/sh\necho fake-1.0\n").unwrap();
    path
}

fn huddle(home: &TempHome, args: &[&str], extra: &[(&str, &str)]) -> Output {
    home.command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(home.root())
        .env("PATH", "/usr/bin:/bin")
        .envs(extra.iter().map(|(k, v)| (k.to_string(), v.to_string())))
        .output()
        .expect("run huddle")
}

#[test]
fn setup_with_no_harness_lists_the_table_and_exits_0() {
    let home = TempHome::new();
    let out = huddle(&home, &["setup"], &[]);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("| Harness |"), "{text}");
    assert!(text.contains("opencode"), "{text}");
    assert!(text.contains("omp"), "{text}");
}

#[test]
fn a_ready_harness_reports_ready_and_exits_0() {
    let home = TempHome::new();
    let bin = fake_binary(home.root(), "opencode");
    let out = huddle(
        &home,
        &["setup", "opencode"],
        &[("HUDDLE_OPENCODE_BIN", bin.to_str().unwrap())],
    );
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(out.status.success(), "{text}");
    assert!(text.contains("Status: ready"), "{text}");
    assert!(text.contains("fake-1.0"), "{text}");
}

#[test]
fn a_missing_harness_exits_6_and_says_what_to_do() {
    let home = TempHome::new();
    let out = huddle(
        &home,
        &["setup", "opencode"],
        &[("HUDDLE_OPENCODE_BIN", "/no/such/opencode")],
    );
    assert_eq!(out.status.code(), Some(6));
    let text = String::from_utf8_lossy(&out.stdout);
    assert!(text.contains("Status: needs attention"), "{text}");
}

#[test]
fn an_unknown_harness_exits_2() {
    let home = TempHome::new();
    let out = huddle(&home, &["setup", "nonesuch"], &[]);
    assert_eq!(out.status.code(), Some(2));
    assert_eq!(
        String::from_utf8_lossy(&out.stderr).trim(),
        "huddle: no harness named \"nonesuch\": /huddle:setup lists them"
    );
}

#[test]
fn smoke_needs_an_id() {
    let home = TempHome::new();
    assert_eq!(
        huddle(&home, &["setup", "--smoke"], &[]).status.code(),
        Some(2)
    );
}

#[test]
fn setup_json_is_the_report_object() {
    let home = TempHome::new();
    let bin = fake_binary(home.root(), "opencode");
    let out = huddle(
        &home,
        &["setup", "opencode", "--json"],
        &[("HUDDLE_OPENCODE_BIN", bin.to_str().unwrap())],
    );
    let parsed: serde_json::Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(parsed["id"], "opencode");
    assert_eq!(parsed["ready"], true);
}
