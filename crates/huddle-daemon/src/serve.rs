//! huddle-daemon: serve. The daemon's run loop: one lock, one port, one record, the handover
//! socket, the lanes following providers.toml, the memory ceiling, and a clean stop or handover
//! at the end.

use std::fmt;
use std::fs;
use std::io;
use std::net::TcpListener;
use std::os::fd::FromRawFd;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock, RwLock};
use std::time::{Duration, Instant};

use huddle_catalog::providers::provider_ids;
use huddle_config::atomic::write_atomic;
use huddle_config::dashboard_hosts;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_config::providers_file::read_enabled;
use huddle_history::History;
use huddle_procs::live::start_token;
use huddle_router::clock::{iso_utc_ms, now_ms};
use huddle_router::connect::{DrainOutcome, TunnelTracker};
use huddle_router::dispatch::{Router, RouterConfig};
use huddle_router::reload::{DashboardHostsWatch, POLL, ProvidersWatch, ReloadAction};
use huddle_router::server::{ROUTER_NAME, ServerLimits, serve_tracked};
use tokio::sync::{mpsc, watch};

use crate::dashboard_inputs;
use crate::handover::{serve_handover, takeover as take_over_socket};
use crate::history_sink::HistoryBridge;
use crate::history_upkeep::{self, FILL_WINDOW_MS, FIRST_FILL_WINDOW_MS};
use crate::nochain::clean_proxy_env;
use crate::periodic::{Job, Runner, Schedule};
use crate::rss::{RssDecision, RssWatch, ceiling_of, interval_of, read_rss};
use crate::singleton::{self, SingletonError};
use crate::start::{RouterRecord, read_record, spawn_serve};
use huddle_config::files::{DIR_MODE, FILE_MODE, create_dir};

/// How long a clean stop keeps the requests it already accepted.
pub const STOP_BUDGET: Duration = Duration::from_secs(10);

/// How long a daemon that handed its socket over keeps the requests it already accepted.
pub const DRAIN_BUDGET: Duration = Duration::from_secs(600);

/// `$HUDDLE_ROUTER_DRAIN_LIMIT_MS` moves [`DRAIN_BUDGET`]; a value that is not a positive
/// number of milliseconds is ignored.
pub fn drain_limit(env: &Env) -> Duration {
    env.get("HUDDLE_ROUTER_DRAIN_LIMIT_MS")
        .and_then(|raw| raw.parse::<u64>().ok())
        .filter(|ms| *ms > 0)
        .map_or(DRAIN_BUDGET, Duration::from_millis)
}

/// How a drain of both trackers ended, with what was still open when the limit hit.
struct DrainReport {
    outcome: DrainOutcome,
    tunnels_open: usize,
    connections_open: usize,
}

/// Drains one tracker and answers how many entries it still held when `limit` ran out (zero
/// when it finished in time). The count is read before the drain closes what is left.
async fn drain_counted(tracker: &TunnelTracker, limit: Duration) -> (DrainOutcome, usize) {
    let deadline = tokio::time::sleep(limit);
    let drain = tracker.drain(limit);
    tokio::pin!(deadline, drain);
    tokio::select! {
        biased;
        _ = &mut deadline => {
            let open = tracker.count();
            ((&mut drain).await, open)
        }
        outcome = &mut drain => (outcome, 0),
    }
}

/// Drains every tunnel and every connection together: idle ones close at once, a request in
/// flight finishes, and whatever is left at `limit` is cut.
async fn drain_all(
    tunnels: &TunnelTracker,
    connections: &TunnelTracker,
    limit: Duration,
) -> DrainReport {
    let ((tunnels, tunnels_open), (connections, connections_open)) = tokio::join!(
        drain_counted(tunnels, limit),
        drain_counted(connections, limit)
    );
    let outcome = if tunnels == DrainOutcome::TimedOut || connections == DrainOutcome::TimedOut {
        DrainOutcome::TimedOut
    } else {
        DrainOutcome::Finished
    };
    DrainReport {
        outcome,
        tunnels_open,
        connections_open,
    }
}

/// How long a takeover start waits for the router lock the previous daemon still holds while it
/// drains: the drain's budget plus a minute of margin.
pub const TAKEOVER_LOCK_WAIT: Duration = Duration::from_secs(660);

/// How often the takeover's lock wait tries the lock and looks for its run's end.
const LOCK_POLL: Duration = Duration::from_millis(50);

/// The record under the run dir, `<run_dir>/router.toml`.
pub(crate) const RECORD_FILE: &str = "router.toml";

/// The lock under the run dir, `<run_dir>/router.lock`.
const LOCK_FILE: &str = "router.lock";

/// Where the router lock of this home lives.
pub fn lock_path(paths: &Paths) -> std::path::PathBuf {
    paths.run_dir.join(LOCK_FILE)
}

/// The request history database in the state folder.
const HISTORY_FILE: &str = "huddle.db";

/// How often old history rows are pruned; the first run is at start.
const PRUNE_EVERY: Duration = Duration::from_secs(24 * 3600);
/// How often the missing usage of finished requests is credited from the session logs.
const FILL_EVERY: Duration = Duration::from_secs(60);

/// The Anthropic the Claude models go to when no override is set.
const ANTHROPIC_DEFAULT: &str = "https://api.anthropic.com";

/// Where the daemon's lines go.
pub type Logger = Arc<dyn Fn(String) + Send + Sync>;

/// One run's wiring: the environment, the paths, the port to serve, the version the record
/// names, whether a takeover asks the running daemon for its socket first, and where the lines
/// go.
pub struct ServeOptions {
    pub env: Env,
    pub paths: Paths,
    pub port: u16,
    pub version: String,
    pub takeover: bool,
    /// Who started this run: `setup`, `session-start`, `prompt-submit`, `router start`. A
    /// takeover passes the reason it asks the running daemon to hand over.
    pub by: String,
    /// The install mark a start nobody asked for (a hook) needs: once the router lock is held,
    /// a missing mark ends the run before it writes anything. `None` serves unconditionally.
    pub install_mark: Option<PathBuf>,
    /// The signal that stopped the run, set by the caller's signal task before it sends the stop.
    pub signal: Arc<OnceLock<&'static str>>,
    pub log: Logger,
    /// Where the daemon's stdout and stderr lines go once the router's logger exists; `None`
    /// when nothing is captured.
    pub output: Option<crate::output::Sink>,
}

/// How one run ended.
#[derive(Debug, PartialEq, Eq)]
pub enum ServeOutcome {
    /// The stop signal came and the daemon drained and cleaned up.
    Stopped,
    /// A taker took the socket; the daemon drained and left the record for it.
    HandedOver,
    /// Another daemon already serves this home; nothing was bound.
    AlreadyServed { pid: u32 },
}

/// Why a run ended before it served.
#[derive(Debug)]
pub enum ServeError {
    /// The port could not be taken.
    Bind { port: u16, message: String },
    /// `$HUDDLE_ROUTER_ANTHROPIC_URL` does not hold up; found before anything is taken.
    BadAnthropicUrl(String),
    /// A worker started without the socket its parent passes.
    Worker(String),
}

impl fmt::Display for ServeError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ServeError::Bind { port, message } => {
                write!(
                    f,
                    "huddle-router: cannot listen on 127.0.0.1:{port} ({message})"
                )
            }
            ServeError::BadAnthropicUrl(text) | ServeError::Worker(text) => write!(f, "{text}"),
        }
    }
}

impl std::error::Error for ServeError {}

/// Who runs [`engine`]: a daemon that serves and owns the lock, the record and the handover
/// socket, or the worker of a supervising parent that owns them.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Mode {
    Standalone,
    Worker,
}

/// What a start holds once it may serve: the listening socket, the router lock, how it came by
/// them.
pub struct Acquired {
    pub listener: TcpListener,
    pub lock_guard: Option<singleton::LockGuard>,
    pub lock_release: Option<tokio::sync::oneshot::Receiver<singleton::LockGuard>>,
    /// `handover` after a takeover, else the reason the caller gave.
    pub started_by: String,
    /// The line a takeover logged, for the dashboard's recent events.
    pub took_over: Option<String>,
}

/// Why a start holds nothing.
#[derive(Debug)]
pub enum Refused {
    /// Another daemon already serves this home.
    Served {
        pid: u32,
    },
    Failed(ServeError),
}

/// Runs until `stop` becomes true, a taker takes the socket, or the listener fails.
pub async fn run(
    options: ServeOptions,
    stop: watch::Receiver<bool>,
) -> Result<ServeOutcome, ServeError> {
    // The Anthropic origin is parsed before anything is taken: a bad override ends the run with
    // the lock, the port and the run dir untouched.
    let anthropic_url = match anthropic_url(&options.env) {
        Ok(url) => url,
        Err(text) => return Err(ServeError::BadAnthropicUrl(text)),
    };
    make_run_dir(&options.paths.run_dir, &options.log);
    let held = match acquire(&options).await {
        Ok(held) => held,
        Err(Refused::Served { pid }) => return Ok(ServeOutcome::AlreadyServed { pid }),
        Err(Refused::Failed(error)) => return Err(error),
    };
    engine(options, held, Mode::Standalone, anthropic_url, stop, None).await
}

/// Serves on the listening socket a supervising parent passed in, until `stop` becomes true or
/// `drain` names a taker (the parent handed the socket over: the worker stops accepting and
/// keeps its open tunnels for [`DRAIN_BUDGET`]). The parent owns the lock, the record and the
/// handover socket.
pub async fn run_worker(
    options: ServeOptions,
    listener: TcpListener,
    stop: watch::Receiver<bool>,
    drain: mpsc::UnboundedReceiver<u32>,
) -> Result<ServeOutcome, ServeError> {
    let anthropic_url = match anthropic_url(&options.env) {
        Ok(url) => url,
        Err(text) => return Err(ServeError::BadAnthropicUrl(text)),
    };
    let held = Acquired {
        listener,
        lock_guard: None,
        lock_release: None,
        started_by: options.by.clone(),
        took_over: None,
    };
    engine(
        options,
        held,
        Mode::Worker,
        anthropic_url,
        stop,
        Some(drain),
    )
    .await
}

/// Takes the router lock and the socket: a takeover asks the running daemon for its socket and
/// serves it at once, catching up with the lock in the background while the previous daemon
/// drains; any other start takes the lock, then binds the port. Needs the run dir to exist.
pub async fn acquire(options: &ServeOptions) -> Result<Acquired, Refused> {
    let ServeOptions {
        paths,
        port,
        takeover,
        by,
        log,
        ..
    } = options;
    let run_dir = paths.run_dir.clone();
    let mut listener = None;
    let mut lock_release = None;
    let mut started_by = by.clone();
    let mut took_over = None;
    if *takeover {
        let old_pid = read_record(paths).map(|record| record.pid);
        match tokio::task::spawn_blocking({
            let run_dir = run_dir.clone();
            let by = by.clone();
            move || take_over_socket(&run_dir, std::process::id(), &by)
        })
        .await
        {
            Ok(Ok(taken)) => {
                started_by = "handover".to_owned();
                if let Some(old) = old_pid {
                    let line = format!("huddle-router: took over from pid={old}");
                    log(line.clone());
                    took_over = Some(line);
                }
                listener = Some(taken);
                lock_release = Some(hold_lock_behind_the_run(run_dir.clone(), log.clone()));
            }
            Ok(Err(error)) => log(format!("huddle-router: takeover: {error}")),
            Err(error) => log(format!("huddle-router: takeover: {error}")),
        }
    }

    // One router per home: a standing holder is named at once; a dead one never blocks, the
    // kernel already dropped its lock. Any other failure leaves the daemon serving unheld.
    let lock_guard = if listener.is_none() {
        match singleton::take_for_start(&run_dir.join(LOCK_FILE), ROUTER_NAME) {
            Ok(guard) => Some(guard),
            Err(SingletonError::Held(held)) => return Err(Refused::Served { pid: held.pid }),
            Err(error) => {
                log(singleton_line(&error));
                None
            }
        }
    } else {
        None
    };

    let listener = match listener {
        Some(listener) => listener,
        None => bind_with_backlog(*port).map_err(|error| {
            Refused::Failed(ServeError::Bind {
                port: *port,
                message: error.to_string(),
            })
        })?,
    };
    Ok(Acquired {
        listener,
        lock_guard,
        lock_release,
        started_by,
        took_over,
    })
}

/// The serving loop shared by a standalone daemon and a worker.
async fn engine(
    options: ServeOptions,
    held: Acquired,
    mode: Mode,
    anthropic_url: hyper::Uri,
    stop: watch::Receiver<bool>,
    drain: Option<mpsc::UnboundedReceiver<u32>>,
) -> Result<ServeOutcome, ServeError> {
    let ServeOptions {
        env,
        paths,
        mut port,
        version,
        signal,
        log,
        output,
        ..
    } = options;
    let Acquired {
        listener,
        lock_guard,
        lock_release,
        started_by,
        took_over,
    } = held;
    let config_dir = paths.config_dir.clone();
    let run_dir = paths.run_dir.clone();

    // The taken socket's port comes with the socket; a takeover ignores the asked port.
    if let Ok(addr) = listener.local_addr() {
        port = addr.port();
    }
    if let Err(error) = listener.set_nonblocking(true) {
        return Err(ServeError::Bind {
            port,
            message: error.to_string(),
        });
    }
    let start_line = format!(
        "huddle-router: start v{version} pid={} port={port} by={started_by}",
        std::process::id()
    );
    log(start_line.clone());

    // The daemon must not chain through a router, its own or a neighbour's; the check probes
    // loopback proxies, so it runs off the async task.
    let cleaned = match tokio::task::spawn_blocking({
        let env = env.clone();
        let log = Arc::clone(&log);
        move || clean_proxy_env(&env, port, &*log)
    })
    .await
    {
        Ok(env) => env,
        Err(error) => {
            log(format!("huddle-router: proxy check: {error}"));
            env.clone()
        }
    };

    // The baseline is taken before the first read: a provider enabled while the daemon starts
    // changes the file after this snapshot, so the first tick swaps instead of missing it.
    let providers_watch = ProvidersWatch::new(config_dir.clone(), provider_ids().to_vec());
    let initial_enabled = match read_enabled(&config_dir, &provider_ids()) {
        Ok(names) => names,
        Err(error) => {
            log(format!("huddle-router: {error}"));
            Vec::new()
        }
    };

    let mut hosts_watch = DashboardHostsWatch::new(dashboard_hosts::config_path(&paths));
    let dashboard_hosts_now = Arc::new(RwLock::new(hosts_watch.tick(&*log)));
    // Every later poll stats and reads files, so the watches live where a blocking thread can
    // take them; the loop below only applies their answer.
    let reload_watches = Arc::new(Mutex::new((providers_watch, hosts_watch)));

    let executable = executable_resolved();
    let mut dashboard =
        dashboard_inputs::start(version.clone(), cleaned.clone(), paths.clone(), port);
    dashboard.hosts = {
        let hosts = Arc::clone(&dashboard_hosts_now);
        // The guard is never held across an await; a poisoned lock still holds a whole list.
        Arc::new(move || hosts.read().unwrap_or_else(|e| e.into_inner()).clone())
    };
    let history_db = paths.state_dir.join(HISTORY_FILE);
    dashboard.history = {
        let db = history_db.clone();
        Arc::new(move || history_upkeep::open_for_read(&db))
    };
    dashboard.keep_days = {
        let config = dashboard_hosts::config_path(&paths);
        Arc::new(move || history_upkeep::keep_days(&config))
    };
    // The database is created and migrated once, here, so the writer threads that open it next
    // never race to create it.
    let opened = tokio::task::spawn_blocking({
        let db = history_db.clone();
        move || History::open(&db).map(drop)
    })
    .await;
    match opened {
        Ok(Ok(())) => {}
        Ok(Err(error)) => log(format!("huddle-router: history: {error}")),
        Err(error) => log(format!("huddle-router: history: {error}")),
    }
    let history = Arc::new(HistoryBridge::start(&history_db));
    let router = Router::new(
        RouterConfig {
            env: cleaned.clone(),
            paths: paths.clone(),
            port,
            anthropic_url,
            log: Arc::clone(&log),
            history: Some(history.clone()),
            dashboard,
        },
        &initial_enabled,
    );
    // The lines logged before the router existed join the dashboard's recent events, and the
    // lines after them reach it through the router's own log.
    for line in took_over.iter().chain([&start_line]) {
        router.note_event(line);
    }
    let log = router.logger();
    if let Some(output) = &output {
        let _ = output.set(Arc::clone(&log));
    }

    // The handover socket gives the listening socket to the next binary without closing the
    // port; a socket that cannot open is logged and skipped, the router still serves.
    let (handed_tx, mut handed_rx) = mpsc::unbounded_channel::<u32>();
    let mut handover = (mode == Mode::Standalone)
        .then(|| serve_handover(&run_dir, port, &listener, handed_tx, Arc::clone(&log)));
    if let Some(drain) = drain {
        handed_rx = drain;
    }

    // The record lands before the first request is answered; status and start read it. A
    // router without its record still serves.
    let record_path = run_dir.join(RECORD_FILE);
    if mode == Mode::Standalone {
        write_record(
            &record_path,
            port,
            &version,
            executable.as_deref(),
            &iso_utc_ms(now_ms()),
            &Workers::default(),
            &log,
        );
    }

    let (serve_stop, serve_stop_rx) = watch::channel(false);
    let tokio_listener = match tokio::net::TcpListener::from_std(listener) {
        Ok(listener) => listener,
        Err(error) => {
            return Err(ServeError::Bind {
                port,
                message: error.to_string(),
            });
        }
    };
    let connections = router.connections();
    let mut serve_task = tokio::spawn(serve_tracked(
        tokio_listener,
        router.handler(),
        ServerLimits::default(),
        serve_stop_rx,
        Arc::clone(&connections),
    ));
    let tunnels = router.tunnels();

    // The reload, the RSS check and the prune share one schedule: the loop awaits each due job
    // before it waits again, so one blocking read runs at a time.
    let rss_interval = interval_of(&cleaned);
    let mut schedule = Schedule::new(
        Instant::now(),
        POLL,
        rss_interval,
        Duration::ZERO,
        PRUNE_EVERY,
    );

    let mut stop = stop;
    let mut fill_timer = tokio::time::interval(FILL_EVERY);
    let mut first_fill = true;
    let fill_gate = history_upkeep::FillGate::default();
    let mut rss_watch = RssWatch::new(ceiling_of(&cleaned), rss_interval);
    let outcome;
    loop {
        tokio::select! {
            _ = stop_fired(&mut stop) => {
                let _ = serve_stop.send(true);
                let _ = drain_all(&tunnels, &connections, STOP_BUDGET).await;
                // A clean stop takes the record with it; status reads no ghost.
                remove_record(mode, &record_path);
                outcome = Ok(ServeOutcome::Stopped);
                break;
            }
            Some(taker) = handed_rx.recv() => {
                log(format!("huddle-router: handed over to pid {taker}; draining"));
                let _ = serve_stop.send(true);
                // The taker owns the port now: the socket file and this process's copy of the
                // listener go first, so nothing queues behind a router that no longer accepts.
                if let Some(handover) = handover.take() {
                    handover.stop();
                }
                // A signal ends the drain at once: the daemon is going anyway.
                log(format!(
                    "huddle-router: drain starts; {} tunnels and {} connections open",
                    tunnels.count(),
                    connections.count()
                ));
                let drain_started = Instant::now();
                tokio::select! {
                    drained = drain_all(&tunnels, &connections, drain_limit(&cleaned)) => {
                        if drained.outcome == DrainOutcome::TimedOut {
                            log(format!(
                                "huddle-router: drain limit reached; open requests cut; {} tunnels and {} connections still open",
                                drained.tunnels_open, drained.connections_open
                            ));
                        } else {
                            log(format!(
                                "huddle-router: drained in {} ms",
                                drain_started.elapsed().as_millis()
                            ));
                        }
                    }
                    _ = stop_fired(&mut stop) => {}
                }
                // The record stays for the taker to overwrite.
                outcome = Ok(ServeOutcome::HandedOver);
                break;
            }
            served = &mut serve_task => {
                remove_record(mode, &record_path);
                match served {
                    Ok(Ok(())) => outcome = Ok(ServeOutcome::Stopped),
                    Ok(Err(error)) => {
                        log(format!("huddle-router: {error}"));
                        outcome = Ok(ServeOutcome::Stopped);
                    }
                    Err(error) => {
                        log(format!("huddle-router: serve: {error}"));
                        outcome = Ok(ServeOutcome::Stopped);
                    }
                }
                break;
            }
            _ = schedule.wait() => {
                let mut jobs = Jobs {
                    reload_watches: &reload_watches,
                    dashboard_hosts_now: &dashboard_hosts_now,
                    router: &router,
                    rss_watch: &mut rss_watch,
                    env: &cleaned,
                    paths: &paths,
                    port,
                    executable: executable.as_deref(),
                    log: &log,
                };
                schedule.run_due(&mut jobs).await;
            }
            _ = fill_timer.tick() => {
                let window = if first_fill { FIRST_FILL_WINDOW_MS } else { FILL_WINDOW_MS };
                first_fill = false;
                fill_history(&fill_gate, &paths, window, &log);
            }
        }
    }
    if let Some(handover) = &handover {
        handover.stop();
    }
    // The writer flushes its queue when the last handle drops.
    drop(router);
    drop(history);
    drop(lock_guard);
    drop(lock_release);
    let reason = match &outcome {
        Ok(ServeOutcome::HandedOver) => "handover",
        _ => signal.get().copied().unwrap_or("stop"),
    };
    log(format!(
        "huddle-router: exit pid={} reason={reason}",
        std::process::id()
    ));
    outcome
}

/// A standalone daemon owns the record; a worker's parent does.
fn remove_record(mode: Mode, path: &Path) {
    if mode == Mode::Standalone {
        let _ = fs::remove_file(path);
    }
}

/// Resolves when `stop` becomes true; a dropped sender means nobody can stop this run, so it
/// never fires.
async fn stop_fired(stop: &mut watch::Receiver<bool>) {
    loop {
        if stop.wait_for(|value| *value).await.is_ok() {
            return;
        }
        std::future::pending::<()>().await;
    }
}

/// How many dials a listening socket holds while nobody accepts them: dials that arrive while a
/// taker sets up sit in the kernel queue instead of being refused.
const LISTEN_BACKLOG: libc::c_int = 1024;

/// Binds 127.0.0.1:`port` with a [`LISTEN_BACKLOG`] queue. `std::net::TcpListener::bind` would
/// take the OS default, which is far smaller on macOS.
fn bind_with_backlog(port: u16) -> io::Result<TcpListener> {
    // SAFETY: plain socket calls; the descriptor is owned here and closed on every error path
    // before `from_raw_fd` takes it.
    unsafe {
        let fd = libc::socket(libc::AF_INET, libc::SOCK_STREAM, 0);
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        // A process this daemon starts must not inherit the socket by accident; the supervisor
        // hands it to its worker on a descriptor of its own.
        if libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC) != 0 {
            let error = io::Error::last_os_error();
            let _ = libc::close(fd);
            return Err(error);
        }
        // The reuse rule std's own bind applies, so a restart inside TIME_WAIT rebinds.
        let one: libc::c_int = 1;
        if libc::setsockopt(
            fd,
            libc::SOL_SOCKET,
            libc::SO_REUSEADDR,
            std::ptr::addr_of!(one).cast(),
            std::mem::size_of_val(&one) as libc::socklen_t,
        ) != 0
        {
            let error = io::Error::last_os_error();
            let _ = libc::close(fd);
            return Err(error);
        }
        #[cfg(target_os = "macos")]
        let addr = libc::sockaddr_in {
            sin_len: std::mem::size_of::<libc::sockaddr_in>() as u8,
            sin_family: libc::AF_INET as libc::sa_family_t,
            sin_port: port.to_be(),
            sin_addr: libc::in_addr {
                s_addr: u32::from_ne_bytes([127, 0, 0, 1]),
            },
            sin_zero: [0; 8],
        };
        #[cfg(not(target_os = "macos"))]
        let addr = libc::sockaddr_in {
            sin_family: libc::AF_INET as libc::sa_family_t,
            sin_port: port.to_be(),
            sin_addr: libc::in_addr {
                s_addr: u32::from_ne_bytes([127, 0, 0, 1]),
            },
            sin_zero: [0; 8],
        };
        if libc::bind(
            fd,
            std::ptr::addr_of!(addr).cast(),
            std::mem::size_of::<libc::sockaddr_in>() as libc::socklen_t,
        ) != 0
        {
            let error = io::Error::last_os_error();
            let _ = libc::close(fd);
            return Err(error);
        }
        if libc::listen(fd, LISTEN_BACKLOG) != 0 {
            let error = io::Error::last_os_error();
            let _ = libc::close(fd);
            return Err(error);
        }
        Ok(TcpListener::from_raw_fd(fd))
    }
}

/// The Anthropic the Claude models go to: `$HUDDLE_ROUTER_ANTHROPIC_URL`, else Anthropic
/// itself. The override must name a scheme and a host.
pub(crate) fn anthropic_url(env: &Env) -> Result<hyper::Uri, String> {
    let raw = env
        .get("HUDDLE_ROUTER_ANTHROPIC_URL")
        .filter(|value| !value.is_empty())
        .unwrap_or(ANTHROPIC_DEFAULT);
    let url: hyper::Uri = raw
        .parse()
        .map_err(|error| format!("huddle-router: $HUDDLE_ROUTER_ANTHROPIC_URL: {error}"))?;
    if url.scheme_str().is_none() || url.host().is_none() {
        return Err(format!(
            "huddle-router: $HUDDLE_ROUTER_ANTHROPIC_URL: {raw} names no scheme or host"
        ));
    }
    Ok(url)
}

/// Makes the run dir, 0700 when missing, and its parent with it; a failure is logged and the
/// daemon still serves without record, lock and handover socket.
pub(crate) fn make_run_dir(run_dir: &Path, log: &Logger) {
    if let Err(error) = create_dir(run_dir, DIR_MODE) {
        log(format!("huddle-router: {error}"));
    }
}

/// The executable this process runs, resolved through symlinks; `None` when the system will not
/// name it.
fn executable_resolved() -> Option<PathBuf> {
    let path = std::env::current_exe().ok()?;
    Some(path.canonicalize().unwrap_or(path))
}

/// Writes the record: the pid to signal, the port to talk to, the binary that serves, when it
/// came up and the worker pid (0 when the daemon serves itself). A failure is logged and the router serves on.
/// What the record says about the worker under a supervising parent.
#[derive(Debug, Default, Clone)]
pub(crate) struct Workers {
    /// The worker's pid; 0 for a daemon with no worker.
    pub pid: u32,
    /// How many times the parent restarted the worker.
    pub restarts: u32,
    /// How the last worker ended, such as `signal 9`; empty before the first restart.
    pub last_restart: String,
}

pub(crate) fn write_record(
    path: &Path,
    port: u16,
    version: &str,
    executable: Option<&Path>,
    started: &str,
    workers: &Workers,
    log: &Logger,
) {
    let pid = std::process::id();
    let record = RouterRecord {
        pid,
        port,
        version: version.to_string(),
        executable: executable
            .map(|path| path.to_string_lossy().into_owned())
            .unwrap_or_default(),
        started: started.to_string(),
        start: start_token(pid).unwrap_or_default(),
        worker: workers.pid,
        restarts: workers.restarts,
        last_restart: workers.last_restart.clone(),
    };
    match toml::to_string(&record) {
        Ok(text) => {
            if let Err(error) = write_atomic(path, text.as_bytes(), FILE_MODE) {
                log(format!("huddle-router: {error}"));
            }
        }
        Err(error) => log(format!("huddle-router: {error}")),
    }
}

/// Starts one fill of the history database's missing usage from the session logs, on its own
/// thread, unless the last one still runs. The caller never waits for it. A failure is one log line.
fn fill_history(gate: &history_upkeep::FillGate, paths: &Paths, window_ms: u64, log: &Logger) {
    let db = paths.state_dir.join(HISTORY_FILE);
    let claude_dir = paths.claude_dir.clone();
    let now = u64::try_from(now_ms()).unwrap_or(0);
    let log = Arc::clone(log);
    gate.start(move |state| {
        if let Err(error) = history_upkeep::fill_with(state, &db, &claude_dir, now, window_ms) {
            log(format!("huddle-router: history fill: {error}"));
        }
    });
}

/// The daemon's periodic jobs with what each needs.
struct Jobs<'a> {
    reload_watches: &'a Arc<Mutex<(ProvidersWatch, DashboardHostsWatch)>>,
    dashboard_hosts_now: &'a RwLock<Vec<String>>,
    router: &'a Arc<Router>,
    rss_watch: &'a mut RssWatch,
    env: &'a Env,
    paths: &'a Paths,
    port: u16,
    executable: Option<&'a Path>,
    log: &'a Logger,
}

impl Runner for Jobs<'_> {
    async fn run(&mut self, job: Job) {
        match job {
            Job::Reload => {
                reload_tick(
                    self.reload_watches,
                    self.dashboard_hosts_now,
                    self.router,
                    self.log,
                )
                .await
            }
            Job::Rss => {
                rss_check(
                    self.rss_watch,
                    self.env,
                    self.paths,
                    self.port,
                    self.executable,
                    self.log,
                )
                .await
            }
            Job::Prune => prune_history(self.paths, self.log).await,
        }
    }
}

/// One reload of the provider and dashboard-host files, read off the async runtime, then applied.
async fn reload_tick(
    watches: &Arc<Mutex<(ProvidersWatch, DashboardHostsWatch)>>,
    dashboard_hosts_now: &RwLock<Vec<String>>,
    router: &Arc<Router>,
    log: &Logger,
) {
    let (hosts, action) = match tokio::task::spawn_blocking({
        let watches = Arc::clone(watches);
        let log = Arc::clone(log);
        move || {
            let mut watches = watches.lock().unwrap_or_else(|e| e.into_inner());
            let (providers, hosts) = &mut *watches;
            let hosts = hosts.tick(&*log);
            let action = providers.tick(&*log);
            (hosts, action)
        }
    })
    .await
    {
        Ok(answer) => answer,
        Err(error) => {
            log(format!("huddle-router: reload poll: {error}"));
            (Vec::new(), ReloadAction::Keep)
        }
    };
    *dashboard_hosts_now
        .write()
        .unwrap_or_else(|e| e.into_inner()) = hosts;
    if let ReloadAction::Swap(names) = action {
        router.reload(&names);
        log(format!("huddle-router: now serving {}", names.join(", ")));
    }
}

/// One prune of the history database, off the async runtime. A failure is one log line.
async fn prune_history(paths: &Paths, log: &Logger) {
    let db = paths.state_dir.join(HISTORY_FILE);
    let config = dashboard_hosts::config_path(paths);
    let now = u64::try_from(now_ms()).unwrap_or(0);
    let pruned =
        tokio::task::spawn_blocking(move || history_upkeep::prune(&db, &config, now)).await;
    match pruned {
        Ok(Ok(_)) => {}
        Ok(Err(error)) => log(format!("huddle-router: history prune: {error}")),
        Err(error) => log(format!("huddle-router: history prune: {error}")),
    }
}

/// One memory check: the resident size read off the runtime, the decision, and on a handover
/// the spawn of this binary as `router serve --takeover`. A read error is logged and the check
/// moves on; a spawn error is logged, the try still counts.
async fn rss_check(
    watch: &mut RssWatch,
    env: &Env,
    paths: &Paths,
    port: u16,
    executable: Option<&Path>,
    log: &Logger,
) {
    let pid = std::process::id();
    let rss = match tokio::task::spawn_blocking(move || read_rss(pid)).await {
        Ok(Ok(rss)) => rss,
        Ok(Err(error)) => {
            log(format!("huddle-router: RSS: {error}"));
            return;
        }
        Err(error) => {
            log(format!("huddle-router: RSS: {error}"));
            return;
        }
    };
    if let RssDecision::HandOver { lines } = watch.decide(rss, Instant::now()) {
        for line in lines {
            log(line);
        }
        let Some(executable) = executable else {
            log("huddle-router: handover: the executable is unknown".to_string());
            return;
        };
        let spawn = {
            let env = env.clone();
            let paths = paths.clone();
            let executable = executable.to_path_buf();
            let log = Arc::clone(log);
            tokio::task::spawn_blocking(move || {
                if let Err(error) = spawn_serve(
                    &env,
                    &paths,
                    port,
                    &executable,
                    &["--takeover", "--by", "memory ceiling"],
                ) {
                    log(format!("huddle-router: handover: {error}"));
                }
            })
            .await
        };
        if let Err(error) = spawn {
            log(format!("huddle-router: handover: {error}"));
        }
    }
}

/// Takes the router lock once the previous daemon lets go and holds it until the returned
/// receiver drops: a takeover start serves the port before it can prove it is the one daemon, and
/// the lock catches up with it. The waiting thread ends once it has the lock: the guard waits in
/// the channel, and dropping the receiver drops it, also when it arrives after the drop. It also
/// ends within one poll of the receiver dropping, before it tries the lock again, so a run that
/// is over creates no file. A wait that fails is logged, the daemon serves unheld.
fn hold_lock_behind_the_run(
    run_dir: PathBuf,
    log: Logger,
) -> tokio::sync::oneshot::Receiver<singleton::LockGuard> {
    let (guard_tx, guard_rx) = tokio::sync::oneshot::channel();
    tokio::task::spawn_blocking(move || {
        let path = run_dir.join(LOCK_FILE);
        let deadline = Instant::now() + TAKEOVER_LOCK_WAIT;
        loop {
            if guard_tx.is_closed() {
                return;
            }
            match singleton::lock(&path, ROUTER_NAME) {
                Ok(guard) => return drop(guard_tx.send(guard)),
                Err(error @ SingletonError::Held(_)) if Instant::now() >= deadline => {
                    return log(singleton_line(&error));
                }
                Err(SingletonError::Held(_)) => std::thread::sleep(LOCK_POLL),
                Err(error) => return log(singleton_line(&error)),
            }
        }
    });
    guard_rx
}

/// One log line for a lock failure.
pub(crate) fn singleton_line(error: &SingletonError) -> String {
    match error {
        SingletonError::Held(held) => format!("huddle-router: {held}"),
        SingletonError::Io { path, source } => {
            format!("huddle-router: {}: {source}", path.display())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;

    #[tokio::test]
    async fn l11_the_takeover_lock_is_held_by_the_receiver_and_freed_when_it_drops() {
        let home = TempHome::new();
        let run_dir = home.root().join("r");
        let path = run_dir.join(LOCK_FILE);
        let previous = singleton::lock(&path, "previous").expect("previous daemon's lock");
        let held = hold_lock_behind_the_run(run_dir, Arc::new(|_| {}));
        // The wait joins the queue behind the previous daemon.
        drop(previous);
        let deadline = Instant::now() + Duration::from_secs(10);
        while singleton::lock(&path, "probe").is_ok() {
            assert!(Instant::now() < deadline, "the lock was never taken");
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        // A second router cannot take it while the receiver lives.
        assert!(singleton::lock(&path, "second").is_err());
        drop(held);
        let deadline = Instant::now() + Duration::from_secs(10);
        while singleton::lock(&path, "after").is_err() {
            assert!(Instant::now() < deadline, "the lock was never freed");
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
    }
}
