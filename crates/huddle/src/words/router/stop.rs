//! The `huddle router stop` word: one SIGTERM to the recorded pid, then a wait for the port
//! and the record to go.

use std::io::Write;
use std::time::{Duration, Instant};

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::serve::STOP_BUDGET;
use huddle_daemon::start::{HEALTH_WAIT, health, read_record};
use huddle_procs::live::is_alive;

/// How long between two checks of the port and the record.
const POLL_STEP: Duration = Duration::from_millis(50);

/// How long a stop waits for a handover's fresh record while the port still answers: the old
/// daemon drops the record before the fresh one writes it.
const HANDOVER_WAIT: Duration = Duration::from_secs(2);

pub fn run(_args: &[String], env: &Env) -> i32 {
    let Some(port) = super::router_port(env) else {
        return 2;
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    // The record speaks for the daemon only while its pid lives and its port is the probed
    // one; a stale record earns no signal. During a handover the port answers while the
    // record is between owners: wait for the fresh one before calling it stale.
    let handover_deadline = Instant::now() + HANDOVER_WAIT;
    let record = loop {
        match read_record(&paths)
            .filter(|record| record.pid > 0 && is_alive(record.pid) && record.port == port)
        {
            Some(record) => break record,
            None => {
                if Instant::now() >= handover_deadline || health(port, HEALTH_WAIT).is_none() {
                    return not_running();
                }
                std::thread::sleep(POLL_STEP);
            }
        }
    };
    if health(port, HEALTH_WAIT).is_none() {
        return not_running();
    }
    let pid = record.pid;
    // SAFETY: `kill` is a plain syscall wrapper; it reads no memory and only sets errno on
    // failure. A failed signal changes nothing: the poll decides the outcome.
    let _ = unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
    let deadline = Instant::now() + STOP_BUDGET;
    loop {
        if health(port, HEALTH_WAIT).is_none() && read_record(&paths).is_none() {
            let _ = writeln!(std::io::stdout(), "huddle-router: stopped (pid {pid})");
            return 0;
        }
        if Instant::now() >= deadline {
            let _ = writeln!(
                std::io::stderr(),
                "huddle-router: pid {pid} did not stop within 10s"
            );
            return 1;
        }
        std::thread::sleep(POLL_STEP);
    }
}

fn not_running() -> i32 {
    let _ = writeln!(std::io::stdout(), "huddle-router: not running");
    6
}
