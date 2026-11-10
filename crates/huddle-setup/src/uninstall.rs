//! The uninstall work: every set-up provider's remove, the router stop, the settings undo, the
//! managed agent files. Each step reports a line; a step's failure never stops the rest.

use std::path::Path;
use std::process::Command;

use huddle_catalog::providers::provider_ids;
use huddle_config::paths::Paths;
use huddle_config::providers_file::read_enabled;

use crate::agent_files::remove_managed;
use crate::undo::{UndoOutcome, undo};

/// The lines one uninstall says, in the order the steps ran.
pub struct UninstallReport {
    pub lines: Vec<String>,
}

/// Runs the steps in this order: each set-up provider's remove, the router stop, the settings
/// undo, the agent files. Each step's failure is reported in `lines` and the rest still run.
pub fn run(paths: &Paths, exe: &Path) -> UninstallReport {
    let mut lines = Vec::new();
    // No providers file (or an unreadable one) means nothing was ever set up.
    let enabled = read_enabled(&paths.router_dir, &provider_ids()).unwrap_or_default();
    for id in &enabled {
        match Command::new(exe)
            .args(["provider", id.as_str(), "remove"])
            .output()
        {
            Ok(out) if out.status.success() => lines.push(format!("removed {id}")),
            Ok(out) => lines.push(format!(
                "could not remove {id}: {}",
                first_line(&out.stderr)
            )),
            Err(error) => lines.push(format!("could not remove {id}: {error}")),
        }
    }

    match Command::new(exe).args(["router", "stop"]).output() {
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout);
            if stdout.contains("not running") {
                // The router was already down: nothing to undo there.
            } else if out.status.success() {
                lines.push("stopped huddle-router".to_string());
            } else {
                lines.push(format!(
                    "could not stop huddle-router: {}",
                    first_line(&out.stderr)
                ));
            }
        }
        Err(error) => lines.push(format!("could not stop huddle-router: {error}")),
    }

    match undo(paths) {
        Ok(UndoOutcome::Nothing) => {
            lines.push("nothing to undo in settings.json".to_string());
        }
        Ok(UndoOutcome::Restored { exact: true }) => {
            lines.push("restored settings.json".to_string());
        }
        Ok(UndoOutcome::Restored { exact: false }) => {
            lines.push("removed huddle's keys from settings.json".to_string());
        }
        Ok(UndoOutcome::NotOurs) => {
            lines.push("settings.json no longer points at huddle-router; left it alone".to_string())
        }
        Err(error) => lines.push(format!("could not undo settings.json: {error}")),
    }

    if let Err(error) = remove_managed(paths) {
        lines.push(format!("could not remove the agent files: {error}"));
    }

    UninstallReport { lines }
}

/// The first line of a command's stderr; empty when the stderr is empty.
fn first_line(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes)
        .lines()
        .next()
        .unwrap_or_default()
        .to_string()
}
