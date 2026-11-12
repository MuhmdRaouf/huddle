//! `huddle provider <p> setup`: finds the key, finishes the setup with it and reports the whole
//! state as ready or not ready.

use std::io::Write;

use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_keystore::port::{SecretString, platform_default};

use super::finish::{finish, render_lines};

const VERSION_FILE: &str = include_str!("../../../../../VERSION");

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
    let runtime_line = format!("huddle {} ({})", VERSION_FILE.trim(), executable.display());

    let (key_line, secret) = match find_key(provider, env, &paths) {
        Ok(Some(found)) => found,
        Ok(None) => {
            let _ = writeln!(std::io::stdout(), "{} setup: needs a key.", provider.id);
            let _ = writeln!(
                std::io::stdout(),
                "In a terminal: huddle provider {}",
                provider.id
            );
            return 6;
        }
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

    let head = if outcome.ready {
        format!("{} setup: ready.", provider.id)
    } else {
        format!("{} setup: not ready.", provider.id)
    };
    let _ = writeln!(std::io::stdout(), "{head}");
    let _ = writeln!(std::io::stdout(), "{runtime_line}");
    let _ = writeln!(std::io::stdout(), "{key_line}");
    for line in render_lines(&outcome, provider) {
        let _ = writeln!(std::io::stdout(), "{line}");
    }
    if outcome.ready { 0 } else { 6 }
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
