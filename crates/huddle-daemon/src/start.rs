//! huddle-daemon: start. One probe, one spawn shape and one report line serve every caller that
//! needs the daemon: a health GET within 300 ms names a running one, a start that must act
//! spawns this binary as `router serve` detached, and a test binary is never started as a
//! daemon. All of it is synchronous: it runs in a word or a hook, never on an async task.

use std::fmt;
use std::fs;
use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::thread;
use std::time::{Duration, Instant};

use huddle_catalog::providers::provider_ids;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_config::providers_file::read_enabled;
use huddle_procs::detached::{DetachError, DetachSpec, start_detached_pid};
use huddle_procs::live::is_alive;

use crate::nochain::{PROXY_VARS, is_router_proxy};
use crate::serve::lock_path;
use crate::singleton::holder;
use huddle_config::files::{DIR_MODE, create_dir};

/// The probe every start begins with: a daemon answering within it is up.
pub const HEALTH_WAIT: Duration = Duration::from_millis(300);

/// How often a start re-probes while its daemon comes up.
pub const SPAWN_POLL: Duration = Duration::from_millis(50);

/// How long a start waits for a spawned daemon's health answer.
pub const DEFAULT_BUDGET: Duration = Duration::from_secs(10);

/// How long a start waits for the takeover it asked for to publish its record.
pub const UPDATE_WAIT: Duration = Duration::from_secs(10);

/// The most of the health answer the probe takes: a dozen components with 200-byte errors all
/// fit, and a flood is not a router.
const HEALTH_ANSWER_MAX: usize = 64 * 1024;

/// Set to a non-empty value, code running in a test refuses to start the daemon.
pub const NO_DAEMON_ENV: &str = "HUDDLE_NO_DAEMON";

/// The file under the router dir the spawned daemon's output appends to.
const LOG_NAME: &str = "router.log";

/// The record under the run dir, `<run_dir>/router.toml`.
const RECORD_NAME: &str = "router.toml";

/// The running router's health answer, as the probe reads it.
#[derive(Debug, Clone, serde::Deserialize, PartialEq, Eq)]
pub struct HealthAnswer {
    pub name: String,
    pub providers: Vec<String>,
}

/// The record the running daemon publishes; the serve loop writes it.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct RouterRecord {
    pub pid: u32,
    pub port: u16,
    pub version: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub executable: String,
    pub started: String,
    /// The worker process serving requests under the pid above; 0 for a daemon with no worker.
    #[serde(default, skip_serializing_if = "is_zero")]
    pub worker: u32,
    /// How many times the parent restarted the worker.
    #[serde(default)]
    pub restarts: u32,
    /// How the last restarted worker ended, such as `signal 9`; empty before the first restart.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub last_restart: String,
}

fn is_zero(value: &u32) -> bool {
    *value == 0
}

/// What a start can refuse. `Refused` and `Spawn` carry their whole line; the rest get their
/// `huddle-router: ` prefix from Display.
#[derive(Debug)]
pub enum StartError {
    /// A raw port value that is no port. The caller was meant to take the port from
    /// `huddle_config::paths::router_port` and map its error here.
    BadPort { raw: String },
    /// The router log or its directory could not take the daemon's output.
    Log {
        path: PathBuf,
        source: std::io::Error,
    },
    /// providers.toml does not hold up; nothing is spawned.
    ProvidersFile(String),
    /// The daemon could not be started.
    Spawn(String),
    /// The spawn was refused before anything ran.
    Refused(String),
    /// The spawned daemon is alive but never answered health within the budget.
    NeverAnswered { port: u16, log: PathBuf },
    /// The spawned daemon exited before it answered health.
    Exited { log: PathBuf },
}

impl fmt::Display for StartError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            StartError::BadPort { raw } => {
                write!(
                    f,
                    "huddle-router: $HUDDLE_ROUTER_PORT ({raw:?}) is not a port"
                )
            }
            StartError::Log { path, source } => {
                write!(f, "huddle-router: {}: {source}", path.display())
            }
            StartError::ProvidersFile(text) => write!(f, "huddle-router: {text}"),
            StartError::Spawn(text) => write!(f, "{text}"),
            StartError::Refused(text) => write!(f, "{text}"),
            StartError::NeverAnswered { port, log } => write!(
                f,
                "huddle-router: the daemon never answered at http://127.0.0.1:{port} (its log: {})",
                log.display()
            ),
            StartError::Exited { log } => write!(
                f,
                "huddle-router: the daemon exited before it answered (its log: {})",
                log.display()
            ),
        }
    }
}

impl std::error::Error for StartError {}

/// The shape of one daemon start.
pub struct StartOptions<'a> {
    /// The environment the port override and the daemon's own environment come from.
    pub env: &'a Env,
    /// The state roots; the daemon's working directory and log live in `state_dir`.
    pub paths: &'a Paths,
    /// The port the daemon serves, from `huddle_config::paths::router_port`.
    pub port: u16,
    /// The binary the daemon is spawned from.
    pub executable: PathBuf,
    /// How long to wait for the spawned daemon's health answer; 0 still bounds one probe.
    pub budget: Duration,
}

/// The running router's health answer, `None` while nothing answers naming huddle-router
/// within `within`: not the daemon on a foreign name, an error status, a non-JSON body, or a
/// free port. A plain socket, so no proxy environment applies.
pub fn health(port: u16, within: Duration) -> Option<HealthAnswer> {
    let address = SocketAddr::from(([127, 0, 0, 1], port));
    let mut stream = TcpStream::connect_timeout(&address, within).ok()?;
    stream.set_read_timeout(Some(within)).ok()?;
    stream.set_write_timeout(Some(within)).ok()?;
    let request = format!(
        "GET /huddle-router/health HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(request.as_bytes()).ok()?;
    let mut answer = Vec::new();
    let mut buffer = [0u8; 4096];
    loop {
        match stream.read(&mut buffer) {
            Ok(0) => break,
            Ok(read) => answer.extend_from_slice(&buffer[..read]),
            Err(_) => return None,
        }
        if answer.len() > HEALTH_ANSWER_MAX + 4096 {
            return None;
        }
    }
    up_answer(&answer)
}

/// Whether a raw HTTP answer is status 200 with a JSON body whose `name` is exactly
/// huddle-router. A 200 alone is any web server; the name is ours.
fn up_answer(answer: &[u8]) -> Option<HealthAnswer> {
    let text = std::str::from_utf8(answer).ok()?;
    let (head, body) = text.split_once("\r\n\r\n")?;
    if !head.starts_with("HTTP/1.1 200") {
        return None;
    }
    let parsed: HealthAnswer = serde_json::from_str(body).ok()?;
    (parsed.name == "huddle-router").then_some(parsed)
}

/// The one line a script reads: where the router answers and what it serves.
pub fn up_line(port: u16, answer: &HealthAnswer) -> String {
    if answer.providers.is_empty() {
        format!("huddle-router: http://127.0.0.1:{port}")
    } else {
        format!(
            "huddle-router: http://127.0.0.1:{port} ({})",
            answer.providers.join(", ")
        )
    }
}

/// The record the running daemon publishes, `None` when the file is missing or malformed.
pub fn read_record(paths: &Paths) -> Option<RouterRecord> {
    let path = paths.run_dir.join(RECORD_NAME);
    let text = fs::read_to_string(path).ok()?;
    toml::from_str(&text).ok()
}

/// Equal strings, or both paths resolve to one file; a path that no longer resolves names only
/// itself.
pub fn same_binary(a: &Path, b: &Path) -> bool {
    if a == b {
        return true;
    }
    match (a.canonicalize(), b.canonicalize()) {
        (Ok(resolved_a), Ok(resolved_b)) => resolved_a == resolved_b,
        _ => false,
    }
}

/// True when `starter` may replace the running daemon: there is no `<bin_dir>/huddle`
/// link (a local cargo build), or `starter` resolves to the file the link names.
pub fn may_take_over(paths: &Paths, starter: &Path) -> bool {
    match paths.bin_dir.join("huddle").canonicalize() {
        Ok(installed) => same_binary(starter, &installed),
        Err(_) => true,
    }
}

/// Whether an executable is a Cargo test harness: a file named `<base>-<16 hex>` under a
/// `deps` directory. Started as a daemon it re-runs its own suite, detached and with the
/// environment of the code that reached here, so every daemon spawn refuses one.
pub fn is_test_harness(executable: &Path) -> bool {
    let Some(name) = executable.file_name().and_then(|name| name.to_str()) else {
        return false;
    };
    let Some((_, hash)) = name.rsplit_once('-') else {
        return false;
    };
    hash.len() == 16
        && hash.bytes().all(|byte| byte.is_ascii_hexdigit())
        && executable
            .parent()
            .and_then(|parent| parent.file_name())
            .and_then(|parent| parent.to_str())
            == Some("deps")
}

/// Runs the daemon detached: `<exe> router serve` with `extra_args`, its output appended to
/// the router log (created 0600, its directory 0700), its working directory the router dir, so
/// a caller in a deleted directory cannot stop it. A test build, a `HUDDLE_NO_DAEMON`
/// environment and a Cargo harness path are refused before anything runs. The answer is the
/// daemon's pid.
pub fn spawn_serve(
    env: &Env,
    paths: &Paths,
    port: u16,
    executable: &Path,
    extra_args: &[&str],
) -> Result<u32, StartError> {
    if cfg!(test) {
        return Err(refusal(&executable.display().to_string()));
    }
    if env
        .get(NO_DAEMON_ENV)
        .is_some_and(|value| !value.is_empty())
    {
        return Err(StartError::Refused(
            "huddle-router: HUDDLE_NO_DAEMON is set; the daemon is not started".to_string(),
        ));
    }
    if is_test_harness(executable) {
        let base = executable
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("the executable")
            .to_string();
        return Err(refusal(&base));
    }
    let log = paths.state_dir.join(LOG_NAME);
    create_dir(&paths.state_dir, DIR_MODE).map_err(|source| StartError::Log {
        path: log.clone(),
        source,
    })?;
    let spec = DetachSpec {
        program: executable.to_path_buf(),
        args: std::iter::once("router")
            .chain(std::iter::once("serve"))
            .chain(extra_args.iter().copied())
            .map(str::to_string)
            .collect(),
        env: daemon_env(env, port),
        log_file: Some(log),
        cwd: paths.state_dir.clone(),
    };
    start_detached_pid(&spec).map_err(|error| match error {
        DetachError::Log { path, source } => StartError::Log { path, source },
        DetachError::Spawn { program, source } => StartError::Spawn(format!(
            "huddle-router: could not start {} ({source})",
            program.display()
        )),
    })
}

/// The refusal a test binary earns.
fn refusal(base: &str) -> StartError {
    StartError::Refused(format!(
        "huddle-router: {base} is a test binary, not huddle; the daemon is not started"
    ))
}

/// The daemon's environment: the caller's own, so the overrides and keys it resolved reach the
/// lanes, minus every proxy variable that names a router of ours — a daemon that routes through
/// a router chains. The pure check only; the daemon re-checks each loopback value with a health
/// probe once it serves.
fn daemon_env(env: &Env, port: u16) -> Vec<(String, String)> {
    env.pairs()
        .into_iter()
        .filter(|(name, value)| {
            !(PROXY_VARS.contains(&name.as_str()) && is_router_proxy(value, port))
        })
        .collect()
}

/// A start's answer when a healthy daemon already serves: a record naming another binary this
/// one may replace is a takeover — the daemon spawns with `--takeover`, and the start polls
/// the record until it names a pid that differs from the old one, up to [`UPDATE_WAIT`]. The
/// same binary, an unknown one, or a start that may not replace the daemon is the quiet
/// address line as always.
fn updated_or_up(
    opts: &StartOptions<'_>,
    answer: &HealthAnswer,
    by: &str,
) -> Result<String, StartError> {
    let Some(record) = read_record(opts.paths) else {
        return Ok(up_line(opts.port, answer));
    };
    if record.executable.is_empty()
        || same_binary(Path::new(&record.executable), &opts.executable)
        || !may_take_over(opts.paths, &opts.executable)
    {
        return Ok(up_line(opts.port, answer));
    }
    let old = record.pid;
    spawn_serve(
        opts.env,
        opts.paths,
        opts.port,
        &opts.executable,
        &["--takeover", "--by", by],
    )?;
    let deadline = Instant::now() + UPDATE_WAIT;
    loop {
        if let Some(fresh) = read_record(opts.paths)
            && fresh.pid > 0
            && fresh.pid != old
        {
            return Ok(format!(
                "huddle-router: updated (pid {old} → {})",
                fresh.pid
            ));
        }
        if Instant::now() >= deadline {
            return Err(StartError::Spawn(format!(
                "huddle-router: the daemon (pid {old}) never handed over to {} (its log: {})",
                opts.executable.display(),
                opts.paths.state_dir.join(LOG_NAME).display(),
            )));
        }
        thread::sleep(SPAWN_POLL);
    }
}

/// Makes sure one daemon serves: a health answer within [`HEALTH_WAIT`] names a daemon that is
/// up, taken over when its record names a binary this start may replace; else one is spawned
/// detached and waited for up to the budget. The line names the address and the providers the
/// daemon serves; with no provider enabled the daemon still starts.
pub fn start(opts: &StartOptions<'_>) -> Result<String, StartError> {
    start_by(opts, "router start")
}

/// [`start`], naming who asks: the daemon it spawns logs the reason on its start line, and a
/// daemon asked to hand over logs it on its handover line.
pub fn start_by(opts: &StartOptions<'_>, by: &str) -> Result<String, StartError> {
    if let Some(answer) = health(opts.port, HEALTH_WAIT) {
        return updated_or_up(opts, &answer, by);
    }
    // An empty list still spawns: the router serves Anthropic with no provider enabled.
    let _providers = read_enabled(&opts.paths.config_dir, &provider_ids())
        .map_err(|error| StartError::ProvidersFile(error.to_string()))?;
    // A held router lock means a daemon is up or coming up: wait for its answer, spawn nothing.
    let router_lock = lock_path(opts.paths);
    let lock_held = || holder(&router_lock).is_some();
    let spawned = if lock_held() {
        None
    } else {
        Some(spawn_serve(
            opts.env,
            opts.paths,
            opts.port,
            &opts.executable,
            &["--by", by],
        )?)
    };
    let deadline = Instant::now() + opts.budget;
    loop {
        if let Some(answer) = health(opts.port, HEALTH_WAIT) {
            return Ok(up_line(opts.port, &answer));
        }
        // A daemon that lost the lock to a concurrent start exits while the winner comes up: the
        // exit is an error only when nobody holds the lock.
        if spawned.is_some_and(|pid| !is_alive(pid)) && !lock_held() {
            return Err(StartError::Exited {
                log: opts.paths.state_dir.join(LOG_NAME),
            });
        }
        if Instant::now() >= deadline {
            return Err(StartError::NeverAnswered {
                port: opts.port,
                log: opts.paths.state_dir.join(LOG_NAME),
            });
        }
        thread::sleep(SPAWN_POLL);
    }
}

/// Setup's router key line: the daemon's health answers once it serves the provider, waiting up
/// to `wait` for a reload that is still catching up. The key itself is setup's own `key` line.
pub fn router_key_check(
    provider: &str,
    display: &str,
    port: u16,
    wait: Duration,
) -> Result<String, StartError> {
    let deadline = Instant::now() + wait;
    loop {
        let answer = health(port, HEALTH_WAIT);
        if let Some(up) = answer.as_ref()
            && up.providers.iter().any(|name| name == provider)
        {
            return Ok(format!("huddle-router serves {display}"));
        }
        if Instant::now() >= deadline {
            return match answer {
                Some(up) => Err(StartError::Refused(format!(
                    "huddle-router serves {}, not {provider}",
                    up.providers.join(", ")
                ))),
                None => Err(StartError::Refused(format!(
                    "huddle-router is not running on port {port}"
                ))),
            };
        }
        thread::sleep(SPAWN_POLL);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::fs;

    #[test]
    fn a_test_build_can_never_spawn_the_daemon() {
        let env = Env::with(&[]);
        let dir = std::path::Path::new("/nonexistent");
        let paths_home = Env::with(&[("HOME", "/nonexistent")]);
        let paths = Paths::from_env(&paths_home).expect("paths");
        let result = spawn_serve(&env, &paths, 1, dir, &[]);
        assert!(matches!(result, Err(StartError::Refused(_))));
    }

    #[test]
    fn a_cargo_test_harness_path_is_recognised() {
        assert!(is_test_harness(Path::new(
            "/x/target/debug/deps/huddle_daemon-0123456789abcdef"
        )));
        assert!(is_test_harness(Path::new(
            "/x/target/release/deps/start_probe-0123456789abcdef"
        )));
        assert!(!is_test_harness(Path::new("/x/target/debug/huddle")));
        assert!(!is_test_harness(Path::new("/x/bin/huddle-1.2.3")));
        assert!(!is_test_harness(Path::new(
            "/x/deps/nothex-zzzzzzzzzzzzzzzz"
        )));
    }

    #[test]
    fn the_up_line_names_address_and_providers() {
        let answer = HealthAnswer {
            name: "huddle-router".into(),
            providers: vec!["zai".into(), "kimi".into()],
        };
        assert_eq!(
            up_line(4000, &answer),
            "huddle-router: http://127.0.0.1:4000 (zai, kimi)"
        );
    }

    #[test]
    fn read_record_answers_the_record_or_none() {
        let home = TempHome::new();
        let paths = Paths::from_env(&Env::with(&[("HOME", home.root().to_str().expect("utf8"))]))
            .expect("paths");
        assert_eq!(read_record(&paths), None);
        let run = paths.run_dir.clone();
        fs::create_dir_all(&run).expect("mkdir");
        let record = RouterRecord {
            pid: 42,
            port: 4835,
            version: "0.1.0".into(),
            executable: "/x/target/debug/huddle".into(),
            started: "2026-11-09T12:00:00Z".into(),
            worker: 0,
            restarts: 0,
            last_restart: String::new(),
        };
        fs::write(
            run.join("router.toml"),
            toml::to_string(&record).expect("toml"),
        )
        .expect("write");
        assert_eq!(read_record(&paths), Some(record));
        fs::write(run.join("router.toml"), "not toml =").expect("write");
        assert_eq!(read_record(&paths), None);
    }

    #[test]
    fn t2_the_record_is_router_toml_in_the_run_dir() {
        assert_eq!(RECORD_NAME, "router.toml");
    }
}
