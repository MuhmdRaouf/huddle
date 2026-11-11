//! huddle-procs: detached. A child that leaves its parent: own session, re-parented to init,
//! output on a log or nowhere.

use std::fs::OpenOptions;
use std::io;
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::process::CommandExt;
use std::path::PathBuf;
use std::process::{Command, Stdio};

pub struct DetachSpec {
    pub program: PathBuf,
    pub args: Vec<String>,
    /// The child's whole environment. Nothing is inherited.
    pub env: Vec<(String, String)>,
    /// Where stdout and stderr are appended (created 0600). None sends them to /dev/null.
    pub log_file: Option<PathBuf>,
    /// The child's working directory. A caller in a deleted directory must not stop a daemon.
    pub cwd: PathBuf,
}

#[derive(Debug)]
pub enum DetachError {
    Log { path: PathBuf, source: io::Error },
    Spawn { program: PathBuf, source: io::Error },
}

/// Starts the child detached: own session, re-parented to init, never waited on. The caller
/// gets no pid: the daemon and the job driver write their own pid where it is needed.
pub fn start_detached(spec: &DetachSpec) -> Result<(), DetachError> {
    let mut command = Command::new(&spec.program);
    command
        .args(&spec.args)
        .env_clear()
        .envs(spec.env.iter().cloned())
        .current_dir(&spec.cwd)
        .stdin(Stdio::null());
    match &spec.log_file {
        Some(path) => {
            let stdout = OpenOptions::new()
                .append(true)
                .create(true)
                .mode(huddle_config::files::FILE_MODE)
                .open(path)
                .map_err(|source| DetachError::Log {
                    path: path.clone(),
                    source,
                })?;
            let stderr = stdout.try_clone().map_err(|source| DetachError::Log {
                path: path.clone(),
                source,
            })?;
            command.stdout(stdout).stderr(stderr);
        }
        None => {
            command.stdout(Stdio::null()).stderr(Stdio::null());
        }
    }
    // SAFETY: fork, setsid and _exit are async-signal-safe. The closure touches nothing else.
    unsafe {
        command.pre_exec(|| {
            match libc::fork() {
                -1 => return Err(io::Error::last_os_error()),
                0 => {}
                _ => libc::_exit(0),
            }
            if libc::setsid() == -1 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let mut child = command.spawn().map_err(|source| DetachError::Spawn {
        program: spec.program.clone(),
        source,
    })?;
    // The intermediate fork exits at once: this reaps it. `spawn` returned only after the
    // exec, so a bad program is already reported above.
    let _ = child.wait();
    Ok(())
}
