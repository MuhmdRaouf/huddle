//! `huddle provider <p> key`: the key read from hidden stdin lands in the key store. The key
//! never travels in an argument and never reaches an output line.

use std::io::{self, BufRead, Write};

use huddle_catalog::providers::Provider;
use huddle_config::{env::Env, paths::Paths};
use huddle_keystore::port::{SecretString, is_valid_key, platform_default};

/// Reads, checks and stores the key; every refusal is one line and an exit code.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let key = match read_key(provider.display) {
        Ok(key) => key,
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

/// One line of stdin, trimmed. A terminal gets a prompt with the echo off, and the echo comes
/// back in every path.
fn read_key(display: &str) -> io::Result<String> {
    let mut line = String::new();
    if unsafe { libc::isatty(0) } != 1 {
        io::stdin().lock().read_line(&mut line)?;
        return Ok(line.trim().to_owned());
    }
    let _ = write!(io::stderr(), "Paste your {display} key and press enter: ");
    let _ = io::stderr().flush();
    let mut state = unsafe { std::mem::zeroed::<libc::termios>() };
    if unsafe { libc::tcgetattr(0, &mut state) } == 0 {
        let flags = state.c_lflag;
        state.c_lflag = flags & !(libc::ECHO as libc::tcflag_t);
        unsafe { libc::tcsetattr(0, libc::TCSANOW, &state) };
        let read = io::stdin().lock().read_line(&mut line);
        state.c_lflag = flags;
        unsafe { libc::tcsetattr(0, libc::TCSANOW, &state) };
        let _ = writeln!(io::stderr());
        read?;
    } else {
        io::stdin().lock().read_line(&mut line)?;
    }
    Ok(line.trim().to_owned())
}
