//! The release binary size check against release/size-limit.

use std::env;
use std::ffi::OsStr;
use std::fmt;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};

const LIMIT_FILE: &str = "release/size-limit";

pub const USAGE: &str = "usage: huddle-release size <binary>...";

/// One binary measured against the limit.
pub struct Checked {
    pub path: PathBuf,
    pub size: u64,
    pub over: bool,
}

/// Every expected failure of the size word.
pub enum Error {
    Usage,
    CurrentDir,
    LimitMissing(PathBuf),
    LimitRead(PathBuf),
    LimitParse(PathBuf),
    CannotRead(PathBuf),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Usage => f.write_str(USAGE),
            Error::CurrentDir => f.write_str("huddle-release: cannot read the current directory"),
            Error::LimitMissing(from) => {
                write!(
                    f,
                    "huddle-release: no {LIMIT_FILE} above {}",
                    from.display()
                )
            }
            Error::LimitRead(path) | Error::CannotRead(path) => {
                write!(f, "huddle-release: cannot read {}", path.display())
            }
            Error::LimitParse(path) => write!(f, "huddle-release: cannot parse {}", path.display()),
        }
    }
}

/// The size of every binary against the limit found from the current directory upward. A missing
/// or unreadable binary stops the check.
pub fn check(binaries: &[&OsStr]) -> Result<(u64, Vec<Checked>), Error> {
    if binaries.is_empty() {
        return Err(Error::Usage);
    }
    let start = env::current_dir().map_err(|_| Error::CurrentDir)?;
    let limit = limit_from(&start)?;
    let mut checked = Vec::new();
    for path in binaries {
        let path = PathBuf::from(path);
        let meta = fs::metadata(&path).map_err(|_| Error::CannotRead(path.clone()))?;
        if !meta.is_file() {
            return Err(Error::CannotRead(path));
        }
        let size = meta.len();
        checked.push(Checked {
            over: size > limit,
            size,
            path,
        });
    }
    Ok((limit, checked))
}

/// The byte limit: release/size-limit in the nearest directory at or above `start`.
fn limit_from(start: &Path) -> Result<u64, Error> {
    for dir in start.ancestors() {
        let file = dir.join(LIMIT_FILE);
        match fs::read_to_string(&file) {
            Ok(text) => {
                return text
                    .trim()
                    .parse::<u64>()
                    .map_err(|_| Error::LimitParse(file));
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => continue,
            Err(_) => return Err(Error::LimitRead(file)),
        }
    }
    Err(Error::LimitMissing(start.to_path_buf()))
}
