//! `huddle provider <p> setup`: the key checked, the provider enabled, the router started and
//! waited on for the provider's lane, the whole state reported as ready or not ready.

use std::io::Write;
use std::time::Duration;

use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::paths::{self, Paths};
use huddle_daemon::start::{StartOptions, router_key_check, start};
use huddle_keystore::port::{SecretString, platform_default};
use huddle_router::dispatch::os_roots;
use huddle_router::keycheck::{Verdict, check_key, ping_model};
use huddle_router::upstream::{IDLE_TIMEOUT, UpstreamClient, UpstreamConfig};
use huddle_setup::env_file::is_router_url;
use huddle_setup::{provider_setup, settings};

const VERSION_FILE: &str = include_str!("../../../../../VERSION");

/// How long a start waits for a spawned daemon's health answer.
const START_BUDGET: Duration = Duration::from_millis(4000);

/// How long the lane check waits for a reload that is still catching up.
const LANE_WAIT: Duration = Duration::from_secs(8);

/// What the report says about Claude Code's settings when no run of the huddle setup word is
/// on record.
const LEDGER_LINE: &str =
    "run /huddle:setup first so Claude Code goes through the router, then restart it";

/// The one line a changed setup adds last: the /model entries are new, so one restart shows them.
const RESTART_LINE: &str = "Restart Claude Code once so /model shows the new models.";

/// Checks the key, enables the provider, starts the router and reports ready or not ready.
/// Exit 0 when ready, 6 when not, 1 when a step refuses outright.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let executable = match std::env::current_exe() {
        Ok(executable) => executable,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let runtime_line = format!("huddle {} ({})", VERSION_FILE.trim(), executable.display());

    let mut lines: Vec<String> = Vec::new();
    let mut changed = false;
    let ready = match key(provider, env, &paths, &executable, &mut lines) {
        Some((key_line, secret)) => {
            lines.push(key_line);
            let (verdict_line, proved) = check_provider_key(provider, env, &secret);
            lines.push(verdict_line);
            if proved {
                match wire(provider, env, &paths, executable, &mut lines, &mut changed) {
                    Ok(ready) => ready,
                    Err(error) => {
                        let _ = writeln!(std::io::stderr(), "{error}");
                        return 1;
                    }
                }
            } else {
                false
            }
        }
        None => false,
    };

    let head = if ready {
        format!("{} setup: ready.", provider.id)
    } else {
        format!("{} setup: not ready.", provider.id)
    };
    let _ = writeln!(std::io::stdout(), "{head}");
    let _ = writeln!(std::io::stdout(), "{runtime_line}");
    for line in &lines {
        let _ = writeln!(std::io::stdout(), "{line}");
    }
    if ready && changed {
        let _ = writeln!(std::io::stdout(), "{RESTART_LINE}");
    }
    if ready { 0 } else { 6 }
}

/// Where the key is: the first key environment variable set and non-blank, else the store.
/// A missing key adds the two lines that name the key word; a store error adds its Display.
/// The line naming where the key was found and the key itself are the return value.
fn key(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &std::path::Path,
    lines: &mut Vec<String>,
) -> Option<(String, SecretString)> {
    let found = provider.key_env.iter().find_map(|name| {
        let value = env.get(name)?;
        (!value.trim().is_empty()).then_some((*name, value.trim().to_owned()))
    });
    if let Some((name, value)) = found {
        return Some((format!("key: {name}"), SecretString::new(value)));
    }
    match platform_default(paths).get(provider.id) {
        Ok(Some(secret)) => Some(("key: stored".to_owned(), secret)),
        Ok(None) => {
            lines.push(format!(
                "no {} key: run this in your shell, then /{}:setup again:",
                provider.display, provider.id
            ));
            lines.push(format!(
                "  \"{}\" provider {} key",
                executable.display(),
                provider.id
            ));
            None
        }
        Err(error) => {
            lines.push(error.to_string());
            None
        }
    }
}

/// One small request to the provider with the key: the answer's status alone decides whether
/// setup goes on. Refused is not ready and enables nothing; the key is never named in a line.
fn check_provider_key(
    provider: &'static Provider,
    env: &Env,
    key: &SecretString,
) -> (String, bool) {
    let client = UpstreamClient::new(UpstreamConfig {
        env: env.clone(),
        roots: os_roots(&|_| {}),
        idle: IDLE_TIMEOUT,
    });
    let model = ping_model(provider);
    let base = check_base_url(provider, env);
    let verdict = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map(|runtime| runtime.block_on(check_key(&base, key.expose(), model, &client)))
        .unwrap_or(Verdict::Unknown);
    let display = provider.display;
    let billing = provider.billing_url;
    match verdict {
        Verdict::Accepted => (format!("key: {display} accepted the key."), true),
        Verdict::Limited => (
            format!(
                "key: {display} knows the key but takes no requests yet (quota or balance): {billing}"
            ),
            true,
        ),
        Verdict::Refused => (
            format!("key: {display} refused the stored key; enter a new one."),
            false,
        ),
        Verdict::Unknown => (
            format!("key: could not reach {display} to check the key; continuing."),
            true,
        ),
    }
}

/// The endpoint the check asks: `<P>_ROUTER_URL`, else `<P>_BASE_URL`, else the catalog's.
fn check_base_url(provider: &Provider, env: &Env) -> String {
    let prefix = provider.env_prefix;
    let router_knob = format!("{prefix}_ROUTER_URL");
    let base_knob = format!("{prefix}_BASE_URL");
    let set = |name: &str| {
        env.get(name)
            .map(str::trim)
            .filter(|value| !value.is_empty())
    };
    set(&router_knob)
        .or_else(|| set(&base_knob))
        .map_or_else(|| provider.base_url.to_owned(), str::to_owned)
}

/// Records the lane, starts the router, waits for it to serve the provider and checks that
/// Claude Code's settings lead to the router. False is a report that is not ready; the error
/// is a step that refuses outright and ends the word before any report.
fn wire(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: std::path::PathBuf,
    lines: &mut Vec<String>,
    changed: &mut bool,
) -> Result<bool, String> {
    let enabled = match provider_setup::enable(paths, provider) {
        Ok(enabled) => enabled,
        Err(error) => return Err(error.to_string()),
    };
    lines.extend(enabled.lines);
    *changed = enabled.changed;

    let port = match paths::router_port(env) {
        Ok(port) => port,
        Err(error) => return Err(error.to_string()),
    };

    let mut ready = true;
    let options = StartOptions {
        env,
        paths,
        port,
        executable,
        budget: START_BUDGET,
    };
    match start(&options) {
        Err(error) => {
            lines.push(format!("the router did not start: {error}"));
            ready = false;
        }
        Ok(_) => match router_key_check(provider.id, provider.display, port, LANE_WAIT) {
            Ok(line) => lines.push(line),
            Err(error) => {
                lines.push(error.to_string());
                ready = false;
            }
        },
    }
    if !settings::ledger_path(paths).exists() {
        lines.push(LEDGER_LINE.to_owned());
        ready = false;
    }
    if let Some(base_url) = settings_base_url(paths)
        && !is_router_url(&base_url, port)
    {
        lines.push(format!(
            "not routed: ANTHROPIC_BASE_URL is {base_url}; the router at \
             http://127.0.0.1:{port} never sees these requests."
        ));
        ready = false;
    }
    Ok(ready)
}

/// The `ANTHROPIC_BASE_URL` settings.json carries, when it is a string. A missing or malformed
/// file reads as none.
fn settings_base_url(paths: &Paths) -> Option<String> {
    let (_, doc) = settings::load_doc(&settings::settings_path(paths)).ok()?;
    doc.get("env")?
        .get("ANTHROPIC_BASE_URL")?
        .as_str()
        .map(str::to_owned)
}
