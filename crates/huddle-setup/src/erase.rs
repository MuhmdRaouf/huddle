//! The last uninstall step: deletes the folders huddle owns. Each folder is checked before a
//! byte goes; one that is not clearly huddle's own is left in place and named.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use huddle_config::paths::Paths;

/// What the erase did with one folder.
#[derive(Debug, PartialEq, Eq)]
pub enum Erased {
    /// The folder is gone.
    Removed,
    /// The folder never existed.
    Absent,
    /// The folder is still there, for this reason.
    LeftInPlace(String),
}

/// The folders to erase, in order. The default runtime folder sits inside the state folder and
/// goes with it, so it is not listed then. The data folder comes last, since the running binary
/// lives in it.
pub fn folders(paths: &Paths) -> Vec<PathBuf> {
    let mut found = vec![paths.config_dir.clone(), paths.state_dir.clone()];
    found.push(paths.cache_dir.clone());
    if !paths.run_dir.starts_with(&paths.state_dir) {
        found.push(paths.run_dir.clone());
    }
    found.push(paths.data_dir.clone());
    found
}

/// Deletes `dir` and everything under it when it is a folder huddle owns.
pub fn erase(paths: &Paths, dir: &Path) -> Erased {
    if matches!(fs::symlink_metadata(dir), Err(error) if error.kind() == io::ErrorKind::NotFound) {
        return Erased::Absent;
    }
    if let Some(reason) = refusal(paths, dir) {
        return Erased::LeftInPlace(reason);
    }
    match fs::symlink_metadata(dir) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Erased::Absent,
        Err(error) => Erased::LeftInPlace(error.to_string()),
        Ok(meta) if meta.file_type().is_symlink() => {
            Erased::LeftInPlace("it is a link, not a folder".to_string())
        }
        Ok(meta) if !meta.is_dir() => Erased::LeftInPlace("it is not a folder".to_string()),
        // `remove_dir_all` unlinks a link inside the tree and never follows it.
        Ok(_) => match fs::remove_dir_all(dir) {
            Ok(()) => Erased::Removed,
            Err(error) => Erased::LeftInPlace(error.to_string()),
        },
    }
}

/// Why `dir` must not be deleted, or `None` when it may be.
fn refusal(paths: &Paths, dir: &Path) -> Option<String> {
    if dir.as_os_str().is_empty() || !dir.is_absolute() {
        return Some("the path is empty or relative".to_string());
    }
    if dir.parent().is_none() {
        return Some("it is the root folder".to_string());
    }
    // `HUDDLE_*_HOME` names the folder itself, so only a folder named huddle is provably ours.
    if dir.file_name().is_none_or(|name| name != "huddle") {
        return Some("it is not named huddle".to_string());
    }
    let shared_parents = [
        paths.home.join(".config"),
        paths.home.join(".local"),
        paths.home.join(".local/share"),
        paths.home.join(".local/state"),
        paths.home.join(".local/bin"),
        paths.home.join(".cache"),
        paths.claude_dir.clone(),
    ];
    // A folder that holds the home folder, or is one of the shared parents, is not huddle's.
    if paths.home.starts_with(dir) {
        return Some("it is the home folder or holds it".to_string());
    }
    if shared_parents.iter().any(|shared| shared.starts_with(dir)) {
        return Some("it holds folders that are not huddle's".to_string());
    }
    if paths.user_bin_dir.starts_with(dir) {
        return Some("it holds the user bin folder".to_string());
    }
    None
}
