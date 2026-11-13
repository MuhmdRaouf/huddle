//! The ready report of a setup the key page finished. The page's server writes it as one file in
//! the state folder; the setup command that is still waiting, or else the next prompt's hook,
//! prints it once and removes it. The report holds step names and states, never a key.

use std::path::{Path, PathBuf};

use huddle_catalog::providers::{Provider, provider_by_id, provider_ids};
use huddle_config::atomic::write_atomic;
use huddle_config::files::PRIVATE_FILE_MODE;
use huddle_config::paths::Paths;

use super::finish::{Outcome, render_lines};

const VERSION_FILE: &str = include_str!("../../../../../VERSION");

/// The words `setup` prints on top of its report.
pub fn head(provider: &Provider, ready: bool) -> String {
    let state = if ready { "ready" } else { "not ready" };
    format!("{} setup: {state}.", provider.id)
}

/// `huddle <version> (<path>)`.
pub fn runtime_line(executable: &Path) -> String {
    format!("huddle {} ({})", VERSION_FILE.trim(), executable.display())
}

/// The whole report of an outcome: the head, the runtime line, an optional line naming where
/// the key was found, then the outcome's own lines.
pub fn report(
    provider: &Provider,
    outcome: &Outcome,
    executable: &Path,
    key_line: Option<&str>,
) -> String {
    let mut lines = vec![head(provider, outcome.ready), runtime_line(executable)];
    lines.extend(key_line.map(str::to_owned));
    lines.extend(render_lines(outcome, provider));
    lines.join("\n") + "\n"
}

/// `<state_dir>/key-page-<p>.ready`.
fn note_path(paths: &Paths, provider: &Provider) -> PathBuf {
    paths
        .state_dir
        .join(format!("key-page-{}.ready", provider.id))
}

pub fn write(paths: &Paths, provider: &Provider, report: &str) {
    let _ = write_atomic(
        &note_path(paths, provider),
        report.as_bytes(),
        PRIVATE_FILE_MODE,
    );
}

/// The report, removed from disk; none when no page finished a setup since the last take.
pub fn take(paths: &Paths, provider: &Provider) -> Option<String> {
    let path = note_path(paths, provider);
    let text = std::fs::read_to_string(&path).ok()?;
    std::fs::remove_file(&path).ok()?;
    Some(text)
}

/// Every provider's waiting report, in catalog order.
pub fn take_all(paths: &Paths) -> String {
    provider_ids()
        .iter()
        .filter_map(|id| provider_by_id(id))
        .filter_map(|provider| take(paths, provider))
        .collect()
}
