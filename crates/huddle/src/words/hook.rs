//! `huddle hook <event>`. Runs at Claude Code's session start. Exits 0 on any failure and never
//! blocks a session.

use std::net::{SocketAddr, TcpStream};
use std::os::unix::process::CommandExt;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::Duration;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_setup::settings::ledger_path;

/// The events wired in hooks.json. Every other word stays a silent no-op: an old session's
/// hooks.json keeps naming words this binary dropped.
const SESSION_START: &str = "session-start";
const PROMPT_SUBMIT: &str = "prompt-submit";
const PROMPT_PROBE_TIMEOUT: Duration = Duration::from_millis(50);

pub fn run(args: &[String], env: &Env) -> i32 {
    let word = args.first().map(String::as_str);
    if (word == Some(SESSION_START) || word == Some(PROMPT_SUBMIT))
        && let (Ok(paths), Ok(exe)) = (Paths::from_env(env), std::env::current_exe())
    {
        if word == Some(SESSION_START) {
            session_start(env, &paths, &exe);
        } else {
            prompt_submit(env, &paths, &exe);
        }
    }
    0
}

/// The router probe alone: this runs on every prompt.
fn prompt_submit(env: &Env, paths: &Paths, exe: &Path) {
    if !ledger_path(paths).exists() {
        return; // setup has not run: change nothing
    }
    let Ok(port) = huddle_config::paths::router_port(env) else {
        return;
    };
    let addr = SocketAddr::from(([127, 0, 0, 1], port));
    if TcpStream::connect_timeout(&addr, PROMPT_PROBE_TIMEOUT).is_err() {
        start_router_detached(exe);
    }
}

fn session_start(env: &Env, paths: &Paths, exe: &Path) {
    if !ledger_path(paths).exists() {
        return; // setup has not run: change nothing
    }
    // Best effort: the agent files are cosmetic, and a hook never fails a session.
    let _ = huddle_setup::agent_files::sync(paths);
    if let Ok(port) = huddle_config::paths::router_port(env) {
        let addr = SocketAddr::from(([127, 0, 0, 1], port));
        if TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_err() {
            start_router_detached(exe);
        }
    }
    let lines = session_providers(env, paths, exe);
    if !lines.is_empty() {
        let message = lines.join("\n");
        if let Ok(json) = serde_json::to_string(&serde_json::json!({ "systemMessage": message })) {
            println!("{json}");
        }
    }
}

/// One pass over the enabled providers: re-applies a plugin update's wiring removal, cleans
/// up after a removed plugin and names each provider whose key is missing. Every failure
/// stays silent: a hook never disturbs the session it observes.
fn session_providers(env: &Env, paths: &Paths, exe: &Path) -> Vec<String> {
    let enabled = huddle_config::providers_file::read_enabled(
        &paths.router_dir,
        &huddle_catalog::providers::provider_ids(),
    )
    .unwrap_or_default();
    let mut lines = Vec::new();
    for id in enabled {
        let Some(provider) = huddle_catalog::providers::provider_by_id(&id) else {
            continue;
        };
        if plugin_absent(paths, provider.id) {
            let _ = Command::new(exe)
                .args(["provider", provider.id, "remove"])
                .output();
            continue;
        }
        let _ = huddle_setup::model_entries::apply(paths, provider);
        if !key_present(env, paths, provider) {
            lines.push(format!(
                "{id}: no {display} key found, so the {id} agents cannot reach {display}. Run {slash}setup to enter it.",
                id = provider.id,
                display = provider.display,
                slash = provider.slash,
            ));
        }
    }
    lines
}

/// True only when installed_plugins.json exists, parses and has no entry naming the
/// provider's plugin. A missing or unparsable file is unknown, and unknown leaves the
/// provider alone; a disabled plugin keeps its entries.
fn plugin_absent(paths: &Paths, id: &str) -> bool {
    let path = paths.claude_dir.join("plugins/installed_plugins.json");
    let Ok(text) = std::fs::read_to_string(path) else {
        return false;
    };
    let Ok(doc) = serde_json::from_str::<serde_json::Value>(&text) else {
        return false;
    };
    let key = format!("{id}-plugin-cc@");
    match doc.get("plugins").and_then(|p| p.as_object()) {
        Some(plugins) => !plugins.keys().any(|k| k.starts_with(&key)),
        None => false,
    }
}

/// The key comes from the environment first, then the keystore.
fn key_present(env: &Env, paths: &Paths, provider: &huddle_catalog::providers::Provider) -> bool {
    if provider
        .key_env
        .iter()
        .any(|name| env.get(name).is_some_and(|v| !v.is_empty()))
    {
        return true;
    }
    matches!(
        huddle_keystore::port::platform_default(paths).get(provider.id),
        Ok(Some(_))
    )
}

fn start_router_detached(exe: &Path) {
    let mut cmd = Command::new(exe);
    cmd.args(["router", "start"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .process_group(0);
    let _ = cmd.spawn();
}
