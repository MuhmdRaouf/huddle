//! Writing a default file once: setup creates each config file it owns, never overwriting one.

use std::fs::{self, DirBuilder};
use std::io::{self, Write as _};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt};
use std::path::Path;

/// The mode of a folder huddle creates.
pub const DIR_MODE: u32 = 0o755;
/// The mode of a file huddle creates.
pub const FILE_MODE: u32 = 0o644;
/// The mode of the CA folder.
pub const PRIVATE_DIR_MODE: u32 = 0o700;
/// The mode of a file that holds a key or a copy of the user's settings.
pub const PRIVATE_FILE_MODE: u32 = 0o600;

/// Creates `path` and any missing parent with exactly `mode`, whatever the umask. A folder that
/// exists keeps its mode.
pub fn create_dir(path: &Path, mode: u32) -> io::Result<()> {
    let mut missing = Vec::new();
    for dir in path.ancestors() {
        if dir.as_os_str().is_empty() || dir.is_dir() {
            break;
        }
        missing.push(dir);
    }
    for dir in missing.into_iter().rev() {
        match DirBuilder::new().mode(mode).create(dir) {
            Ok(()) => fs::set_permissions(dir, fs::Permissions::from_mode(mode))?,
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => {}
            Err(err) => return Err(err),
        }
    }
    Ok(())
}

/// Writes `text` to `path`, creating its folder, when no file is there. True when it wrote.
pub fn write_if_missing(path: &Path, text: &str) -> io::Result<bool> {
    if let Some(parent) = path.parent() {
        create_dir(parent, DIR_MODE)?;
    }
    match fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(FILE_MODE)
        .open(path)
    {
        Ok(mut file) => {
            file.write_all(text.as_bytes())?;
            Ok(true)
        }
        Err(err) if err.kind() == io::ErrorKind::AlreadyExists => Ok(false),
        Err(err) => Err(err),
    }
}
