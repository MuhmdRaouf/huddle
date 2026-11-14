//! The token that guards the router's `/mcp`: 32 random bytes from the OS, as hex, in one file
//! of mode 0600 in the state folder. The router makes it at start when it is missing, setup
//! makes it before it starts the router, and `huddle mcp headers` hands it to Claude Code.

use std::fmt;
use std::fs::{self, File};
use std::io::{self, Read as _, Write as _};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

use crate::atomic::write_atomic;
use crate::files::{DIR_MODE, PRIVATE_FILE_MODE, create_dir};
use crate::paths::Paths;

const TOKEN_BYTES: usize = 32;
const BEARER: &str = "Bearer ";

/// Numbers the temp files of `ensure` calls made in this process.
static TEMP_CALLS: AtomicU64 = AtomicU64::new(0);

/// `<state_dir>/mcp-token`.
pub fn token_path(paths: &Paths) -> PathBuf {
    paths.state_dir.join("mcp-token")
}

/// The token. Its `Debug` hides the value, so a log line that formats it carries nothing.
#[derive(Clone, PartialEq, Eq)]
pub struct McpToken(String);

impl fmt::Debug for McpToken {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("McpToken(..)")
    }
}

/// Why the token file cannot be used.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TokenError {
    Missing,
    /// The file's mode lets someone else read it.
    NotPrivate(u32),
    Malformed,
    Unreadable(String),
}

impl fmt::Display for TokenError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            TokenError::Missing => write!(f, "missing"),
            TokenError::NotPrivate(mode) => write!(f, "readable by others (mode {mode:04o})"),
            TokenError::Malformed => write!(f, "malformed"),
            TokenError::Unreadable(error) => write!(f, "unreadable: {error}"),
        }
    }
}

impl std::error::Error for TokenError {}

impl McpToken {
    /// The token in `path`, when the file is private and holds one.
    pub fn read(path: &Path) -> Result<McpToken, TokenError> {
        let meta = match fs::metadata(path) {
            Ok(meta) => meta,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return Err(TokenError::Missing);
            }
            Err(error) => return Err(TokenError::Unreadable(error.to_string())),
        };
        let mode = meta.permissions().mode() & 0o777;
        if mode != PRIVATE_FILE_MODE {
            return Err(TokenError::NotPrivate(mode));
        }
        let text =
            fs::read_to_string(path).map_err(|error| TokenError::Unreadable(error.to_string()))?;
        let text = text.trim();
        if text.len() == TOKEN_BYTES * 2 && text.bytes().all(|b| b.is_ascii_hexdigit()) {
            Ok(McpToken(text.to_ascii_lowercase()))
        } else {
            Err(TokenError::Malformed)
        }
    }

    /// The token in `path`, made first when it is missing. Two callers racing to make it end
    /// with the same token. A file that is not private or holds no token is replaced.
    pub fn ensure(path: &Path) -> Result<McpToken, TokenError> {
        match McpToken::read(path) {
            Ok(token) => return Ok(token),
            Err(TokenError::Missing) => {}
            Err(TokenError::Unreadable(error)) => return Err(TokenError::Unreadable(error)),
            Err(TokenError::NotPrivate(_) | TokenError::Malformed) => {
                let token = McpToken::fresh()?;
                write_atomic(path, token.0.as_bytes(), PRIVATE_FILE_MODE)
                    .map_err(|error| TokenError::Unreadable(error.to_string()))?;
                return Ok(token);
            }
        }
        let token = McpToken::fresh()?;
        if let Some(parent) = path.parent() {
            create_dir(parent, DIR_MODE)
                .map_err(|error| TokenError::Unreadable(error.to_string()))?;
        }
        // A whole file under a temp name, then a hard link: the link fails when another caller
        // got there first, and nobody ever reads a half-written token.
        // The name is unique per call: the pid separates processes, the counter separates
        // callers inside one.
        let call = TEMP_CALLS.fetch_add(1, Ordering::Relaxed);
        let temp = path.with_extension(format!("{}.{call}.tmp", std::process::id()));
        // A crash can leave this name behind, and a recycled pid reuses it.
        let _ = fs::remove_file(&temp);
        let written = (|| {
            let mut file = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(PRIVATE_FILE_MODE)
                .open(&temp)?;
            file.write_all(token.0.as_bytes())?;
            file.sync_all()?;
            fs::hard_link(&temp, path)
        })();
        let _ = fs::remove_file(&temp);
        match written {
            Ok(()) => Ok(token),
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => McpToken::read(path),
            Err(error) => Err(TokenError::Unreadable(error.to_string())),
        }
    }

    fn fresh() -> Result<McpToken, TokenError> {
        let mut bytes = [0u8; TOKEN_BYTES];
        File::open("/dev/urandom")
            .and_then(|mut source| source.read_exact(&mut bytes))
            .map_err(|error| TokenError::Unreadable(format!("/dev/urandom: {error}")))?;
        Ok(McpToken(bytes.iter().map(|b| format!("{b:02x}")).collect()))
    }

    /// The `Authorization` header value a client sends.
    pub fn authorization(&self) -> String {
        format!("{BEARER}{}", self.0)
    }

    /// True when `header` is exactly this token's `Authorization` value. The comparison takes
    /// the same time wherever the first difference is.
    pub fn accepts(&self, header: &[u8]) -> bool {
        let expected = self.authorization();
        let expected = expected.as_bytes();
        if header.len() != expected.len() {
            return false;
        }
        let difference = header
            .iter()
            .zip(expected)
            .fold(0u8, |acc, (a, b)| acc | (a ^ b));
        difference == 0
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;

    #[test]
    fn h3_token_is_made_once_and_kept() {
        let home = TempHome::new();
        let path = home.root().join("state/mcp-token");
        let first = McpToken::ensure(&path).unwrap();
        let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600);
        assert_eq!(McpToken::ensure(&path).unwrap(), first);
        assert_eq!(McpToken::read(&path).unwrap(), first);
        let other = home.root().join("other/mcp-token");
        assert_ne!(McpToken::ensure(&other).unwrap(), first);
    }

    #[test]
    fn c2_stale_token_temp_does_not_block_ensure() {
        let home = TempHome::new();
        let path = home.root().join("state/mcp-token");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        let stale = path.with_extension(format!("{}.tmp", std::process::id()));
        fs::write(&stale, "left behind by a crash").unwrap();
        let token = McpToken::ensure(&path).unwrap();
        assert_eq!(McpToken::read(&path).unwrap(), token);
    }

    #[test]
    fn h3_a_readable_or_broken_token_is_replaced() {
        let home = TempHome::new();
        let path = home.root().join("mcp-token");
        let first = McpToken::ensure(&path).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();
        assert_eq!(McpToken::read(&path), Err(TokenError::NotPrivate(0o644)));
        let second = McpToken::ensure(&path).unwrap();
        assert_ne!(second, first);
        fs::write(&path, "short").unwrap();
        assert_eq!(McpToken::read(&path), Err(TokenError::Malformed));
        assert_ne!(McpToken::ensure(&path).unwrap(), second);
        assert_eq!(
            fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o600
        );
    }

    #[test]
    fn h3_accepts_only_the_exact_header() {
        let token = McpToken("ab".repeat(TOKEN_BYTES));
        assert!(token.accepts(token.authorization().as_bytes()));
        for wrong in [
            String::new(),
            "ab".repeat(TOKEN_BYTES),
            format!("Bearer {}", "ab".repeat(TOKEN_BYTES - 1)),
            format!("Bearer {}ac", "ab".repeat(TOKEN_BYTES - 1)),
            format!("bearer {}", "ab".repeat(TOKEN_BYTES)),
            format!("{}0", token.authorization()),
        ] {
            assert!(!token.accepts(wrong.as_bytes()), "{wrong:?}");
        }
        assert_eq!(format!("{token:?}"), "McpToken(..)");
    }
}
