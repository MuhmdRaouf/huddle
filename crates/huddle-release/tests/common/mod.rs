//! A private directory per test and a clean environment for the tool it runs.

use std::ffi::OsString;
use std::fs::{self, DirBuilder};
use std::os::unix::fs::DirBuilderExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

const KEPT_FROM_PARENT: &[&str] = &["PATH", "LANG", "LC_ALL", "LC_CTYPE", "USER", "LOGNAME"];

/// A directory under the OS per-user temp directory, removed on drop.
pub struct TempDir {
    root: PathBuf,
}

/// Counts the directories this test binary makes, so parallel tests never draw the same name.
static SEQ: AtomicU64 = AtomicU64::new(0);

impl TempDir {
    #[allow(clippy::new_without_default)]
    pub fn new() -> TempDir {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or_default();
        let root = std::env::temp_dir().join(format!(
            "huddle-release-test-{}-{}-{}",
            std::process::id(),
            SEQ.fetch_add(1, Ordering::Relaxed),
            nanos
        ));
        DirBuilder::new().mode(0o700).create(&root).unwrap();
        TempDir { root }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

/// Runs the tool binary in `dir` with only the machine variables a test child keeps.
pub fn run(dir: &TempDir, args: &[OsString]) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_huddle-release"));
    command.args(args).current_dir(dir.root()).env_clear();
    for name in KEPT_FROM_PARENT {
        if let Some(value) = std::env::var_os(name) {
            command.env(name, value);
        }
    }
    command.output().unwrap()
}
