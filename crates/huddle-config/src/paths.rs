//! Every state path huddle uses, derived from one environment map.

use std::fmt;
use std::path::{Path, PathBuf};

use crate::env::Env;

/// The port huddle-router serves when no override names one.
pub const DEFAULT_ROUTER_PORT: u16 = 4835;

/// The folders huddle uses, read once and passed down. Each kind (config, data, state, cache)
/// takes `HUDDLE_<KIND>_HOME`, else `$XDG_<KIND>_HOME/huddle`, else its XDG default under `HOME`.
/// A relative value is invalid and ignored, as the XDG Base Directory spec says.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Paths {
    pub home: PathBuf,
    /// User-edited config.
    pub config_dir: PathBuf,
    /// Installed binaries and the local CA.
    pub data_dir: PathBuf,
    /// Logs, ledgers, jobs and the running router's files.
    pub state_dir: PathBuf,
    pub bin_dir: PathBuf,
    /// `~/.local/bin`, the systemd file-hierarchy home for user executables: setup links `huddle` here.
    pub user_bin_dir: PathBuf,
    pub ca_dir: PathBuf,
    /// Files that can be rebuilt: the install's download and build log.
    pub cache_dir: PathBuf,
    /// The router record, lock and handover socket: `HUDDLE_RUNTIME_DIR`, else
    /// `$XDG_RUNTIME_DIR/huddle`, else `state_dir/run` (macOS sets no `XDG_RUNTIME_DIR`).
    pub run_dir: PathBuf,
    pub jobs_root: PathBuf,
    pub claude_dir: PathBuf,
}

/// What reading the paths can refuse.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PathsError {
    NoHome,
    BadRouterPort(String),
}

impl fmt::Display for PathsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            PathsError::NoHome => write!(f, "huddle: HOME is not set"),
            PathsError::BadRouterPort(raw) => {
                write!(
                    f,
                    "huddle-router: $HUDDLE_ROUTER_PORT ({raw:?}) is not a port"
                )
            }
        }
    }
}

impl std::error::Error for PathsError {}

impl Paths {
    pub fn from_env(env: &Env) -> Result<Paths, PathsError> {
        let home = PathBuf::from(set(env, "HOME").ok_or(PathsError::NoHome)?);
        let config_dir = kind_dir(env, &home, "CONFIG", ".config");
        let data_dir = kind_dir(env, &home, "DATA", ".local/share");
        let state_dir = kind_dir(env, &home, "STATE", ".local/state");
        let cache_dir = kind_dir(env, &home, "CACHE", ".cache");
        let run_dir = match (
            absolute(env, "HUDDLE_RUNTIME_DIR"),
            absolute(env, "XDG_RUNTIME_DIR"),
        ) {
            (Some(own), _) => PathBuf::from(own),
            (None, Some(xdg)) => PathBuf::from(xdg).join("huddle"),
            (None, None) => state_dir.join("run"),
        };
        Ok(Paths {
            bin_dir: data_dir.join("bin"),
            user_bin_dir: home.join(".local/bin"),
            ca_dir: data_dir.join("ca"),
            run_dir,
            cache_dir,
            jobs_root: state_dir.join("jobs"),
            claude_dir: set(env, "CLAUDE_CONFIG_DIR")
                .map(PathBuf::from)
                .unwrap_or_else(|| home.join(".claude")),
            config_dir,
            data_dir,
            state_dir,
            home,
        })
    }
}

/// One kind's folder: `HUDDLE_<KIND>_HOME`, else `$XDG_<KIND>_HOME/huddle`, else `HOME/<default>/huddle`.
fn kind_dir(env: &Env, home: &Path, kind: &str, default: &str) -> PathBuf {
    if let Some(own) = absolute(env, &format!("HUDDLE_{kind}_HOME")) {
        return PathBuf::from(own);
    }
    match absolute(env, &format!("XDG_{kind}_HOME")) {
        Some(xdg) => PathBuf::from(xdg).join("huddle"),
        None => home.join(default).join("huddle"),
    }
}

/// The port huddle-router serves: `$HUDDLE_ROUTER_PORT`, else `DEFAULT_ROUTER_PORT`. Only plain
/// digits in 1..=65535 count, and setup shares the rule, so the URL setup writes is one the
/// daemon binds.
pub fn router_port(env: &Env) -> Result<u16, PathsError> {
    let raw = match set(env, "HUDDLE_ROUTER_PORT") {
        None => return Ok(DEFAULT_ROUTER_PORT),
        Some(raw) => raw,
    };
    // Plain ASCII digits only: no space, sign, exponent or dot.
    if !raw.bytes().all(|byte| byte.is_ascii_digit()) {
        return Err(PathsError::BadRouterPort(raw.to_string()));
    }
    match raw.parse::<u16>() {
        Ok(port) if (1..=65535).contains(&port) => Ok(port),
        _ => Err(PathsError::BadRouterPort(raw.to_string())),
    }
}

/// The one address every caller points at: the router port, no trailing slash.
pub fn router_url(env: &Env) -> Result<String, PathsError> {
    Ok(format!("http://127.0.0.1:{}", router_port(env)?))
}

/// The variable's value when it is set and not empty; an empty value counts as unset.
fn set<'e>(env: &'e Env, name: &str) -> Option<&'e str> {
    env.get(name).filter(|value| !value.is_empty())
}

/// Like `set`, for a folder variable: a relative path is invalid and counts as unset.
fn absolute<'e>(env: &'e Env, name: &str) -> Option<&'e str> {
    set(env, name).filter(|value| Path::new(value).is_absolute())
}
