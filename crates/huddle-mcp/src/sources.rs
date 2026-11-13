use std::path::PathBuf;

use huddle_history::History;
use serde_json::Value;

use crate::machine::{Machine, ProviderSetup};

/// Where a tool reads its data. Tools never touch the disk or the router themselves.
pub trait Sources: Send {
    /// Whether the router answered its health probe when these sources were picked.
    fn router_up(&self) -> bool;
    /// The path of the running binary.
    fn binary_path(&self) -> PathBuf;
    /// The request history, or none when there is no database.
    fn open_history(&self) -> Option<History>;
    /// The router's live state; none while the router is down.
    fn router_state(&self) -> Option<Value>;
    /// The five providers as setup left them.
    fn provider_setups(&self) -> Vec<ProviderSetup>;
    /// The folder that holds the router's log files.
    fn state_dir(&self) -> PathBuf;
}

/// Reads state files only. Used while the router is down.
pub struct DiskSources {
    pub machine: Machine,
}

impl Sources for DiskSources {
    fn router_up(&self) -> bool {
        false
    }

    fn binary_path(&self) -> PathBuf {
        self.machine.binary.clone()
    }

    fn open_history(&self) -> Option<History> {
        self.machine.open_history()
    }

    fn router_state(&self) -> Option<Value> {
        None
    }

    fn provider_setups(&self) -> Vec<ProviderSetup> {
        self.machine.provider_setups()
    }

    fn state_dir(&self) -> PathBuf {
        self.machine.paths.state_dir.clone()
    }
}

/// Reads state files and asks the live router for what only it holds.
pub struct RouterSources {
    pub machine: Machine,
}

impl Sources for RouterSources {
    fn router_up(&self) -> bool {
        true
    }

    fn binary_path(&self) -> PathBuf {
        self.machine.binary.clone()
    }

    fn open_history(&self) -> Option<History> {
        self.machine.open_history()
    }

    fn router_state(&self) -> Option<Value> {
        self.machine.router_state()
    }

    fn provider_setups(&self) -> Vec<ProviderSetup> {
        self.machine.provider_setups()
    }

    fn state_dir(&self) -> PathBuf {
        self.machine.paths.state_dir.clone()
    }
}

/// The router's sources while it answers, the disk's otherwise.
pub fn pick(router_answers: bool, machine: Machine) -> Box<dyn Sources> {
    if router_answers {
        Box::new(RouterSources { machine })
    } else {
        Box::new(DiskSources { machine })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::machine::fixtures::machine;

    #[test]
    fn pick_follows_the_health_answer() {
        let home = std::env::temp_dir().join("huddle-mcp-pick-unused");
        assert!(pick(true, machine(&home, 1)).router_up());
        assert!(!pick(false, machine(&home, 1)).router_up());
    }
}
