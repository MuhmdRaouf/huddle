//! huddle-procs: detached. A child that leaves its parent: own session, re-parented to init,
//! output on a log or nowhere.

use std::fs::OpenOptions;
use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
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

/// Starts the child detached: own session, re-parented to init, never waited on.
pub fn start_detached(spec: &DetachSpec) -> Result<(), DetachError> {
    start_detached_pid(spec).map(|_| ())
}

/// Like `start_detached`, answering the child's pid so a caller can watch whether it is still
/// alive.
pub fn start_detached_pid(spec: &DetachSpec) -> Result<u32, DetachError> {
    start(spec, Stdio::null())
}

/// Like `start_detached`, with `input` as the child's stdin: a pipe the child reads, so a secret
/// reaches it without touching argv, the environment or a file. `input` must fit the pipe buffer.
pub fn start_detached_with_input(spec: &DetachSpec, input: &[u8]) -> Result<(), DetachError> {
    let spawn_error = |source| DetachError::Spawn {
        program: spec.program.clone(),
        source,
    };
    let (reader, mut writer) = io::pipe().map_err(spawn_error)?;
    writer.write_all(input).map_err(spawn_error)?;
    drop(writer);
    start(spec, Stdio::from(reader)).map(|_| ())
}

fn start(spec: &DetachSpec, stdin: Stdio) -> Result<u32, DetachError> {
    let spawn_error = |source| DetachError::Spawn {
        program: spec.program.clone(),
        source,
    };
    // The intermediate fork writes the grandchild's pid here before it exits. The pipe is
    // close-on-exec, so the grandchild's own copy goes away at its exec.
    let (mut pid_reader, pid_writer) = io::pipe().map_err(spawn_error)?;
    let pid_fd = pid_writer.as_raw_fd();
    let mut command = Command::new(&spec.program);
    command
        .args(&spec.args)
        .env_clear()
        .envs(spec.env.iter().cloned())
        .current_dir(&spec.cwd)
        .stdin(stdin);
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
    // SAFETY: fork, write, setsid and _exit are async-signal-safe. The closure touches nothing
    // else; `pid_fd` stays open until the parent drops `pid_writer` after the spawn.
    unsafe {
        command.pre_exec(move || {
            match libc::fork() {
                -1 => return Err(io::Error::last_os_error()),
                0 => {}
                grandchild => {
                    let bytes = (grandchild as u32).to_ne_bytes();
                    libc::write(pid_fd, bytes.as_ptr().cast(), bytes.len());
                    libc::_exit(0)
                }
            }
            if libc::setsid() == -1 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let mut child = command.spawn().map_err(spawn_error)?;
    drop(pid_writer);
    // The intermediate fork exits at once: this reaps it. `spawn` returned only after the
    // exec, so a bad program is already reported above.
    let _ = child.wait();
    let mut bytes = [0u8; 4];
    pid_reader.read_exact(&mut bytes).map_err(spawn_error)?;
    Ok(u32::from_ne_bytes(bytes))
}
