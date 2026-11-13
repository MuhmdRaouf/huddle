//! The router's parent process. It takes the lock, binds (or takes over) the port, owns the
//! record and the handover socket, and runs the worker (`router serve --worker`) with the
//! listening socket on fd 3. It serves no request. The socket stays open while the parent
//! lives, so a dial that arrives while the worker restarts waits in the kernel queue.

use std::net::TcpListener;
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitStatus, Stdio};
use std::sync::Arc;
use std::time::{Duration, Instant};

use huddle_config::env::Env;
use huddle_router::clock::{iso_utc_ms, now_ms};
use tokio::sync::{mpsc, watch};

use crate::handover::serve_handover;
use crate::serve::{
    DRAIN_BUDGET, RECORD_FILE, Refused, STOP_BUDGET, ServeError, ServeOptions, ServeOutcome,
    Workers, acquire, anthropic_url, make_run_dir, write_record,
};

/// The descriptor number the worker finds the listening socket on.
const LISTENER_FD: libc::c_int = 3;

/// The environment variable that tells a worker its socket is inherited.
const LISTEN_FD_ENV: &str = "HUDDLE_ROUTER_LISTEN_FD";

/// The environment variable that names the parent a worker follows.
const PARENT_ENV: &str = "HUDDLE_ROUTER_PARENT";

/// A worker that lived this long counts as healthy: the next crash starts the delays over.
const STABLE_AFTER: Duration = Duration::from_secs(10);

/// The wait before restart number 1, 2, 3 and 4 or more of a run of fast crashes.
const DELAYS_MS: [u64; 5] = [0, 1_000, 2_000, 5_000, 30_000];

/// How long past its own drain a stopping worker gets before it is killed.
const KILL_GRACE: Duration = Duration::from_secs(5);

/// The restart delays: none for the first crash, then 1 s, 2 s, 5 s and 30 s for each further
/// fast one.
#[derive(Debug, Default)]
pub struct Backoff {
    fast_crashes: usize,
}

impl Backoff {
    /// The wait before the next restart, given how long the worker that just exited lived.
    pub fn after(&mut self, lived: Duration) -> Duration {
        if lived >= STABLE_AFTER {
            self.fast_crashes = 0;
        }
        let step = self.fast_crashes.min(DELAYS_MS.len() - 1);
        self.fast_crashes += 1;
        Duration::from_millis(DELAYS_MS[step])
    }
}

/// How a worker ended, as one log phrase: `signal 9` or `code 1`.
pub fn exit_phrase(status: &ExitStatus) -> String {
    match (status.signal(), status.code()) {
        (Some(signal), _) => format!("signal {signal}"),
        (None, Some(code)) => format!("code {code}"),
        (None, None) => "unknown".to_owned(),
    }
}

/// The socket a worker inherited, or why there is none.
pub fn inherited_listener(env: &Env) -> Result<TcpListener, String> {
    let fd: libc::c_int = env
        .get(LISTEN_FD_ENV)
        .and_then(|value| value.parse().ok())
        .ok_or_else(|| format!("huddle-router: {LISTEN_FD_ENV} names no socket"))?;
    // SAFETY: the parent put a listening socket on this descriptor and nothing else in this
    // process owns it; ownership moves to the listener.
    let listener = unsafe { TcpListener::from_raw_fd(fd) };
    // A process the worker starts must not inherit the socket.
    // SAFETY: fcntl on a descriptor this process owns.
    unsafe {
        libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC);
    }
    Ok(listener)
}

/// The parent a worker follows, from the environment; `None` for a worker started by hand.
pub fn parent_of(env: &Env) -> Option<u32> {
    env.get(PARENT_ENV)?.parse().ok()
}

/// Sets `stop` once the process is no longer a child of `parent`: a worker whose parent died
/// would keep the port with nobody to restart it or take it over.
pub async fn stop_when_orphaned(parent: u32, stop: watch::Sender<bool>) {
    loop {
        // SAFETY: getppid has no preconditions.
        if unsafe { libc::getppid() } as u32 != parent {
            let _ = stop.send(true);
            return;
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

/// One running worker, or the time the next one starts.
enum State {
    Running { pid: u32, since: Instant },
    Waiting { until: tokio::time::Instant },
}

/// What ended a wait on the worker.
enum Event {
    Exited(u32, ExitStatus),
    Stop,
    HandedOver(u32),
}

/// Runs the parent until `stop` becomes true or a taker takes the socket. The outcomes are the
/// ones of a standalone daemon.
pub async fn run(
    options: ServeOptions,
    stop: watch::Receiver<bool>,
) -> Result<ServeOutcome, ServeError> {
    if let Err(text) = anthropic_url(&options.env) {
        return Err(ServeError::BadAnthropicUrl(text));
    }
    make_run_dir(&options.paths.run_dir, &options.log);
    let held = match acquire(&options).await {
        Ok(held) => held,
        Err(Refused::Served { pid }) => return Ok(ServeOutcome::AlreadyServed { pid }),
        Err(Refused::Failed(error)) => return Err(error),
    };
    let ServeOptions {
        paths,
        port,
        version,
        signal,
        log,
        ..
    } = options;
    let port = held.listener.local_addr().map_or(port, |addr| addr.port());
    let executable = std::env::current_exe()
        .map(|path| path.canonicalize().unwrap_or(path))
        .ok();
    let record_path = paths.run_dir.join(RECORD_FILE);
    let (handed_tx, mut handed_rx) = mpsc::unbounded_channel::<u32>();
    let handover = serve_handover(
        &paths.run_dir,
        port,
        &held.listener,
        handed_tx,
        Arc::clone(&log),
    );
    log(format!(
        "huddle-router: supervisor pid={} port={port} by={}",
        std::process::id(),
        held.started_by
    ));

    let (exit_tx, mut exit_rx) = mpsc::unbounded_channel::<(u32, ExitStatus)>();
    let started = iso_utc_ms(now_ms());
    let spawn = |executable: Option<&Path>, workers: &Workers| -> Result<u32, String> {
        let executable = executable.ok_or("the executable is unknown")?;
        let child = spawn_worker(executable, &held.listener, &held.started_by)
            .map_err(|error| error.to_string())?;
        let pid = child.id();
        wait_in_thread(child, exit_tx.clone());
        let workers = Workers {
            pid,
            ..workers.clone()
        };
        write_record(
            &record_path,
            port,
            &version,
            Some(executable),
            &started,
            &workers,
            &log,
        );
        Ok(pid)
    };

    let mut backoff = Backoff::default();
    let mut stop = stop;
    let mut restarts = 0u32;
    let mut state = match spawn(executable.as_deref(), &Workers::default()) {
        Ok(pid) => State::Running {
            pid,
            since: Instant::now(),
        },
        Err(error) => {
            log(format!("huddle-router: worker did not start ({error})"));
            State::Waiting {
                until: tokio::time::Instant::now() + backoff.after(Duration::ZERO),
            }
        }
    };
    // What the last worker's exit said, for the line the restart logs.
    let mut last_exit = String::from("worker did not start");
    let mut delay = Duration::ZERO;
    let outcome = loop {
        let event = match &state {
            State::Running { .. } => {
                tokio::select! {
                    _ = stopped(&mut stop) => Event::Stop,
                    Some(taker) = handed_rx.recv() => Event::HandedOver(taker),
                    Some((exited, status)) = exit_rx.recv() => Event::Exited(exited, status),
                }
            }
            State::Waiting { until } => {
                tokio::select! {
                    _ = stopped(&mut stop) => Event::Stop,
                    Some(taker) = handed_rx.recv() => Event::HandedOver(taker),
                    () = tokio::time::sleep_until(*until) => {
                        let next = Workers {
                            pid: 0,
                            restarts: restarts + 1,
                            last_restart: last_exit.clone(),
                        };
                        match spawn(executable.as_deref(), &next) {
                            Ok(pid) => {
                                restarts = next.restarts;
                                log(format!(
                                    "huddle-router: worker exited ({last_exit}), restarted in {} ms",
                                    delay.as_millis()
                                ));
                                state = State::Running { pid, since: Instant::now() };
                            }
                            Err(error) => {
                                log(format!("huddle-router: worker did not start ({error})"));
                                delay = backoff.after(Duration::ZERO);
                                state = State::Waiting {
                                    until: tokio::time::Instant::now() + delay,
                                };
                            }
                        }
                        continue;
                    }
                }
            }
        };
        match event {
            Event::Exited(pid, status) => {
                let State::Running {
                    pid: current,
                    since,
                } = &state
                else {
                    continue;
                };
                if *current != pid {
                    continue;
                }
                last_exit = exit_phrase(&status);
                delay = backoff.after(since.elapsed());
                state = State::Waiting {
                    until: tokio::time::Instant::now() + delay,
                };
            }
            Event::Stop => {
                if let State::Running { pid, .. } = &state {
                    end_worker(*pid, libc::SIGTERM, STOP_BUDGET, &mut exit_rx).await;
                }
                let _ = std::fs::remove_file(&record_path);
                break ServeOutcome::Stopped;
            }
            Event::HandedOver(taker) => {
                log(format!(
                    "huddle-router: handed over to pid {taker}; draining"
                ));
                if let State::Running { pid, .. } = &state {
                    // The worker stops accepting and keeps its open tunnels; a signal to the
                    // parent ends the drain at once.
                    tokio::select! {
                        _ = end_worker(*pid, libc::SIGUSR1, DRAIN_BUDGET, &mut exit_rx) => {}
                        _ = stopped(&mut stop) => {
                            end_worker(*pid, libc::SIGTERM, STOP_BUDGET, &mut exit_rx).await;
                        }
                    }
                }
                // The record stays for the taker to overwrite.
                break ServeOutcome::HandedOver;
            }
        }
    };
    handover.stop();
    let reason = match outcome {
        ServeOutcome::HandedOver => "handover",
        _ => signal.get().copied().unwrap_or("stop"),
    };
    log(format!(
        "huddle-router: exit pid={} reason={reason}",
        std::process::id()
    ));
    drop(held);
    Ok(outcome)
}

/// Tells the worker to end with `signal`, waits for it up to `budget` plus a grace, and kills
/// it when it does not go.
async fn end_worker(
    pid: u32,
    signal: libc::c_int,
    budget: Duration,
    exits: &mut mpsc::UnboundedReceiver<(u32, ExitStatus)>,
) {
    send(pid, signal);
    let ended = tokio::time::timeout(budget + KILL_GRACE, async {
        while let Some((exited, _)) = exits.recv().await {
            if exited == pid {
                return;
            }
        }
    })
    .await;
    if ended.is_err() {
        send(pid, libc::SIGKILL);
        let _ = tokio::time::timeout(KILL_GRACE, async {
            while let Some((exited, _)) = exits.recv().await {
                if exited == pid {
                    return;
                }
            }
        })
        .await;
    }
}

fn send(pid: u32, signal: libc::c_int) {
    // SAFETY: the pid is a worker this parent started and has not yet reaped.
    unsafe {
        libc::kill(pid as libc::pid_t, signal);
    }
}

/// Resolves when `stop` becomes true; a dropped sender never stops the run.
async fn stopped(stop: &mut watch::Receiver<bool>) {
    loop {
        if stop.wait_for(|value| *value).await.is_ok() {
            return;
        }
        std::future::pending::<()>().await;
    }
}

/// Reaps the worker on its own thread and reports how it ended.
fn wait_in_thread(mut child: Child, exits: mpsc::UnboundedSender<(u32, ExitStatus)>) {
    let pid = child.id();
    std::thread::spawn(move || {
        if let Ok(status) = child.wait() {
            let _ = exits.send((pid, status));
        }
    });
}

/// Starts `<executable> router serve --worker` with the listening socket on fd 3.
fn spawn_worker(
    executable: &Path,
    listener: &TcpListener,
    started_by: &str,
) -> std::io::Result<Child> {
    let fd = listener.as_raw_fd();
    let mut command = Command::new(PathBuf::from(executable));
    command
        .args(["router", "serve", "--worker", "--by", started_by])
        .env(LISTEN_FD_ENV, LISTENER_FD.to_string())
        .env(PARENT_ENV, std::process::id().to_string())
        .stdin(Stdio::null());
    // SAFETY: only dup2 and fcntl run between fork and exec, both async-signal-safe.
    unsafe {
        command.pre_exec(move || {
            if fd == LISTENER_FD {
                let flags = libc::fcntl(fd, libc::F_GETFD);
                if flags < 0 || libc::fcntl(fd, libc::F_SETFD, flags & !libc::FD_CLOEXEC) < 0 {
                    return Err(std::io::Error::last_os_error());
                }
            } else if libc::dup2(fd, LISTENER_FD) < 0 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    command.spawn()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn s1_fast_crashes_back_off() {
        let mut backoff = Backoff::default();
        let fast = Duration::from_millis(300);
        let waits: Vec<u64> = (0..7)
            .map(|_| backoff.after(fast).as_millis() as u64)
            .collect();
        assert_eq!(waits, [0, 1_000, 2_000, 5_000, 30_000, 30_000, 30_000]);
    }

    #[test]
    fn a_worker_that_lived_long_starts_the_delays_over() {
        let mut backoff = Backoff::default();
        for _ in 0..3 {
            backoff.after(Duration::from_secs(1));
        }
        assert_eq!(backoff.after(STABLE_AFTER), Duration::ZERO);
        assert_eq!(backoff.after(Duration::from_secs(1)).as_millis(), 1_000);
    }
}
