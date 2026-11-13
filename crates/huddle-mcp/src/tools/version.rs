use rmcp::schemars::{self, JsonSchema};
use serde::Serialize;

use crate::sources::Sources;

/// What `huddle_version` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct VersionInfo {
    /// The huddle version.
    pub version: String,
    /// Whether the router answers right now.
    pub router_up: bool,
    /// The path of the running huddle binary.
    pub binary_path: String,
}

pub fn version(version: &str, sources: &dyn Sources) -> VersionInfo {
    VersionInfo {
        version: version.to_string(),
        router_up: sources.router_up(),
        binary_path: sources.binary_path().display().to_string(),
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use super::*;
    use crate::sources::DiskSources;

    #[test]
    fn version_reports_the_sources_state() {
        let sources = DiskSources {
            binary: PathBuf::from("/bin/huddle"),
        };
        let info = version("1.2.3", &sources);
        assert_eq!(info.version, "1.2.3");
        assert!(!info.router_up);
        assert_eq!(info.binary_path, "/bin/huddle");
    }
}
