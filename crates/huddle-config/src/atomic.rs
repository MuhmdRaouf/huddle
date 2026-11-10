//! The one way a state file is written: a fresh temp file, an fsync, a rename.

use std::ffi::OsStr;
use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};

/// Writes `data` to `path` with `mode`, so a reader sees the old file or the new one and a
/// crash never leaves a torn file behind. Creates the parent directory (mode 0700, only when
/// this call creates it), writes a fresh temp file beside the target, fsyncs it, sets `mode`
/// over the umask, renames it over the target, then fsyncs the parent. A failure before the
/// rename removes the temp file and returns the error.
pub fn write_atomic(path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
    let parent = match path.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent.to_path_buf(),
        _ => PathBuf::from("."),
    };
    if !parent.is_dir() {
        fs::create_dir_all(&parent)?;
        set_mode(&parent, 0o700)?;
    }
    let (temp, mut file) = open_temp(&parent, path.file_name().unwrap_or_default())?;
    if let Err(err) = land(&temp, path, &mut file, data, mode) {
        let _ = fs::remove_file(&temp);
        return Err(err);
    }
    if let Ok(dir) = fs::File::open(&parent) {
        let _ = dir.sync_all();
    }
    Ok(())
}

/// Opens `.<name>.<pid>.<i>.tmp` in `parent` for the first free `i` in 0..100, mode 0600.
/// The pid keeps a crashed process's leftovers from spending this process's own names.
fn open_temp(parent: &Path, name: &OsStr) -> io::Result<(PathBuf, fs::File)> {
    let mut last = io::Error::other("no free temp name beside the target");
    for i in 0..100_u32 {
        let candidate = parent.join(format!(
            ".{}.{}.{}.tmp",
            name.display(),
            std::process::id(),
            i
        ));
        match OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&candidate)
        {
            Ok(file) => return Ok((candidate, file)),
            Err(err) if matches!(err.kind(), io::ErrorKind::AlreadyExists) => last = err,
            Err(err) => return Err(err),
        }
    }
    Err(last)
}

/// Writes, fsyncs, sets the mode over the umask and renames the temp file over the target.
fn land(temp: &Path, target: &Path, file: &mut fs::File, data: &[u8], mode: u32) -> io::Result<()> {
    file.write_all(data)?;
    file.sync_all()?;
    set_mode(temp, mode)?;
    fs::rename(temp, target)
}

fn set_mode(path: &Path, mode: u32) -> io::Result<()> {
    let mut permissions = fs::metadata(path)?.permissions();
    permissions.set_mode(mode);
    fs::set_permissions(path, permissions)
}
