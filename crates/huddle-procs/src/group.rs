//! Process groups: signalling every member of a group a streamed child leads, terminating it,
//! and the signal names an exit reports.

use std::io;
use std::thread;
use std::time::{Duration, Instant};

/// How often a terminating group is probed for its last member.
const POLL_INTERVAL: Duration = Duration::from_millis(25);

/// After SIGKILL only the kernel is left to act; this bounds the wait for a member stuck in an
/// uninterruptible call.
const KILL_SETTLE: Duration = Duration::from_millis(2000);

/// The signals an exit can name, in Node's spelling.
const SIGNAL_NAMES: &[(i32, &str)] = &[
    (libc::SIGHUP, "SIGHUP"),
    (libc::SIGINT, "SIGINT"),
    (libc::SIGQUIT, "SIGQUIT"),
    (libc::SIGILL, "SIGILL"),
    (libc::SIGTRAP, "SIGTRAP"),
    (libc::SIGABRT, "SIGABRT"),
    (libc::SIGBUS, "SIGBUS"),
    (libc::SIGFPE, "SIGFPE"),
    (libc::SIGKILL, "SIGKILL"),
    (libc::SIGUSR1, "SIGUSR1"),
    (libc::SIGSEGV, "SIGSEGV"),
    (libc::SIGUSR2, "SIGUSR2"),
    (libc::SIGPIPE, "SIGPIPE"),
    (libc::SIGALRM, "SIGALRM"),
    (libc::SIGTERM, "SIGTERM"),
    (libc::SIGCHLD, "SIGCHLD"),
    (libc::SIGCONT, "SIGCONT"),
    (libc::SIGSTOP, "SIGSTOP"),
    (libc::SIGTSTP, "SIGTSTP"),
    (libc::SIGTTIN, "SIGTTIN"),
    (libc::SIGTTOU, "SIGTTOU"),
];

/// The signal's Node spelling; "SIG<n>" for one Node does not name.
pub fn signal_name(signal: i32) -> String {
    match SIGNAL_NAMES.iter().find(|(number, _)| *number == signal) {
        Some((_, name)) => (*name).to_string(),
        None => format!("SIG{signal}"),
    }
}

/// kill treats 0 and negatives as process groups (-1: every process we may signal), and our own
/// group would take this process down with it, so only another group leader's pid is safe.
fn leads_a_group(pid: u32) -> bool {
    pid > 1 && pid != std::process::id()
}

/// Signals the group `pid` leads. Refuses pid 0, 1 and our own pid; true when the group had a
/// member to signal (EPERM counts: the member exists but belongs to someone else).
pub fn signal_group(pid: u32, signal: i32) -> bool {
    if !leads_a_group(pid) {
        return false;
    }
    // SAFETY: kill only delivers the given signal to the named group.
    let rc = unsafe { libc::kill(-(pid as i32), signal) };
    if rc == 0 {
        return true;
    }
    io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

/// Whether a member of the group still answers (kill(-pid, 0); EPERM counts as yes).
pub fn group_alive(pid: u32) -> bool {
    signal_group(pid, 0)
}

/// SIGTERM the group, poll every 25 ms until it is empty, SIGKILL after `grace`, then wait up to
/// 2000 ms more. A pid we refuse to signal is left alone.
pub fn terminate_group(pid: u32, grace: Duration) {
    if !leads_a_group(pid) {
        return;
    }
    signal_group(pid, libc::SIGTERM);
    if group_gone_within(pid, grace) {
        return;
    }
    signal_group(pid, libc::SIGKILL);
    group_gone_within(pid, KILL_SETTLE);
}

/// Polls the group until no member answers or the budget runs out; true once it is empty.
fn group_gone_within(pid: u32, budget: Duration) -> bool {
    let deadline = Instant::now() + budget;
    while group_alive(pid) {
        if Instant::now() >= deadline {
            return false;
        }
        thread::sleep(POLL_INTERVAL);
    }
    true
}
