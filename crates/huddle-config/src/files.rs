//! Writing a default file once: setup creates each config file it owns, never overwriting one.

use std::fs;
use std::io::{self, Write as _};
use std::path::Path;

/// Writes `text` to `path`, creating its folder, when no file is there. True when it wrote.
pub fn write_if_missing(path: &Path, text: &str) -> io::Result<bool> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    match fs::OpenOptions::new()
        .write(true)
        .create_new(true)
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
