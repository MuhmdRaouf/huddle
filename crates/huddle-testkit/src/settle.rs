//! Waits for the short-lived helpers a test caused, such as the detached `router start` a hook
//! spawns, so a test ends as soon as they do instead of after a fixed sleep.

use std::path::Path;
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};

const POLL: Duration = Duration::from_millis(50);

/// Waits up to `within` until no process started with this home (its environment names
/// `HOME=<home>`) shows `needle` in its command line. Answers whether they all ended.
pub fn wait_gone(home: &Path, needle: &str, within: Duration) -> bool {
    let home_var = format!("HOME={}", home.display());
    let deadline = Instant::now() + within;
    loop {
        if !any_running(&home_var, needle) {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        thread::sleep(POLL);
    }
}

// A failed look at the process table counts as one still running, so the wait never ends early.
fn any_running(home_var: &str, needle: &str) -> bool {
    let Ok(listing) = Command::new("ps")
        .args(["axeww", "-o", "pid=,command="])
        .output()
    else {
        return true;
    };
    if !listing.status.success() {
        return true;
    }
    String::from_utf8_lossy(&listing.stdout)
        .lines()
        .any(|line| {
            line.contains(needle)
                && line.match_indices(home_var).any(|(at, _)| {
                    line[at + home_var.len()..]
                        .chars()
                        .next()
                        .is_none_or(char::is_whitespace)
                })
        })
}
