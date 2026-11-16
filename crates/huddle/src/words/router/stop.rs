//! The `huddle router stop` word: a SIGTERM to each process proven to be this home's router (the
//! recorded one, and the one holding the router lock), then a wait until every one of them has
//! exited, the port is closed and the lock is free. A held lock never reads as "not running":
//! when no pid can be proven, the word exits 1 and names the lock.

use std::io::Write;
use std::path::Path;
use std::time::{Duration, Instant};

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::serve::lock_path;
use huddle_daemon::singleton::{holder, proven_holder};
use huddle_daemon::start::{DEFAULT_BUDGET, HEALTH_WAIT, health, read_record};
use huddle_daemon::supervisor::STOP_DEADLINE;
use huddle_procs::live::{is_alive, process_alive, start_token};

/// How long between two looks at the lock, the record and the port.
const POLL_STEP: Duration = Duration::from_millis(50);

/// How long a stop waits for a handover's fresh record while the port still answers: the old
/// daemon drops the record before the fresh one writes it.
const HANDOVER_WAIT: Duration = Duration::from_secs(2);

/// How long a held lock may stay unproven (a router coming up writes its pid and start token
/// right after it takes the lock) before the word gives up and keeps its hands off.
const UNPROVEN_WAIT: Duration = DEFAULT_BUDGET;

/// What a signalled router gets on top of its supervisor's own worst case before the word
/// reports it did not stop.
const EXIT_MARGIN: Duration = Duration::from_secs(2);

/// One look at this home's router.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
struct Look {
    /// Someone holds the router lock.
    lock_held: bool,
    /// The lock holder, when the lock file proves it (pid and start token of a live process).
    proven_holder: Option<u32>,
    /// The recorded pid, when it is alive and the record names the probed port.
    record: Option<u32>,
    /// A health probe of the port answered.
    answers: bool,
    /// How long since the word started or last sent a signal.
    waited: Duration,
}

/// What the word does after one look.
#[derive(Debug, PartialEq, Eq)]
enum Step {
    /// Send SIGTERM to this pid.
    Signal(u32),
    /// Look again after a short sleep.
    Wait,
    /// Nothing of this home's router is left.
    Done,
    /// The lock is held and no pid can be proven: signal nothing, report failure.
    Unproven,
}

/// The stop decision. `signaled` are the pids this stop already sent SIGTERM to; `exiting` is
/// whether one of them, or a worker they named, still lives.
fn decide(look: &Look, signaled: &[u32], exiting: bool) -> Step {
    // A recorded router is ours when it answers on the probed port or the lock proves its pid.
    // During a handover the taker answers while the old router still holds the lock.
    if let Some(pid) = look.record
        && !signaled.contains(&pid)
        && (look.answers || look.proven_holder == Some(pid))
    {
        return Step::Signal(pid);
    }
    // The port answers while no record names a router: a handover is between owners, and the
    // fresh record is on its way.
    if look.answers && look.record.is_none() && look.waited < HANDOVER_WAIT {
        return Step::Wait;
    }
    // The lock holder: an old router still draining after a handover, or one coming up that
    // has no record yet.
    if let Some(pid) = look.proven_holder
        && !signaled.contains(&pid)
    {
        return Step::Signal(pid);
    }
    // A signalled router the lock or the record still names is still on its way out, whatever
    // one probe of its start token said, for as long as its supervisor may take.
    let named = |pid: Option<u32>| {
        look.waited < STOP_DEADLINE + EXIT_MARGIN && pid.is_some_and(|pid| signaled.contains(&pid))
    };
    if exiting || named(look.proven_holder) || named(look.record) {
        return Step::Wait;
    }
    if look.lock_held {
        return if look.waited < UNPROVEN_WAIT {
            Step::Wait
        } else {
            Step::Unproven
        };
    }
    Step::Done
}

/// A process this stop signalled, or a worker it named, with the start token that proves it is
/// still the same process.
struct Watched {
    pid: u32,
    token: String,
    worker: Option<(u32, String)>,
    deadline: Instant,
}

impl Watched {
    fn alive(&self) -> bool {
        process_alive(self.pid, &self.token)
            || self
                .worker
                .as_ref()
                .is_some_and(|(pid, token)| process_alive(*pid, token))
    }

    fn name(&self) -> String {
        match &self.worker {
            Some((worker, _)) => format!("parent {}, worker {worker}", self.pid),
            None => format!("pid {}", self.pid),
        }
    }
}

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
    let lock = lock_path(&paths);
    let mut since = Instant::now();
    let mut watched: Vec<Watched> = Vec::new();
    loop {
        if let Some(late) = watched
            .iter()
            .find(|watch| watch.alive() && Instant::now() >= watch.deadline)
        {
            let _ = writeln!(
                std::io::stderr(),
                "huddle-router: pid {} did not stop within {}s",
                late.pid,
                (STOP_DEADLINE + EXIT_MARGIN).as_secs()
            );
            return 1;
        }
        let look = look(&paths, &lock, port, since.elapsed());
        let signaled: Vec<u32> = watched.iter().map(|watch| watch.pid).collect();
        let exiting = watched.iter().any(Watched::alive);
        match decide(&look, &signaled, exiting) {
            Step::Signal(pid) => {
                if let Some(watch) = signal(&paths, pid) {
                    watched.push(watch);
                }
                since = Instant::now();
            }
            Step::Wait => std::thread::sleep(POLL_STEP),
            Step::Done if watched.is_empty() => return not_running(),
            Step::Done => {
                let who: Vec<String> = watched.iter().map(Watched::name).collect();
                let _ = writeln!(
                    std::io::stdout(),
                    "huddle-router: stopped ({})",
                    who.join("; ")
                );
                return 0;
            }
            Step::Unproven => {
                let _ = writeln!(
                    std::io::stderr(),
                    "huddle-router: {} is held by a process huddle cannot prove is its router; nothing was stopped",
                    lock.display()
                );
                return 1;
            }
        }
    }
}

/// Looks once at the lock, the record and the port.
fn look(paths: &Paths, lock: &Path, port: u16, waited: Duration) -> Look {
    let record = read_record(paths)
        .filter(|record| record.pid > 0 && is_alive(record.pid) && record.port == port)
        .map(|record| record.pid);
    Look {
        lock_held: holder(lock).is_some(),
        proven_holder: proven_holder(lock),
        record,
        answers: health(port, HEALTH_WAIT).is_some(),
        waited,
    }
}

/// Sends SIGTERM to `pid` and answers what to wait for: the pid with its start token and, when
/// the record names it, its worker. A pid already gone is nothing to wait for.
fn signal(paths: &Paths, pid: u32) -> Option<Watched> {
    let token = start_token(pid)?;
    let worker = read_record(paths)
        .filter(|record| record.pid == pid && record.worker > 0)
        .and_then(|record| start_token(record.worker).map(|token| (record.worker, token)));
    // The daemon's own exit line names the signal; this line names who sent it.
    let log = huddle_router::crashlog::router_logs(&paths.state_dir);
    log(format!("huddle-router: the stop word signals pid={pid}"));
    // SAFETY: `kill` is a plain syscall wrapper; it reads no memory and only sets errno on
    // failure. A failed signal changes nothing: the next look decides the outcome.
    let _ = unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
    Some(Watched {
        pid,
        token,
        worker,
        deadline: Instant::now() + STOP_DEADLINE + EXIT_MARGIN,
    })
}

fn not_running() -> i32 {
    let _ = writeln!(std::io::stdout(), "huddle-router: not running");
    6
}

#[cfg(test)]
mod tests {
    use super::*;

    fn look(
        lock_held: bool,
        proven_holder: Option<u32>,
        record: Option<u32>,
        answers: bool,
    ) -> Look {
        Look {
            lock_held,
            proven_holder,
            record,
            answers,
            waited: Duration::ZERO,
        }
    }

    #[test]
    fn l67_a_healthy_router_is_signalled_through_its_record() {
        assert_eq!(
            decide(&look(true, Some(7), Some(7), true), &[], false),
            Step::Signal(7)
        );
    }

    #[test]
    fn l67_a_frozen_router_that_holds_the_lock_is_signalled() {
        assert_eq!(
            decide(&look(true, Some(7), Some(7), false), &[], false),
            Step::Signal(7)
        );
    }

    #[test]
    fn l67_a_router_coming_up_without_a_record_is_signalled_through_the_lock() {
        assert_eq!(
            decide(&look(true, Some(7), None, false), &[], false),
            Step::Signal(7)
        );
    }

    #[test]
    fn l67_a_held_lock_with_no_proven_pid_waits_then_gives_up_never_not_running() {
        let mut starting = look(true, None, None, false);
        assert_eq!(decide(&starting, &[], false), Step::Wait);
        starting.waited = UNPROVEN_WAIT;
        assert_eq!(decide(&starting, &[], false), Step::Unproven);
    }

    #[test]
    fn l67_a_reused_record_pid_with_another_holder_is_never_signalled() {
        // The record names 7, which no longer answers; the lock proves 9 is the router.
        assert_eq!(
            decide(&look(true, Some(9), Some(7), false), &[], false),
            Step::Signal(9)
        );
        assert_eq!(
            decide(&look(true, Some(9), Some(7), false), &[9], true),
            Step::Wait
        );
        assert_eq!(
            decide(&look(true, None, Some(7), false), &[], false),
            Step::Wait,
            "an unproven lock and a silent record signal nobody"
        );
    }

    #[test]
    fn l67_after_a_handover_the_taker_and_then_the_draining_holder_are_signalled() {
        // The taker (8) answers and holds the record; the old router (7) holds the lock.
        assert_eq!(
            decide(&look(true, Some(7), Some(8), true), &[], false),
            Step::Signal(8)
        );
        assert_eq!(
            decide(&look(true, Some(7), None, false), &[8], false),
            Step::Signal(7)
        );
        assert_eq!(
            decide(&look(true, Some(7), None, false), &[8, 7], true),
            Step::Wait
        );
        assert_eq!(
            decide(&look(false, None, None, false), &[8, 7], false),
            Step::Done
        );
    }

    #[test]
    fn l67_a_signalled_router_is_waited_for_until_the_lock_is_free() {
        assert_eq!(
            decide(&look(true, Some(7), Some(7), true), &[7], true),
            Step::Wait
        );
        assert_eq!(
            decide(&look(false, None, None, true), &[7], true),
            Step::Wait
        );
        assert_eq!(
            decide(&look(false, None, None, false), &[7], false),
            Step::Done
        );
    }

    #[test]
    fn l67_a_signalled_router_still_named_by_the_lock_or_record_is_waited_for() {
        // One probe of its start token failed: the lock and the record still name it.
        let mut stopping = look(true, Some(7), Some(7), false);
        stopping.waited = UNPROVEN_WAIT;
        assert_eq!(decide(&stopping, &[7], false), Step::Wait);
        assert_eq!(
            decide(&look(false, None, Some(7), false), &[7], false),
            Step::Wait
        );
        let mut stale = look(false, None, Some(7), false);
        stale.waited = STOP_DEADLINE + EXIT_MARGIN;
        assert_eq!(
            decide(&stale, &[7], false),
            Step::Done,
            "a record left by a router that is gone stops the wait"
        );
    }

    #[test]
    fn l67_a_proven_holder_with_the_port_answering_and_no_record_waits_for_the_record() {
        let mut between = look(true, Some(7), None, true);
        assert_eq!(decide(&between, &[], false), Step::Wait);
        between.waited = HANDOVER_WAIT;
        assert_eq!(decide(&between, &[], false), Step::Signal(7));
    }

    #[test]
    fn l67_nothing_held_and_nothing_answering_is_done() {
        assert_eq!(
            decide(&look(false, None, None, false), &[], false),
            Step::Done
        );
        assert_eq!(
            decide(&look(false, None, Some(7), false), &[], false),
            Step::Done,
            "a live pid in a record nobody holds the lock for is a stale record"
        );
    }

    #[test]
    fn l67_a_port_answering_between_handover_owners_is_waited_for() {
        let mut between = look(false, None, None, true);
        assert_eq!(decide(&between, &[], false), Step::Wait);
        between.waited = HANDOVER_WAIT;
        assert_eq!(decide(&between, &[], false), Step::Done);
    }
}
