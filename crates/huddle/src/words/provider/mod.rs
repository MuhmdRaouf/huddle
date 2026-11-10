pub mod key;
pub mod remove;
pub mod setup;
pub mod usage;

use std::io::Write;

use huddle_catalog::providers::provider_by_id;
use huddle_config::env::Env;

/// `huddle provider <p> <word> ...`. The env is made once by `main` and passed down.
pub fn run(args: &[String], env: &Env) -> i32 {
    if let Some((name, rest)) = args.split_first()
        && let Some(provider) = provider_by_id(name)
        && let Some((word, rest)) = rest.split_first()
    {
        return match word.as_str() {
            "key" if rest.is_empty() => key::run(provider, env),
            "key" => {
                let _ = writeln!(
                    std::io::stderr(),
                    "key: pass the key on stdin, not as an argument"
                );
                1
            }
            "setup" => setup::run(provider, env),
            "remove" => remove::run(provider, env),
            "usage" => usage::run(provider, env),
            _ => usage(),
        };
    }
    usage()
}

/// The usage line every wrong `huddle provider` call answers.
fn usage() -> i32 {
    let _ = writeln!(
        std::io::stderr(),
        "usage: huddle provider <zai|kimi|deepseek|minimax|qwen> <key|setup|remove|usage>"
    );
    2
}
