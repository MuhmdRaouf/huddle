//! Every state path huddle uses, derived from one environment map.

use std::fmt;
use std::path::PathBuf;

use crate::env::Env;

/// The port huddle-router serves when no override names one.
pub const DEFAULT_ROUTER_PORT: u16 = 4835;

/// The state roots: `HOME` and its overrides, read once and passed down.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Paths {
    pub home: PathBuf,
    pub agents: PathBuf,
    pub router_dir: PathBuf,
    pub ca_dir: PathBuf,
    pub huddle_dir: PathBuf,
    pub jobs_root: PathBuf,
    pub bin_dir: PathBuf,
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
        let agents = home.join(".agents");
        let router_dir = set(env, "HUDDLE_ROUTER_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| agents.join("huddle-router"));
        let ca_dir = match router_dir.parent() {
            Some(parent) => parent.join("ca"),
            None => router_dir.join("ca"),
        };
        let huddle_dir = set(env, "HUDDLE_HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|| agents.join("huddle"));
        Ok(Paths {
            jobs_root: huddle_dir.join("jobs"),
            bin_dir: huddle_dir.join("bin"),
            claude_dir: set(env, "CLAUDE_CONFIG_DIR")
                .map(PathBuf::from)
                .unwrap_or_else(|| home.join(".claude")),
            home,
            agents,
            router_dir,
            ca_dir,
            huddle_dir,
        })
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
