//! Finding a harness binary from the user's own environment: the overrides, PATH names, fixed
//! bundle paths, and the launch of a ZCode app-bundled script.

use crate::table::{Binary, Entry, Launcher};
use huddle_config::env::Env;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

/// The variable that points the bundled CLI at its built-in provider config.
const PROVIDER_CONFIG_VAR: &str = "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE";

/// How a found harness runs.
#[derive(Clone, Debug, PartialEq)]
pub struct Resolved {
    /// What to exec.
    pub program: PathBuf,
    /// Arguments placed before Run.args: the script, when a runner launches it.
    pub prefix_args: Vec<String>,
    /// Added to the child env only when a runner is used: launch_env and the provider config.
    pub env: Vec<(String, String)>,
    /// The binary as found (what /huddle:setup shows): the script, not the runner.
    pub path: PathBuf,
}

/// Why a harness binary could not be resolved.
#[derive(Clone, Debug, PartialEq)]
pub enum ResolveError {
    /// No PATH entry holds the name.
    NotOnPath { name: String },
    /// The candidate exists but is not a file that runs.
    NotExecutable { path: PathBuf },
    /// A script with neither the app's executable beside it nor node on PATH.
    NoRunner { script: PathBuf },
}

impl std::fmt::Display for ResolveError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ResolveError::NotOnPath { name } => write!(f, "{name} is not on PATH"),
            ResolveError::NotExecutable { path } => {
                write!(f, "{} is not an executable file", path.display())
            }
            ResolveError::NoRunner { script } => write!(
                f,
                "{} has no ZCode app executable beside it (Contents/MacOS/ZCode) and no node on PATH",
                script.display()
            ),
        }
    }
}

impl std::error::Error for ResolveError {}

/// The user's override variable for a harness: `HUDDLE_` + the uppercased id + `_BIN`
/// (`cursor-agent` becomes `HUDDLE_CURSOR_AGENT_BIN`).
pub fn override_var(id: &str) -> String {
    let upper: String = id
        .bytes()
        .map(|byte| {
            if byte.is_ascii_alphanumeric() {
                byte.to_ascii_uppercase() as char
            } else {
                '_'
            }
        })
        .collect();
    format!("HUDDLE_{upper}_BIN")
}

/// Finds a harness binary and how it starts. The candidate comes from `HUDDLE_<ID>_BIN`, then the
/// harness's own override variable, then PATH, then the fixed bundle paths. `PATH` and `HOME` come
/// from `env`, never from this process's own environment.
pub fn resolve(entry: &Entry, env: &Env) -> Result<Resolved, ResolveError> {
    let binary = &entry.binary;
    let candidate = if let Some(value) = non_empty(env.get(&override_var(&entry.id))) {
        override_candidate(value, env)?
    } else if let Some(value) = binary
        .env_var
        .as_deref()
        .and_then(|name| non_empty(env.get(name)))
    {
        override_candidate(value, env)?
    } else if let Some(found) = binary.names.iter().find_map(|name| on_path(env, name)) {
        found
    } else if let Some(found) = fixed_bundle_path(binary, env) {
        found
    } else {
        let name = binary.names.first().unwrap_or(&entry.id).clone();
        return Err(ResolveError::NotOnPath { name });
    };
    let candidate = path_candidate(candidate, &binary.launcher)?;
    // A GUI executable stands for the bundle's script: `zcode` on PATH can be a symlink into the app.
    let script = if binary.launcher == Launcher::ElectronOrNode {
        cli_script(&candidate).unwrap_or(candidate)
    } else {
        candidate
    };
    if binary.launcher != Launcher::ElectronOrNode || !is_script_name(&script) {
        return Ok(Resolved {
            program: script.clone(),
            prefix_args: Vec::new(),
            env: Vec::new(),
            path: script,
        });
    }
    if let Some(app) = electron_app(&script) {
        let mut run_env = binary.launch_env.clone();
        if env.get(PROVIDER_CONFIG_VAR).is_none()
            && let Some(config) = builtin_provider_config(&script)
        {
            run_env.push((
                PROVIDER_CONFIG_VAR.to_string(),
                config.display().to_string(),
            ));
        }
        return Ok(Resolved {
            program: app,
            prefix_args: vec![script.display().to_string()],
            env: run_env,
            path: script,
        });
    }
    let node = on_path(env, "node").ok_or_else(|| ResolveError::NoRunner {
        script: script.clone(),
    })?;
    Ok(Resolved {
        program: node,
        prefix_args: vec![script.display().to_string()],
        env: Vec::new(),
        path: script,
    })
}

fn non_empty(value: Option<&str>) -> Option<&str> {
    value.filter(|value| !value.is_empty())
}

/// An override value is a path when it names one, else a bare name looked up on PATH.
fn override_candidate(value: &str, env: &Env) -> Result<PathBuf, ResolveError> {
    if value.contains('/') {
        return Ok(PathBuf::from(value));
    }
    on_path(env, value).ok_or_else(|| ResolveError::NotOnPath {
        name: value.to_string(),
    })
}

/// A candidate that names a path must be a file that runs; a script needs only to be readable.
fn path_candidate(candidate: PathBuf, launcher: &Launcher) -> Result<PathBuf, ResolveError> {
    let ok = if *launcher == Launcher::ElectronOrNode && is_script_name(&candidate) {
        readable_file(&candidate)
    } else {
        is_executable_file(&candidate)
    };
    if ok {
        Ok(candidate)
    } else {
        Err(ResolveError::NotExecutable { path: candidate })
    }
}

/// The first PATH entry holding an executable file named `name`; empty entries keep the search going.
fn on_path(env: &Env, name: &str) -> Option<PathBuf> {
    let path = env.get("PATH")?;
    path.split(':')
        .filter(|dir| !dir.is_empty())
        .map(|dir| Path::new(dir).join(name))
        .find(|candidate| is_executable_file(candidate))
}

/// The first fixed install path for this OS that names a readable file. A `~/` prefix expands
/// against the env's HOME; with no HOME the path is skipped.
fn fixed_bundle_path(binary: &Binary, env: &Env) -> Option<PathBuf> {
    binary
        .fixed
        .iter()
        .filter(|fixed| fixed.os == std::env::consts::OS)
        .find_map(|fixed| {
            let path = home_expanded(&fixed.path, env)?;
            readable_file(&path).then_some(path)
        })
}

fn home_expanded(path: &str, env: &Env) -> Option<PathBuf> {
    match path.strip_prefix("~/") {
        Some(rest) => Some(Path::new(non_empty(env.get("HOME"))?).join(rest)),
        None => Some(PathBuf::from(path)),
    }
}

/// The bundle's CLI script when the candidate is the app's GUI executable: `zcode` on PATH can be a
/// symlink to `Contents/MacOS/ZCode`, whose CLI lives at `Contents/Resources/glm/zcode.cjs`.
fn cli_script(candidate: &Path) -> Option<PathBuf> {
    let resolved = fs::canonicalize(candidate).ok()?;
    let parent = resolved.parent()?;
    if parent.file_name()? != "MacOS" {
        return None;
    }
    let bundle = parent.parent()?;
    if bundle.file_name()? != "Contents" {
        return None;
    }
    let script = bundle.join("Resources/glm/zcode.cjs");
    readable_file(&script).then_some(script)
}

/// A bundle script: a name ending `.js`, `.cjs` or `.mjs`, any case.
fn is_script_name(path: &Path) -> bool {
    let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
        return false;
    };
    let lower = name.to_ascii_lowercase();
    lower.ends_with(".js") || lower.ends_with(".cjs") || lower.ends_with(".mjs")
}

/// The app's own executable beside a bundle script: `Contents/Resources/glm/zcode.cjs` runs on
/// `Contents/MacOS/ZCode` as plain node.
fn electron_app(script: &Path) -> Option<PathBuf> {
    let contents = script.parent()?.parent()?.parent()?;
    let app = contents.join("MacOS/ZCode");
    is_executable_file(&app).then_some(app)
}

/// The bundle's built-in provider config, under the script's own Resources.
fn builtin_provider_config(script: &Path) -> Option<PathBuf> {
    let resources = script.parent()?.parent()?;
    let config = resources.join("config/provider/zcode-builtin.json");
    is_regular_file(&config).then_some(config)
}

fn is_executable_file(path: &Path) -> bool {
    fs::metadata(path).is_ok_and(|meta| meta.is_file() && meta.permissions().mode() & 0o111 != 0)
}

fn is_regular_file(path: &Path) -> bool {
    fs::metadata(path).is_ok_and(|meta| meta.is_file())
}

/// Present, readable, a regular file. No executable bit is needed.
fn readable_file(path: &Path) -> bool {
    fs::File::open(path).is_ok_and(|file| file.metadata().is_ok_and(|meta| meta.is_file()))
}
