use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use serde_json::Value;
use std::fs;
use std::path::PathBuf;
use std::process::{Command, Output};

fn plugin_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../plugin")
}

fn read_json(rel: &str) -> Value {
    let text = fs::read_to_string(plugin_dir().join(rel)).expect("read");
    serde_json::from_str(&text).expect("json")
}

fn hook_command() -> String {
    let hooks = read_json("hooks/hooks.json");
    hooks["hooks"]["SessionStart"][0]["hooks"][0]["command"]
        .as_str()
        .expect("command")
        .to_string()
}

fn run_line(line: &str, home: &TempHome, plugin_root: &PathBuf) -> Output {
    Command::new("sh")
        .arg("-c")
        .arg(line)
        .env_clear()
        .envs(home.env())
        .env_remove("HUDDLE_HOME") // the default layout
        .env("PATH", "/usr/bin:/bin")
        .env("CLAUDE_PLUGIN_ROOT", plugin_root)
        .output()
        .expect("run line")
}

#[test]
fn hooks_json_wires_session_start_and_the_prompt_probe() {
    let hooks = read_json("hooks/hooks.json");
    let mut events: Vec<&str> = hooks["hooks"]
        .as_object()
        .expect("object")
        .keys()
        .map(String::as_str)
        .collect();
    events.sort_unstable();
    assert_eq!(events, ["SessionStart", "UserPromptSubmit"]);
    let prompt = &hooks["hooks"]["UserPromptSubmit"][0]["hooks"][0];
    assert_eq!(prompt["timeout"], 5);
    assert!(
        prompt["command"]
            .as_str()
            .expect("command")
            .contains("hook prompt-submit")
    );
    assert!(
        !prompt["command"]
            .as_str()
            .expect("command")
            .contains("dist/run"),
        "no bootstrap on a prompt"
    );
    let line = &hooks["hooks"]["SessionStart"][0];
    assert_eq!(line["matcher"], "startup|resume|clear|compact");
    let entry = &line["hooks"][0];
    assert_eq!(entry["type"], "command");
    assert_eq!(entry["timeout"], 15);
    assert_eq!(
        hook_command(),
        "h=\"${HUDDLE_HOME:-$HOME/.agents/huddle}/bin/huddle\"; [ -x \"$h\" ] && exec \"$h\" hook session-start; exec sh \"${CLAUDE_PLUGIN_ROOT}/dist/run\" hook session-start"
    );
}

#[test]
fn the_plugin_manifest_names_huddle_and_carries_no_version() {
    let manifest = read_json(".claude-plugin/plugin.json");
    assert_eq!(manifest["name"], "huddle");
    assert!(manifest.get("version").is_none());
    assert_eq!(manifest["license"], "GPL-3.0-or-later");
    let description = manifest["description"].as_str().expect("description");
    assert!(description.contains("providers"));
    assert!(description.contains("delegate"));
}

#[test]
fn the_hook_line_with_no_binary_exits_zero_silently_and_starts_nothing() {
    let home = TempHome::new();

    let out = run_line(&hook_command(), &home, &plugin_dir());

    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(String::from_utf8_lossy(&out.stdout), "");
    assert_eq!(String::from_utf8_lossy(&out.stderr), "");
    assert!(!home.root().join(".agents/huddle/bin").exists());
}

#[test]
fn the_hook_line_runs_the_installed_binary_even_after_its_plugin_directory_is_gone() {
    let home = TempHome::new();
    let bin_dir = home.root().join(".agents/huddle/bin");
    fs::create_dir_all(&bin_dir).expect("mkdir");
    let fake = bin_dir.join("huddle");
    write_executable(&fake, "#!/bin/sh\necho \"huddle ran $*\"\n").expect("write");
    let gone = home.root().join("plugin-dir-that-was-deleted");

    let out = run_line(&hook_command(), &home, &gone);

    assert_eq!(out.status.code(), Some(0));
    assert_eq!(
        String::from_utf8_lossy(&out.stdout),
        "huddle ran hook session-start\n"
    );
}
