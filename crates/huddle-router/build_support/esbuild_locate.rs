//! Where the build script finds esbuild, and which version it accepts.
//!
//! `build.rs` includes this file with `mod`; the crate's test build includes it too, so the
//! lookup is unit-tested even though build scripts have no test target of their own.

use std::ffi::OsStr;
use std::path::{Path, PathBuf};
use std::process::Command;

/// The inputs of one lookup.
pub struct Search<'a> {
    /// The `ESBUILD` variable.
    pub env_esbuild: Option<&'a OsStr>,
    /// The repository root, whose `node_modules/.bin/esbuild` `bun install` writes.
    pub repo_root: &'a Path,
    /// The `PATH` variable.
    pub path: Option<&'a OsStr>,
    /// The version `package.json` pins.
    pub wanted: &'a str,
}

/// The version `package.json` pins for esbuild under `devDependencies`.
pub fn pinned_version(package_json: &str) -> Option<String> {
    let dev = &package_json[package_json.find("\"devDependencies\"")?..];
    let after_key = &dev[dev.find("\"esbuild\"")? + "\"esbuild\"".len()..];
    let open = after_key.find('"')? + 1;
    let value = &after_key[open..];
    Some(value[..value.find('"')?].to_string())
}

/// The version a binary reports for `--version`, or why it did not answer.
fn version_of(binary: &Path) -> Result<String, String> {
    let output = Command::new(binary)
        .arg("--version")
        .output()
        .map_err(|e| format!("{}: {e}", binary.display()))?;
    if !output.status.success() {
        return Err(format!("{}: --version failed", binary.display()));
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

/// The first esbuild of the pinned version, looking at `ESBUILD`, then the repo's
/// `node_modules/.bin/esbuild`, then each `PATH` directory. When none qualifies, the error lists
/// every candidate that was refused and why.
pub fn locate(search: &Search) -> Result<PathBuf, Vec<String>> {
    let mut candidates: Vec<PathBuf> = Vec::new();
    candidates.extend(search.env_esbuild.map(PathBuf::from));
    candidates.push(search.repo_root.join("node_modules/.bin/esbuild"));
    if let Some(path) = search.path {
        candidates.extend(std::env::split_paths(path).map(|dir| dir.join("esbuild")));
    }
    let mut refused = Vec::new();
    for candidate in candidates {
        if !candidate.is_file() {
            continue;
        }
        match version_of(&candidate) {
            Ok(version) if version == search.wanted => return Ok(candidate),
            Ok(version) => refused.push(format!(
                "{} is version {version}, package.json pins {}",
                candidate.display(),
                search.wanted
            )),
            Err(reason) => refused.push(reason),
        }
    }
    Err(refused)
}

/// The `cargo:rerun-if-changed` lines for the files that decide which esbuild is used: only the
/// ones that exist, since cargo rebuilds on every run when a named file is missing.
pub fn rerun_lines(repo_root: &Path) -> Vec<String> {
    ["package.json", "bun.lock", "node_modules/.bin/esbuild"]
        .iter()
        .map(|file| repo_root.join(file))
        .filter(|path| path.exists())
        .map(|path| format!("cargo:rerun-if-changed={}", path.display()))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::{Search, locate, pinned_version, rerun_lines};
    use std::ffi::OsStr;
    use std::os::unix::fs::PermissionsExt;
    use std::path::{Path, PathBuf};

    use huddle_testkit::temp_home::TempHome;

    const WANTED: &str = "0.28.2";

    /// A shell script that answers `--version` with `version`.
    fn fake_esbuild(dir: &Path, version: &str) -> PathBuf {
        std::fs::create_dir_all(dir).expect("create the bin dir");
        let file = dir.join("esbuild");
        std::fs::write(&file, format!("#!/bin/sh\necho {version}\n")).expect("write esbuild");
        std::fs::set_permissions(&file, std::fs::Permissions::from_mode(0o755))
            .expect("make it executable");
        file
    }

    fn scratch() -> TempHome {
        TempHome::new()
    }

    fn search<'a>(root: &'a Path, path: Option<&'a OsStr>) -> Search<'a> {
        Search {
            env_esbuild: None,
            repo_root: root,
            path,
            wanted: WANTED,
        }
    }

    #[test]
    fn n1_build_finds_the_repo_esbuild() {
        let (root, empty_path) = (scratch(), scratch());
        let installed = fake_esbuild(&root.root().join("node_modules/.bin"), WANTED);
        let found = locate(&search(root.root(), Some(empty_path.root().as_os_str())))
            .expect("the repo esbuild");
        assert_eq!(found, installed);
    }

    #[test]
    fn n1_build_refuses_another_esbuild_version() {
        let (root, on_path) = (scratch(), scratch());
        fake_esbuild(&root.root().join("node_modules/.bin"), "0.25.10");
        fake_esbuild(on_path.root(), "0.30.0");
        let refused = locate(&search(root.root(), Some(on_path.root().as_os_str())))
            .expect_err("no esbuild of the pinned version");
        assert_eq!(refused.len(), 2, "{refused:?}");
        assert!(refused.iter().any(|r| r.contains("0.25.10")), "{refused:?}");
        assert!(refused.iter().any(|r| r.contains("0.30.0")), "{refused:?}");
    }

    #[test]
    fn n1_pinned_version_reads_the_dev_dependency() {
        let json = r#"{"name":"x","devDependencies":{"esbuild":"0.28.2"}}"#;
        assert_eq!(pinned_version(json).as_deref(), Some("0.28.2"));
        assert_eq!(pinned_version(r#"{"name":"x"}"#), None);
    }

    #[test]
    fn n1_rerun_lines_name_only_files_that_exist() {
        let root = scratch();
        assert!(rerun_lines(root.root()).is_empty());

        std::fs::write(root.root().join("package.json"), "{}").expect("write package.json");
        std::fs::write(root.root().join("bun.lock"), "").expect("write bun.lock");
        let lines = rerun_lines(root.root());
        assert_eq!(lines.len(), 2, "{lines:?}");
        assert!(
            lines
                .iter()
                .all(|l| l.starts_with("cargo:rerun-if-changed="))
        );
        assert!(lines.iter().any(|l| l.ends_with("package.json")));
        assert!(lines.iter().any(|l| l.ends_with("bun.lock")));
    }
}
