//! `huddle provider <p> setup`: finds the key, finishes the setup with it and reports the whole
//! state as ready or not ready.

use std::io::Write;
use std::time::Duration;

use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_keystore::port::{SecretString, platform_default};

use super::finish::finish;
use super::{key_page_launch, ready_note};

/// How long the command waits for the key page. Claude Code ends a command's shell step at
/// 120 s, so the wait stays under it; the page's server goes on by itself after that.
const KEY_WAIT: Duration = Duration::from_secs(100);

/// Test seams: a shorter wait and a shorter page life, in milliseconds.
const WAIT_VAR: &str = "HUDDLE_KEY_WAIT_MS";
const LIFE_VAR: &str = "HUDDLE_KEY_PAGE_LIFE_MS";

fn millis(env: &Env, name: &str) -> Option<Duration> {
    env.get(name)?.parse().ok().map(Duration::from_millis)
}

/// Finds the key, finishes the setup and reports ready or not ready.
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
    let runtime_line = ready_note::runtime_line(&executable);

    let (key_line, secret) = match find_key(provider, env, &paths) {
        Ok(Some(found)) => found,
        Ok(None) => return wait_for_key_page(provider, env, &paths, &executable),
        Err(error) => {
            let _ = writeln!(std::io::stdout(), "{} setup: not ready.", provider.id);
            let _ = writeln!(std::io::stdout(), "{runtime_line}");
            let _ = writeln!(std::io::stdout(), "{error}");
            return 6;
        }
    };
    let outcome = match finish(provider, env, &paths, &executable, &secret) {
        Ok(outcome) => outcome,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };

    let report = ready_note::report(provider, &outcome, &executable, Some(&key_line));
    let _ = write!(std::io::stdout(), "{report}");
    if outcome.ready { 0 } else { 6 }
}

/// No key anywhere: opens the key page and waits for it. The page's server finishes the setup
/// when a key arrives; this command prints that report when it is still here, else the link
/// and a promise that setup finishes by itself (the next prompt then prints the report).
fn wait_for_key_page(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &std::path::Path,
) -> i32 {
    let life = millis(env, LIFE_VAR);
    let Some(url) = key_page_launch::open(provider, env, paths, executable, life) else {
        return needs_a_key(provider, None);
    };
    let wait = millis(env, WAIT_VAR).unwrap_or(KEY_WAIT);
    if !key_page_launch::wait_until_gone(paths, provider, wait) {
        let _ = writeln!(std::io::stdout(), "{url}");
        let _ = writeln!(
            std::io::stdout(),
            "Setup finishes by itself when you save the key. Nothing else to run."
        );
        return 0;
    }
    match ready_note::take(paths, provider) {
        Some(report) => {
            let _ = write!(std::io::stdout(), "{report}");
            if report.starts_with(&ready_note::head(provider, true)) {
                0
            } else {
                6
            }
        }
        None => needs_a_key(provider, Some(&url)),
    }
}

/// The report of a setup that got no key. The link is named only while its page is open.
fn needs_a_key(provider: &Provider, open_link: Option<&str>) -> i32 {
    let _ = writeln!(std::io::stdout(), "{} setup: needs a key.", provider.id);
    if open_link.is_some() {
        let _ = writeln!(std::io::stdout(), "The key page closed without a key.");
    }
    let _ = writeln!(
        std::io::stdout(),
        "Or, in a terminal: huddle provider {}",
        provider.id
    );
    6
}

/// Where the key is: the first key environment variable set and non-blank, else the store.
/// The line naming where the key was found and the key itself are the return value; none means
/// no key anywhere, and a store error is its Display.
fn find_key(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
) -> Result<Option<(String, SecretString)>, String> {
    let found = provider.key_env.iter().find_map(|name| {
        let value = env.get(name)?;
        (!value.trim().is_empty()).then_some((*name, value.trim().to_owned()))
    });
    if let Some((name, value)) = found {
        return Ok(Some((format!("key: {name}"), SecretString::new(value))));
    }
    match platform_default(paths).get(provider.id) {
        Ok(Some(secret)) => Ok(Some(("key: stored".to_owned(), secret))),
        Ok(None) => Ok(None),
        Err(error) => Err(error.to_string()),
    }
}
