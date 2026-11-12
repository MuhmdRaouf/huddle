pub mod finish;
pub mod key;
pub mod key_page;
pub mod key_page_launch;
pub mod remove;
pub mod setup;
pub mod usage;

use std::io::Write;

use huddle_catalog::providers::provider_by_id;
use huddle_config::env::Env;

use super::home;

/// `huddle provider [<p> [<word> ...]]`. The env is made once by `main` and passed down.
pub fn run(args: &[String], env: &Env) -> i32 {
    let Some((name, rest)) = args.split_first() else {
        return home::run_providers(env);
    };
    let Some(provider) = provider_by_id(name) else {
        return home::run_unknown_provider(env);
    };
    let Some((word, rest)) = rest.split_first() else {
        return usage();
    };
    match word.as_str() {
        "key" if rest.is_empty() => key::run(provider, env),
        "key-page" if rest.is_empty() => key_page::run(provider, env),
        "setup" => setup::run(provider, env),
        "remove" => remove::run(provider, env),
        "usage" => usage::run(provider, env),
        _ => unknown_action(provider.id),
    }
}

/// The usage line of `huddle provider <p>` with no action.
fn usage() -> i32 {
    let _ = writeln!(
        std::io::stderr(),
        "usage: huddle provider <zai|kimi|deepseek|minimax|qwen> <key|setup|remove|usage>"
    );
    2
}

/// An action huddle does not know, or `key` with an argument. The argument is never printed,
/// since it may be a key.
fn unknown_action(provider: &str) -> i32 {
    let _ = write!(
        std::io::stderr(),
        "huddle provider {provider}: unknown action (not shown, in case it is a key).
huddle never takes a key on the command line.

  huddle provider {provider}          set {provider} up; asks for the key, hidden
  huddle provider {provider} usage    tokens and cost
  huddle provider {provider} remove   turn {provider} off

If you pasted a key, delete that line from your shell history.
"
    );
    2
}
