//! Executable files a test writes and then runs.

use std::io::{self, Write};
use std::path::Path;
use std::process::{Command, Stdio};

/// Writes `contents` to `path` with mode 755, from a child `sh`.
///
/// A test that writes the file itself holds a write fd for a moment. A fork in
/// another test thread inherits that fd until it execs, and an exec of the file
/// meanwhile fails with ETXTBSY. Here this process never opens the file for
/// writing, so no fork can inherit a writer.
pub fn write_executable(path: &Path, contents: &str) -> io::Result<()> {
    let mut child = Command::new("/bin/sh")
        .args(["-c", r#"cat > "$1" && chmod 755 "$1""#, "sh"])
        .arg(path)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()?;
    let written = match child.stdin.take() {
        Some(mut stdin) => stdin.write_all(contents.as_bytes()),
        None => Ok(()),
    };
    let out = child.wait_with_output()?;
    if !out.status.success() {
        return Err(io::Error::other(format!(
            "write {}: sh exited {}: {}",
            path.display(),
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        )));
    }
    written
}
