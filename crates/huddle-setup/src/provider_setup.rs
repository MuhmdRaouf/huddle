//! A provider enabled on this machine: its lane in providers.toml first, so huddle-router sees
//! it as soon as it reloads, then its `/model` entries.

use huddle_catalog::providers::Provider;
use huddle_config::paths::Paths;

use crate::model_entries;
use crate::settings::SettingsError;

/// Why enabling a provider did not happen; nothing but the providers file may have changed.
#[derive(Debug)]
pub enum ProviderSetupError {
    Providers(String),
    Settings(SettingsError),
}

impl std::fmt::Display for ProviderSetupError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ProviderSetupError::Providers(message) => write!(f, "{message}"),
            ProviderSetupError::Settings(error) => write!(f, "{error}"),
        }
    }
}

impl std::error::Error for ProviderSetupError {}

/// What one `enable` did.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProviderEnabled {
    pub lines: Vec<String>,
    pub changed: bool,
}

/// Records the provider in providers.toml, then its /model entries.
pub fn enable(
    paths: &Paths,
    provider: &'static Provider,
) -> Result<ProviderEnabled, ProviderSetupError> {
    huddle_config::providers_file::set_enabled(&paths.config_dir, provider.id, true)
        .map_err(|error| ProviderSetupError::Providers(error.to_string()))?;
    let outcome = model_entries::apply(paths, provider).map_err(ProviderSetupError::Settings)?;
    Ok(ProviderEnabled {
        lines: outcome.lines,
        changed: outcome.changed,
    })
}
