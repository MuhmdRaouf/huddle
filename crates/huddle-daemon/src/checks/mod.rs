//! What the dashboard's System pages show: one row per provider plugin, huddle's own health
//! checks and the harness table. Each page has a pure builder over facts that a reader gathers
//! from disk, so the rows are tested from fixture files and the daemon only caches them.

pub mod harnesses;
pub mod health;
pub mod plugins;

/// How far a check got.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Level {
    Pass,
    Warn,
    Fail,
}

/// One huddle health check.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Check {
    pub id: &'static str,
    pub level: Level,
    pub label: &'static str,
    pub value: String,
    pub detail: String,
}

/// Whether a page's first reading has landed.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Phase {
    #[default]
    Checking,
    Ready,
}

use std::collections::HashMap;

use huddle_router::live::Wiring;

/// Everything the System pages read from the machine, as plain values.
pub struct Facts<'a> {
    /// The running binary's version.
    pub version: &'a str,
    pub port: u16,
    /// `<claude_dir>/plugins/installed_plugins.json`.
    pub installed_plugins: Option<&'a str>,
    /// The `version=` line of each installed plugin's `dist/run`, by plugin name.
    pub carried: &'a HashMap<String, String>,
    /// `<claude_dir>/settings.json`.
    pub settings: Option<&'a str>,
    pub wiring: Wiring,
    /// The providers `providers.json` enables, or why it could not be read.
    pub enabled: Result<Vec<String>, String>,
    /// How many dashboard hosts `config.toml` lists, or why it could not be read.
    pub config_hosts: Result<usize, String>,
    /// Whether each provider holds a key. Yes or no only.
    pub keys: &'a HashMap<&'static str, bool>,
}
