//! huddle-procs: live. Whether a recorded pid is still the same process, and
//! which binary path still exists on disk.

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

/// A process answers signal 0 without receiving anything: the call only probes
/// the right to signal the pid.
pub fn is_alive(pid: u32) -> bool {
    if pid == 0 {
        return false;
    }
    // SAFETY: `kill` is a plain syscall wrapper; it reads no memory and only
    // sets errno on failure.
    match unsafe { libc::kill(pid as libc::pid_t, 0) } {
        0 => true,
        -1 => std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM),
        _ => false,
    }
}

/// 5 is SZOMB in the BSD process status: exited, not yet reaped.
#[cfg(target_os = "macos")]
const PROC_STATUS_ZOMBIE: u32 = 5;

#[cfg(target_os = "macos")]
pub fn start_token(pid: u32) -> Option<String> {
    // SAFETY: `zeroed` fills a plain-data C struct before the kernel writes it.
    let mut info: libc::proc_bsdinfo = unsafe { std::mem::zeroed() };
    let size = std::mem::size_of_val(&info);
    // SAFETY: `proc_pidinfo` writes `size` bytes into `info` and returns the
    // byte count it wrote.
    let written = unsafe {
        libc::proc_pidinfo(
            pid as libc::c_int,
            libc::PROC_PIDTBSDINFO,
            0,
            &mut info as *mut libc::proc_bsdinfo as *mut libc::c_void,
            size as libc::c_int,
        )
    };
    if written != size as libc::c_int {
        return None;
    }
    if info.pbi_status == PROC_STATUS_ZOMBIE {
        return None;
    }
    Some(format!(
        "{}.{}",
        info.pbi_start_tvsec, info.pbi_start_tvusec
    ))
}

#[cfg(target_os = "linux")]
pub fn start_token(pid: u32) -> Option<String> {
    let stat = fs::read_to_string(format!("/proc/{pid}/stat")).ok()?;
    // The command name may hold spaces and parentheses, so the state field
    // starts after the LAST closing parenthesis.
    let after = stat.rsplit_once(')')?.1;
    let fields: Vec<&str> = after.trim_start().split(' ').collect();
    let state = *fields.first()?;
    if state == "Z" || state == "X" {
        return None;
    }
    // Field 22 of the file sits at index 19 here: fields 1 and 2 are before
    // the split.
    fields.get(19).map(|token| (*token).to_string())
}

/// True only when the pid is alive and still the process the token was taken
/// from. An empty token never matches.
pub fn process_alive(pid: u32, start_token: &str) -> bool {
    // The parameter shadows the function name, so the call is path-qualified.
    !start_token.is_empty()
        && is_alive(pid)
        && self::start_token(pid).as_deref() == Some(start_token)
}

/// The binary path a driver should run: the installed `huddle` link when it
/// points at an executable file, else this process's own executable, else the
/// raw argv path.
pub fn live_executable(bin_dir: &Path) -> PathBuf {
    let installed = bin_dir.join("huddle");
    if let Ok(real) = fs::canonicalize(&installed)
        && is_executable_file(&real)
    {
        return real;
    }
    if let Ok(exe) = std::env::current_exe()
        && is_executable_file(&exe)
    {
        return exe;
    }
    PathBuf::from(std::env::args().next().unwrap_or_default())
}

fn is_executable_file(path: &Path) -> bool {
    match fs::metadata(path) {
        Ok(meta) => meta.is_file() && meta.permissions().mode() & 0o111 != 0,
        Err(_) => false,
    }
}
