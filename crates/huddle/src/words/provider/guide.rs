//! `huddle provider <p>` with no action: in a terminal it asks for the key and finishes the
//! setup, or shows the state and a menu; off a terminal it prints the next steps and reads
//! nothing.

use std::io::Write;

use huddle_catalog::providers::{Provider, providers};
use huddle_config::{env::Env, paths::Paths, providers_file::read_enabled};
use huddle_keystore::port::{SecretString, is_valid_key, platform_default, platform_parts};

use super::finish::{KeyVerdict, Outcome, StepName, StepState, store_and_finish};
use super::masked::{Echo, Entry, read_entry};
use super::{remove, usage};

/// Refused keys in a row before the guide gives up.
const TRIES: usize = 3;

const PROMPT: &str = "Paste your key, then press Enter: ";

/// Where the provider's key comes from.
enum KeySource {
    Stored,
    Environment(&'static str),
    Missing,
}

/// Runs the guide. Exit 0 when done or off a terminal with a key, 6 when a key is still
/// missing or the setup is not ready, 130 after Ctrl-C, 1 when a step refuses outright.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let source = key_source(provider, env, &paths);
    if !on_terminal() {
        return off_terminal(provider, &paths, &source);
    }
    match source {
        KeySource::Missing => {
            out(&format!(
                "{} has no key yet.\nGet one at {}\n",
                provider.display, provider.keys_url
            ));
            ask_and_finish(provider, env, &paths)
        }
        source => menu(provider, env, &paths, &source),
    }
}

fn on_terminal() -> bool {
    unsafe { libc::isatty(0) == 1 && libc::isatty(1) == 1 }
}

fn out(text: &str) {
    let mut stdout = std::io::stdout();
    let _ = writeln!(stdout, "{text}");
    let _ = stdout.flush();
}

fn key_source(provider: &'static Provider, env: &Env, paths: &Paths) -> KeySource {
    if matches!(platform_default(paths).get(provider.id), Ok(Some(_))) {
        return KeySource::Stored;
    }
    provider
        .key_env
        .iter()
        .find(|name| env.get(name).is_some_and(|value| !value.trim().is_empty()))
        .map_or(KeySource::Missing, |name| KeySource::Environment(name))
}

/// The one-line state: `<Display>  key stored · on`.
fn state_line(provider: &Provider, paths: &Paths, source: &KeySource) -> String {
    let ids: Vec<&str> = providers().iter().map(|provider| provider.id).collect();
    let on = read_enabled(&paths.config_dir, &ids)
        .unwrap_or_default()
        .iter()
        .any(|name| name == provider.id);
    let key = match source {
        KeySource::Environment(name) => format!("key from {name}"),
        _ => "key stored".to_owned(),
    };
    format!(
        "{}  {key} · {}",
        provider.display,
        if on { "on" } else { "off" }
    )
}

fn off_terminal(provider: &Provider, paths: &Paths, source: &KeySource) -> i32 {
    let id = provider.id;
    if matches!(source, KeySource::Missing) {
        out(&format!(
            "{id} setup: needs a key.\nIn Claude Code: /huddle:{id}:setup opens a key page in your browser.\nIn a terminal: huddle provider {id}"
        ));
        return 6;
    }
    out(&format!(
        "{}\n\n  huddle provider {id} usage    tokens and cost\n  huddle provider {id} remove   turn {id} off",
        state_line(provider, paths, source)
    ));
    0
}

fn menu(provider: &'static Provider, env: &Env, paths: &Paths, source: &KeySource) -> i32 {
    let id = provider.id;
    out(&format!(
        "{}\n\n  1  Replace the key\n  2  Usage: tokens and cost\n  3  Remove {id}: key, routing, /model entries, agents\n  4  Quit\n",
        state_line(provider, paths, source)
    ));
    loop {
        let choice = match read_entry("Choose 1-4: ", Echo::Text) {
            Entry::Line(line) => line.expose().trim().to_owned(),
            Entry::Aborted => return 0,
        };
        match choice.as_str() {
            "" | "4" => return 0,
            "1" => return ask_and_finish(provider, env, paths),
            "2" => return usage::run(provider, env),
            "3" => return confirm_remove(provider, env),
            _ => {}
        }
    }
}

fn confirm_remove(provider: &'static Provider, env: &Env) -> i32 {
    let prompt = format!("Remove {}? Type y to confirm: ", provider.id);
    match read_entry(&prompt, Echo::Text) {
        Entry::Line(answer) if answer.expose().trim() == "y" => remove::run(provider, env),
        _ => 0,
    }
}

/// Asks for the key up to `TRIES` times, checks and stores it, and prints the result.
fn ask_and_finish(provider: &'static Provider, env: &Env, paths: &Paths) -> i32 {
    let executable = match std::env::current_exe() {
        Ok(executable) => executable,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let mut refused = 0;
    while refused < TRIES {
        let key = match read_entry(PROMPT, Echo::Stars) {
            Entry::Aborted => {
                out("Stopped. Nothing was stored.");
                return 130;
            }
            Entry::Line(line) if line.expose().trim().is_empty() => continue,
            Entry::Line(line) => SecretString::new(line.expose().trim().to_owned()),
        };
        if !is_valid_key(key.expose()) {
            refused += 1;
            out(&format!(
                "✗ That is not a key: expect 16-512 letters, digits, '.', '_' or '-'. Create one at {}.",
                provider.keys_url
            ));
            continue;
        }
        match store_and_finish(provider, env, paths, &executable, key) {
            Ok(outcome) if outcome.verdict == KeyVerdict::Refused => {
                refused += 1;
                out(&format!(
                    "✗ {} refused this key. Check that you copied all of it, or create a new one at {}.",
                    provider.display, provider.keys_url
                ));
            }
            Ok(outcome) => return report(provider, paths, &outcome),
            Err(error) => {
                let _ = writeln!(std::io::stderr(), "{error}");
                return 1;
            }
        }
    }
    6
}

/// Prints the steps of a finished setup. Exit 0 when ready, 6 when not.
fn report(provider: &Provider, paths: &Paths, outcome: &Outcome) -> i32 {
    out(&render(provider, store_name(paths), outcome).join("\n"));
    if outcome.ready { 0 } else { 6 }
}

/// Where a stored key lands: the OS store when one answers, else the key store file.
fn store_name(paths: &Paths) -> &'static str {
    let os = platform_parts(paths).0;
    match os {
        Some(store) if store.available() && cfg!(target_os = "macos") => "the Keychain",
        _ => "the key store",
    }
}

/// The lines of a finished setup, from the outcome's steps.
fn render(provider: &Provider, store: &str, outcome: &Outcome) -> Vec<String> {
    let mut lines = Vec::new();
    let id = provider.id;
    if state_of(outcome, StepName::KeyStored) == Some(StepState::Done) {
        lines.push(format!("✓ Stored in {store}"));
    }
    lines.push(match outcome.verdict {
        KeyVerdict::Accepted => format!("✓ {} accepted the key", provider.display),
        KeyVerdict::Limited => format!(
            "! {} knows the key but takes no requests yet (quota or balance): {}",
            provider.display, provider.billing_url
        ),
        KeyVerdict::Unknown => format!(
            "! {} didn't answer; the key is stored and {id} is on.",
            provider.display
        ),
        KeyVerdict::Refused => format!("✗ {} refused this key", provider.display),
    });
    match state_of(outcome, StepName::RouterRoutes) {
        Some(StepState::Done) => lines.push(format!("✓ huddle-router routes {id}")),
        Some(_) => {
            if let Some(step) = outcome.step(StepName::RouterRoutes) {
                lines.extend(step.lines.iter().map(|line| format!("✗ {line}")));
            }
        }
        None => {}
    }
    if state_of(outcome, StepName::ModelEntries) == Some(StepState::Done) {
        lines.push(format!(
            "✓ /model: {}, {}",
            provider.main.id, provider.flash.id
        ));
    }
    lines.push(String::new());
    lines.push(if outcome.ready {
        format!(
            "{id} is ready. In Claude Code, pick a {} model with /model.",
            family(provider)
        )
    } else {
        format!("{id} is not ready.")
    });
    lines
}

fn state_of(outcome: &Outcome, name: StepName) -> Option<StepState> {
    outcome.step(name).map(|step| step.state)
}

/// The model family a user picks in `/model`: the first word of the main model's label.
fn family(provider: &Provider) -> &str {
    provider
        .main
        .label
        .split_whitespace()
        .next()
        .unwrap_or(provider.display)
}
