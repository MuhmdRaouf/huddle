//! The suite guard: runs a test command under a private TMPDIR and fails on what it leaves.

use std::collections::hash_map::RandomState;
use std::fs;
use std::hash::{BuildHasher, Hasher};
use std::io::{self, Write};
use std::path::{Path, PathBuf};
use std::process::Command;

use crate::ports::LEASE_DIR;
use crate::temp_home::inside_git_tree;

const RUN_PREFIX: &str = "g";
const OWNER_FILE: &str = "owner.pid";

/// Runs `command` with TMPDIR set to a fresh directory under `temp_root`, then fails on anything
/// the command left behind. Answers the exit code the guard's caller exits with.
pub fn run_guarded(temp_root: &Path, command: &[String], out: &mut dyn Write) -> i32 {
    match guarded(temp_root, command, out) {
        Ok(code) => code,
        Err(err) => {
            let _ = writeln!(out, "guard: {err}");
            1
        }
    }
}

fn guarded(temp_root: &Path, command: &[String], out: &mut dyn Write) -> io::Result<i32> {
    if let Some(tree) = inside_git_tree(temp_root) {
        writeln!(
            out,
            "guard: the temp directory {} is inside the git work tree {}: unset TMPDIR, or point it outside every repository",
            temp_root.display(),
            tree.display()
        )?;
        return Ok(1);
    }
    sweep_stale(temp_root, out)?;
    let Some((program, args)) = command.split_first() else {
        writeln!(out, "guard: no command given")?;
        return Ok(2);
    };
    let run_dir = make_run_dir(temp_root)?;
    fs::write(run_dir.join(OWNER_FILE), std::process::id().to_string())?;
    let status = Command::new(program)
        .args(args)
        .env("TMPDIR", &run_dir)
        .status()?;
    let code = status.code().unwrap_or(1);
    let left = entries_left(&run_dir)?;
    let escaped = stop_processes_under(&run_dir, out)?;
    fs::remove_dir_all(&run_dir)?;
    if !left.is_empty() {
        writeln!(
            out,
            "guard: the suite left {} entries in TMPDIR ({} must end empty):",
            left.len(),
            run_dir.display()
        )?;
        for name in &left {
            writeln!(out, "  {name}")?;
        }
    }
    if !left.is_empty() || escaped {
        return Ok(1);
    }
    Ok(code)
}

fn make_run_dir(temp_root: &Path) -> io::Result<PathBuf> {
    for _ in 0..64 {
        let tag = RandomState::new().build_hasher().finish() as u32 & 0x00ff_ffff;
        let dir = temp_root.join(format!("{RUN_PREFIX}{tag:06x}"));
        match fs::create_dir(&dir) {
            Ok(()) => return Ok(dir),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(err),
        }
    }
    Err(io::Error::other("no unused name for a guard directory"))
}

fn entries_left(run_dir: &Path) -> io::Result<Vec<String>> {
    let mut names = Vec::new();
    for entry in fs::read_dir(run_dir)? {
        let name = entry?.file_name().to_string_lossy().to_string();
        if name != OWNER_FILE && name != LEASE_DIR {
            names.push(name);
        }
    }
    names.sort();
    Ok(names)
}

// A process whose ps line (command or environment) names the run directory can only come from
// this run: the directory has a fresh random name. Platform binaries hide their environment from
// ps on macOS, so a script under the run directory shows through its command line instead.
//
// A process can fork between the look at the process table and the kill, so the guard looks again
// after every kill until a look finds none.
fn stop_processes_under(run_dir: &Path, out: &mut dyn Write) -> io::Result<bool> {
    const LOOKS: usize = 50;
    let dir = run_dir.display().to_string();
    let mut escaped = false;
    for _ in 0..LOOKS {
        let found = processes_naming(&dir)?;
        if found.is_empty() {
            return Ok(escaped);
        }
        escaped = true;
        for (pid, shown) in found {
            writeln!(
                out,
                "guard: a process of this run outlived it: pid {pid}\n  {shown}"
            )?;
            // SAFETY: kill only sends a signal; the pid was read from ps a moment ago.
            let killed = unsafe { libc::kill(pid, libc::SIGKILL) };
            if killed == 0 {
                writeln!(out, "guard: pid {pid} killed")?;
            }
        }
        std::thread::sleep(std::time::Duration::from_millis(20));
    }
    Err(io::Error::other(format!(
        "processes of {dir} still start after {LOOKS} looks"
    )))
}

// The pid and the start of the ps line of every other process whose command or environment names
// `dir` as a whole path: followed by `/`, whitespace or the line's end (an environment shows
// `TMPDIR=<dir>` with nothing after it).
pub(crate) fn processes_naming(dir: &str) -> io::Result<Vec<(i32, String)>> {
    let listing = Command::new("ps")
        .args(["axeww", "-o", "pid=,command="])
        .output()?;
    if !listing.status.success() {
        return Err(io::Error::other("ps failed"));
    }
    let text = String::from_utf8_lossy(&listing.stdout);
    let mut found = Vec::new();
    for line in text.lines() {
        if !names_path(line, dir) {
            continue;
        }
        let trimmed = line.trim_start();
        let Some((pid_text, rest)) = trimmed.split_once(char::is_whitespace) else {
            continue;
        };
        let Ok(pid) = pid_text.parse::<i32>() else {
            continue;
        };
        if pid <= 1 || pid == std::process::id() as i32 {
            continue;
        }
        found.push((pid, rest.chars().take(120).collect()));
    }
    Ok(found)
}

fn names_path(line: &str, dir: &str) -> bool {
    line.match_indices(dir).any(|(at, _)| {
        line[at + dir.len()..]
            .chars()
            .next()
            .is_none_or(|next| next == '/' || next.is_whitespace())
    })
}

// Removes the run directories a dead guard left, after stopping their processes.
fn sweep_stale(temp_root: &Path, out: &mut dyn Write) -> io::Result<()> {
    for entry in fs::read_dir(temp_root)? {
        let path = entry?.path();
        let is_run = path
            .file_name()
            .and_then(|name| name.to_str())
            .is_some_and(|name| {
                name.starts_with(RUN_PREFIX)
                    && name.len() == RUN_PREFIX.len() + 6
                    && name[RUN_PREFIX.len()..]
                        .bytes()
                        .all(|b| b.is_ascii_hexdigit())
            });
        if is_run && owner_is_dead(&path) {
            let _ = stop_processes_under(&path, out);
            writeln!(
                out,
                "guard: removing a dead run's folder {}",
                path.display()
            )?;
            let _ = fs::remove_dir_all(&path);
        }
    }
    Ok(())
}

fn owner_is_dead(dir: &Path) -> bool {
    let Ok(text) = fs::read_to_string(dir.join(OWNER_FILE)) else {
        return false;
    };
    let Ok(pid) = text.trim().parse::<i32>() else {
        return false;
    };
    if pid <= 1 {
        return false;
    }
    // SAFETY: signal 0 checks existence and delivers nothing.
    let result = unsafe { libc::kill(pid, 0) };
    result == -1 && io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH)
}
