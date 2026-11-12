//! Finds the standalone provider plugins that Claude Code still lists as installed.
//! Reads Claude Code's `installed_plugins.json` and never writes under the plugins folder.

use std::collections::BTreeMap;
use std::path::Path;

use serde::Deserialize;

const PROVIDERS: [&str; 5] = ["zai", "kimi", "deepseek", "minimax", "qwen"];

#[derive(Deserialize)]
struct InstalledPlugins {
    plugins: BTreeMap<String, serde::de::IgnoredAny>,
}

/// The hint lines for every old provider plugin listed in `<claude_dir>/plugins/installed_plugins.json`.
/// A missing, unreadable or unparsable file gives no lines.
pub fn hint_lines(claude_dir: &Path) -> Vec<String> {
    let path = claude_dir.join("plugins").join("installed_plugins.json");
    let Ok(text) = std::fs::read_to_string(path) else {
        return Vec::new();
    };
    let Ok(installed) = serde_json::from_str::<InstalledPlugins>(&text) else {
        return Vec::new();
    };
    PROVIDERS
        .iter()
        .filter(|p| {
            installed
                .plugins
                .contains_key(&format!("{p}-plugin-cc@muhmdraouf"))
        })
        .flat_map(|p| {
            [
                format!("The {p} plugin is part of huddle."),
                format!("Remove the old one: /plugin uninstall {p}-plugin-cc@muhmdraouf"),
                format!("Then use /huddle:{p}:setup, /huddle:{p}:remove, /huddle:{p}:usage"),
            ]
        })
        .collect()
}
