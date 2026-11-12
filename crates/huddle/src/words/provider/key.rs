//! `huddle provider <p> key`: the key read from hidden stdin lands in the key store. The key
//! never travels in an argument and never reaches an output line.

use std::io::{self, BufRead, Write};

use huddle_catalog::providers::Provider;
use huddle_config::{env::Env, paths::Paths};
use huddle_keystore::port::{SecretString, is_valid_key, platform_default};

use super::masked::{Echo, Entry, read_entry};

/// Reads, checks and stores the key; every refusal is one line and an exit code.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let key = match read_key(provider.display) {
        Ok(Some(key)) => key,
        Ok(None) => {
            let _ = writeln!(io::stdout(), "Stopped. Nothing was stored.");
            return 130;
        }
        Err(error) => {
            let _ = writeln!(io::stderr(), "key: cannot read stdin ({error})");
            return 1;
        }
    };
    if key.is_empty() {
        let _ = writeln!(io::stderr(), "no key on stdin");
        return 2;
    }
    if !is_valid_key(&key) {
        let _ = writeln!(
            io::stderr(),
            "the key must be 16-512 characters of letters, digits, '.', '_' or '-'"
        );
        return 2;
    }
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(io::stderr(), "{error}");
            return 1;
        }
    };
    if let Err(error) = platform_default(&paths).set(provider.id, &SecretString::new(key)) {
        let _ = writeln!(io::stderr(), "{error}");
        return 1;
    }
    let _ = writeln!(io::stdout(), "{} key saved.", provider.display);
    0
}

/// One line of stdin, trimmed. A terminal gets a masked prompt; None is a Ctrl-C.
fn read_key(display: &str) -> io::Result<Option<String>> {
    if unsafe { libc::isatty(0) } != 1 {
        let mut line = String::new();
        io::stdin().lock().read_line(&mut line)?;
        return Ok(Some(line.trim().to_owned()));
    }
    let prompt = format!("Paste your {display} key, then press Enter: ");
    Ok(match read_entry(&prompt, Echo::Stars) {
        Entry::Line(line) => Some(line.expose().trim().to_owned()),
        Entry::Aborted => None,
    })
}
