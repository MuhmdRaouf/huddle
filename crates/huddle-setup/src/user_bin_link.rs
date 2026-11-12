//! The `~/.local/bin/huddle` link: a symlink to the stable `<data>/bin/huddle` link, so a bare
//! `huddle` works in any terminal whose PATH has `~/.local/bin`. A path that is not huddle's own
//! link is never replaced or removed.

use std::fs;
use std::io;
use std::os::unix::fs::symlink;
use std::path::Path;

use huddle_config::env::Env;
use huddle_config::files::{DIR_MODE, create_dir};
use huddle_config::paths::Paths;

/// What setup did about the link.
#[derive(Debug, PartialEq, Eq)]
pub enum LinkOutcome {
    Created,
    AlreadyOurs,
    SomethingElse,
}

/// True when `path` is a symlink whose target is `target`.
pub fn is_link_to(path: &Path, target: &Path) -> bool {
    fs::read_link(path).is_ok_and(|found| found == target)
}

/// Creates the link when `user_bin_dir/huddle` is missing; leaves anything else as it is.
pub fn ensure(paths: &Paths) -> io::Result<LinkOutcome> {
    let link = paths.user_bin_dir.join("huddle");
    let target = paths.bin_dir.join("huddle");
    if link.symlink_metadata().is_ok() {
        return Ok(if is_link_to(&link, &target) {
            LinkOutcome::AlreadyOurs
        } else {
            LinkOutcome::SomethingElse
        });
    }
    create_dir(&paths.user_bin_dir, DIR_MODE)?;
    symlink(&target, &link)?;
    Ok(LinkOutcome::Created)
}

/// Removes the link only while it still points at `<data>/bin/huddle`. True when it did.
pub fn remove_if_ours(paths: &Paths) -> io::Result<bool> {
    let link = paths.user_bin_dir.join("huddle");
    if !is_link_to(&link, &paths.bin_dir.join("huddle")) {
        return Ok(false);
    }
    fs::remove_file(&link)?;
    Ok(true)
}

/// True when some PATH entry is `user_bin_dir`.
pub fn on_path(env: &Env, paths: &Paths) -> bool {
    env.get("PATH")
        .is_some_and(|path| std::env::split_paths(path).any(|entry| entry == paths.user_bin_dir))
}

/// The report lines for one setup, in the order they are said.
pub fn lines(env: &Env, paths: &Paths, outcome: &LinkOutcome) -> Vec<String> {
    let mut lines = Vec::new();
    match outcome {
        LinkOutcome::Created => lines.push("linked ~/.local/bin/huddle".to_string()),
        LinkOutcome::SomethingElse => lines.push(
            "~/.local/bin/huddle exists and is not huddle's link; left as it is.".to_string(),
        ),
        LinkOutcome::AlreadyOurs => {}
    }
    if !on_path(env, paths) {
        lines.push(format!(
            "~/.local/bin is not on your PATH; run huddle as {}",
            paths.bin_dir.join("huddle").display()
        ));
    }
    lines
}
