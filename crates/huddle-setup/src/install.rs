//! The `setup --install` work: one CA, one router, one settings patch, one restart line.

use std::net::{SocketAddr, TcpStream};
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant, SystemTime};

use crate::env_file::RouterTarget;
use crate::settings::{self, SettingsError};
use huddle_config::env::Env;
use huddle_config::paths::Paths;

/// How long one port probe waits and how long all of them may take.
const PROBE_TIMEOUT: Duration = Duration::from_secs(1);
const PROBE_BUDGET: Duration = Duration::from_secs(3);
const PROBE_STEP: Duration = Duration::from_millis(50);

/// Why an install did not finish; nothing is written past the failed step.
#[derive(Debug)]
pub enum InstallError {
    Port(String),
    Router(String),
    Settings(SettingsError),
}

impl std::fmt::Display for InstallError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            InstallError::Port(message) => write!(f, "huddle setup: {message}"),
            InstallError::Router(message) => {
                write!(f, "huddle setup: the router did not start: {message}")
            }
            InstallError::Settings(error) => write!(f, "huddle setup: settings.json: {error}"),
        }
    }
}
impl std::error::Error for InstallError {}

/// What one install did: the patch's report lines and whether Claude Code must restart.
pub struct InstallReport {
    pub lines: Vec<String>,
    pub restart_needed: bool,
}

/// Loads or creates the local CA, starts the router, then patches settings.json. The router
/// comes before the patch, so a router that cannot start leaves settings.json untouched.
///
/// `exe` is the running binary (`std::env::current_exe`, resolved by the word).
pub fn run(env: &Env, paths: &Paths, exe: &Path) -> Result<InstallReport, InstallError> {
    let port = huddle_config::paths::router_port(env)
        .map_err(|error| InstallError::Port(error.to_string()))?;

    let ca_error = match huddle_pki::ca::load_or_create(&paths.ca_dir, SystemTime::now()) {
        Ok(_) => None,
        Err(error) => Some(error.to_string()),
    };

    start_router(exe)?;
    if !router_answers(port) {
        return Err(InstallError::Router(format!(
            "the router is not answering on port {port}"
        )));
    }

    let target = RouterTarget {
        port,
        router_url: format!("http://127.0.0.1:{port}"),
        ca_pem: huddle_pki::ca::ca_cert_path(&paths.ca_dir)
            .to_string_lossy()
            .into_owned(),
        process_https_proxy: process_https_proxy(env),
        ca_error,
    };
    let outcome = settings::apply_proxy(paths, &target).map_err(InstallError::Settings)?;
    Ok(InstallReport {
        lines: outcome.lines,
        restart_needed: outcome.changed,
    })
}

/// Runs `router start` as a child of this binary and waits for it. The daemon it spawns
/// inherits this process's environment.
fn start_router(exe: &Path) -> Result<(), InstallError> {
    let output = Command::new(exe)
        .args(["router", "start"])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .map_err(|error| InstallError::Router(error.to_string()))?;
    if !output.status.success() {
        return Err(InstallError::Router(
            String::from_utf8_lossy(&output.stderr).trim().to_string(),
        ));
    }
    Ok(())
}

/// True once something accepts connections on `127.0.0.1:port`.
fn router_answers(port: u16) -> bool {
    let addr = SocketAddr::from(([127, 0, 0, 1], port));
    let deadline = Instant::now() + PROBE_BUDGET;
    while Instant::now() < deadline {
        if TcpStream::connect_timeout(&addr, PROBE_TIMEOUT).is_ok() {
            return true;
        }
        std::thread::sleep(PROBE_STEP);
    }
    false
}

/// `https_proxy` then `HTTPS_PROXY` from huddle's own process env; empty counts as unset.
fn process_https_proxy(env: &Env) -> Option<String> {
    for name in ["https_proxy", "HTTPS_PROXY"] {
        if let Some(value) = env.get(name)
            && !value.is_empty()
        {
            return Some(value.to_string());
        }
    }
    None
}
