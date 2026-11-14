//! The ready report of a setup the key page finished. The page's server writes it as one file in
//! the state folder; the setup command that is still waiting, or else the next prompt's hook,
//! prints it once and removes it. The report holds step names and states, never a key.

use std::path::{Path, PathBuf};

use huddle_catalog::providers::{Provider, provider_by_id, provider_ids};
use huddle_config::atomic::write_atomic;
use huddle_config::files::PRIVATE_FILE_MODE;
use huddle_config::paths::Paths;

use super::finish::{KeyVerdict, Outcome, render_lines};

const VERSION_FILE: &str = include_str!("../../../../../VERSION");

/// The words `setup` prints on top of its report: `ready`, or `ready, key not checked` when
/// the provider could not be asked about the key, or `not ready`.
pub fn head(provider: &Provider, ready: bool, key_checked: bool) -> String {
    let state = match (ready, key_checked) {
        (false, _) => "not ready",
        (true, true) => "ready",
        (true, false) => "ready, key not checked",
    };
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
    let checked = outcome.verdict != KeyVerdict::Unknown;
    let mut lines = vec![
        head(provider, outcome.ready, checked),
        runtime_line(executable),
    ];
    lines.extend(key_line.map(str::to_owned));
    lines.extend(render_lines(outcome, provider));
    lines.join("\n") + "\n"
}

/// A waiting report and whether its setup ended ready.
#[derive(Debug, PartialEq, Eq)]
pub struct Note {
    pub report: String,
    pub ready: bool,
}

/// `<state_dir>/key-page-<p>.ready` for a ready setup, `.not-ready` for the other.
fn note_path(paths: &Paths, provider: &Provider, ready: bool) -> PathBuf {
    let suffix = if ready { "ready" } else { "not-ready" };
    paths
        .state_dir
        .join(format!("key-page-{}.{suffix}", provider.id))
}

pub fn write(paths: &Paths, provider: &Provider, note: &Note) {
    let _ = write_atomic(
        &note_path(paths, provider, note.ready),
        note.report.as_bytes(),
        PRIVATE_FILE_MODE,
    );
}

/// The note, removed from disk; none when no page finished a setup since the last take.
pub fn take(paths: &Paths, provider: &Provider) -> Option<Note> {
    [true, false].into_iter().find_map(|ready| {
        let path = note_path(paths, provider, ready);
        let report = std::fs::read_to_string(&path).ok()?;
        std::fs::remove_file(&path).ok()?;
        Some(Note { report, ready })
    })
}

/// Every provider's waiting report, in catalog order.
pub fn take_all(paths: &Paths) -> String {
    provider_ids()
        .iter()
        .filter_map(|id| provider_by_id(id))
        .filter_map(|provider| take(paths, provider))
        .map(|note| note.report)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_catalog::providers::provider_by_id;

    fn zai() -> &'static Provider {
        provider_by_id("zai").expect("zai")
    }

    #[test]
    fn g4_head_names_the_unchecked_key() {
        assert_eq!(head(zai(), true, true), "zai setup: ready.");
        assert_eq!(
            head(zai(), true, false),
            "zai setup: ready, key not checked."
        );
        assert_eq!(head(zai(), false, false), "zai setup: not ready.");
    }
}
