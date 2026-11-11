//! The TOML files a job keeps in its directory.

use huddle_config::atomic::write_atomic;
use serde::Serialize;
use serde::de::DeserializeOwned;
use std::fs;
use std::io;
use std::path::Path;

/// The file's value; a missing file is `Ok(None)`, unreadable or malformed is an error.
pub fn read<T: DeserializeOwned>(path: &Path) -> io::Result<Option<T>> {
    let text = match fs::read_to_string(path) {
        Ok(text) => text,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    toml::from_str(&text)
        .map(Some)
        .map_err(|error| io::Error::other(format!("{}: {error}", path.display())))
}

/// Writes the value as TOML, mode 0644, atomically.
pub fn write<T: Serialize>(path: &Path, value: &T) -> io::Result<()> {
    let text = toml::to_string(value).map_err(io::Error::other)?;
    write_atomic(path, text.as_bytes(), huddle_config::files::FILE_MODE)
}
