//! The setup side of the key page: starts the helper with a fresh token on its stdin, waits for
//! its record and opens the browser at the one-time link.

use std::path::Path;
use std::process::{Command, Stdio};
use std::thread::sleep;
use std::time::{Duration, Instant};

use huddle_catalog::providers::Provider;
use huddle_config::env::Env;
use huddle_config::files::{DIR_MODE, create_dir};
use huddle_config::paths::Paths;
use huddle_jobs::id::now_ms;
use huddle_procs::detached::{DetachSpec, start_detached_with_input};
use huddle_procs::live::is_alive;

use super::key_page::{random_hex, read_record, record_path};

/// How long the helper has to write its record.
const RECORD_WAIT: Duration = Duration::from_secs(2);

/// How long a helper that is still running gets to leave after the stop signal.
const STOP_WAIT: Duration = Duration::from_millis(500);

/// Stops a helper left from an earlier setup, starts a fresh one and opens its link. The link
/// is returned; none means the helper did not come up.
pub fn open(
    provider: &'static Provider,
    env: &Env,
    paths: &Paths,
    executable: &Path,
) -> Option<String> {
    stop_old(paths, provider);
    let token = random_hex(32).ok()?;
    create_dir(&paths.state_dir, DIR_MODE).ok()?;
    let spec = DetachSpec {
        program: executable.to_path_buf(),
        args: vec![
            "provider".to_owned(),
            provider.id.to_owned(),
            "key-page".to_owned(),
        ],
        env: env.pairs(),
        log_file: None,
        cwd: paths.state_dir.clone(),
    };
    start_detached_with_input(&spec, format!("{token}\n").as_bytes()).ok()?;
    let port = wait_for_port(paths, provider)?;
    let url = format!("http://127.0.0.1:{port}/key/{}/{token}", provider.id);
    browse(env, &url);
    Some(url)
}

/// Signals a helper whose record is still live, then clears the record.
fn stop_old(paths: &Paths, provider: &Provider) {
    if let Some(record) = read_record(paths, provider)
        && record.expires_at_ms > now_ms()
        && is_alive(record.pid)
    {
        // SAFETY: the pid comes from this helper's own record, which is live and unexpired.
        unsafe { libc::kill(record.pid as libc::pid_t, libc::SIGTERM) };
        let until = Instant::now() + STOP_WAIT;
        while is_alive(record.pid) && Instant::now() < until {
            sleep(Duration::from_millis(20));
        }
    }
    let _ = std::fs::remove_file(record_path(paths, provider));
}

fn wait_for_port(paths: &Paths, provider: &Provider) -> Option<u16> {
    let until = Instant::now() + RECORD_WAIT;
    while Instant::now() < until {
        if let Some(record) = read_record(paths, provider)
            && is_alive(record.pid)
        {
            return Some(record.port);
        }
        sleep(Duration::from_millis(20));
    }
    None
}

/// Hands the link to the browser command; a failure to open it is ignored, since the link is
/// printed too. `HUDDLE_BROWSER` replaces `open` / `xdg-open`.
fn browse(env: &Env, url: &str) {
    let default = if cfg!(target_os = "macos") {
        "open"
    } else {
        "xdg-open"
    };
    let program = env
        .get("HUDDLE_BROWSER")
        .filter(|value| !value.is_empty())
        .unwrap_or(default);
    let _ = Command::new(program)
        .arg(url)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn();
}
