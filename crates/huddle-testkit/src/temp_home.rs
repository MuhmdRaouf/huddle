//! A private home directory for every test child.

use std::collections::hash_map::RandomState;
use std::ffi::OsStr;
use std::fs::{self, DirBuilder};
use std::hash::{BuildHasher, Hasher};
use std::io;
use std::os::unix::fs::DirBuilderExt;
use std::path::{Path, PathBuf};
use std::process::Command;

use crate::ports;

const KEPT_FROM_PARENT: &[&str] = &["PATH", "LANG", "LC_ALL", "LC_CTYPE", "USER", "LOGNAME"];

/// A directory under the OS per-user temp directory, removed on drop. Nothing here touches the
/// parent's environment: the variables reach only the children built by `command`.
pub struct TempHome {
    root: PathBuf,
    vars: Vec<(String, String)>,
    router_port: u16,
}

impl TempHome {
    /// Makes the directory, or panics: a test cannot run without it.
    #[allow(clippy::new_without_default)]
    pub fn new() -> TempHome {
        match TempHome::try_new() {
            Ok(home) => home,
            Err(err) => panic!("temp home: {err}"),
        }
    }

    fn try_new() -> io::Result<TempHome> {
        let temp = std::env::temp_dir();
        if let Some(tree) = inside_git_tree(&temp) {
            return Err(io::Error::other(format!(
                "the temp directory {} is inside the git work tree {}",
                temp.display(),
                tree.display()
            )));
        }
        let root = make_root(&temp)?;
        for dir in [
            ".agents/huddle",
            ".claude",
            ".config",
            ".local/state",
            ".local/share",
            ".cache",
            "tmp",
            "r",
        ] {
            fs::create_dir_all(root.join(dir))?;
        }
        let router_port = ports::free_port();
        let inherited: Vec<(String, String)> = KEPT_FROM_PARENT
            .iter()
            .filter_map(|name| Some((name.to_string(), std::env::var(name).ok()?)))
            .collect();
        let vars = child_env(&inherited, &root, router_port);
        Ok(TempHome {
            root,
            vars,
            router_port,
        })
    }

    /// The directory, spelled as HOME names it.
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// The directory with symlinks resolved: what git prints.
    pub fn canonical_root(&self) -> io::Result<PathBuf> {
        self.root.canonicalize()
    }

    /// The router port every child of this home is told to use.
    pub fn router_port(&self) -> u16 {
        self.router_port
    }

    /// The variables a child gets: `command.env_clear().envs(home.env())`.
    pub fn env(&self) -> Vec<(String, String)> {
        self.vars.clone()
    }

    /// A command that starts with exactly this home's variables.
    pub fn command<S: AsRef<OsStr>>(&self, program: S) -> Command {
        let mut command = Command::new(program);
        command.env_clear();
        command.envs(self.vars.iter().map(|(name, value)| (name, value)));
        command
    }
}

impl Drop for TempHome {
    fn drop(&mut self) {
        // A child's last writes can land while the tree is being removed; a
        // few short retries close that race without waiting on a live child.
        for _ in 0..20 {
            let removed = fs::remove_dir_all(&self.root).is_ok();
            std::thread::sleep(std::time::Duration::from_millis(25));
            if removed && !self.root.exists() {
                return;
            }
        }
    }
}

/// The full environment of a child rooted at `root`: the machine variables named in
/// `KEPT_FROM_PARENT`, then the private directories. Every other inherited variable is left out,
/// so owner keys, proxies and Claude Code session variables never reach a child.
pub fn child_env(
    inherited: &[(String, String)],
    root: &Path,
    router_port: u16,
) -> Vec<(String, String)> {
    let path = |rel: &str| root.join(rel).display().to_string();
    let mut vars: Vec<(String, String)> = inherited
        .iter()
        .filter(|(name, _)| KEPT_FROM_PARENT.contains(&name.as_str()))
        .cloned()
        .collect();
    for (name, value) in [
        ("HOME", root.display().to_string()),
        ("HUDDLE_HOME", path(".agents/huddle")),
        ("CLAUDE_CONFIG_DIR", path(".claude")),
        ("XDG_CONFIG_HOME", path(".config")),
        ("XDG_STATE_HOME", path(".local/state")),
        ("XDG_DATA_HOME", path(".local/share")),
        ("XDG_CACHE_HOME", path(".cache")),
        ("XDG_RUNTIME_DIR", path("r")),
        ("TMPDIR", path("tmp")),
        ("HUDDLE_KEYS", "files".to_string()),
        ("HUDDLE_ROUTER_PORT", router_port.to_string()),
    ] {
        vars.push((name.to_string(), value));
    }
    vars
}

/// The nearest folder at or above `dir` that holds a `.git` entry, after symlinks.
pub fn inside_git_tree(dir: &Path) -> Option<PathBuf> {
    let resolved = dir.canonicalize().unwrap_or_else(|_| dir.to_path_buf());
    resolved
        .ancestors()
        .find(|folder| folder.join(".git").symlink_metadata().is_ok())
        .map(Path::to_path_buf)
}

fn make_root(temp: &Path) -> io::Result<PathBuf> {
    for _ in 0..64 {
        let tag = RandomState::new().build_hasher().finish() as u32 & 0x00ff_ffff;
        let root = temp.join(format!("h{tag:06x}"));
        match DirBuilder::new().mode(0o700).create(&root) {
            Ok(()) => return Ok(root),
            Err(err) if err.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(err),
        }
    }
    Err(io::Error::other("no unused name for a temp home"))
}

impl TempHome {
    /// Runs git in `dir` and answers its trimmed stdout. Git sees no config of the owner's.
    pub fn git(&self, dir: &Path, args: &[&str]) -> io::Result<String> {
        let output = self
            .command("git")
            .current_dir(dir)
            .env("GIT_CONFIG_GLOBAL", "/dev/null")
            .env("GIT_CONFIG_NOSYSTEM", "1")
            .args(args)
            .output()?;
        if !output.status.success() {
            return Err(io::Error::other(format!(
                "git {}: {}",
                args.join(" "),
                String::from_utf8_lossy(&output.stderr).trim()
            )));
        }
        Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
    }

    pub fn make_repo(&self) -> io::Result<PathBuf> {
        let repo = self.root.join("repo");
        fs::create_dir(&repo)?;
        self.git(&repo, &["init", "-q", "-b", "main"])?;
        for (key, value) in [
            ("user.name", "Test"),
            ("user.email", "test@example.invalid"),
            ("commit.gpgsign", "false"),
            ("core.hooksPath", "/dev/null"),
        ] {
            self.git(&repo, &["config", key, value])?;
        }
        fs::write(repo.join("README.md"), "# test repo\n")?;
        self.git(&repo, &["add", "-A"])?;
        self.git(&repo, &["commit", "-q", "-m", "init"])?;
        Ok(repo)
    }
}
