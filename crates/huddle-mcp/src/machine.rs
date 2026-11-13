//! What a tool reads from this machine: the history database, the provider setup and the
//! running router's state. A key is only ever tested for presence.

use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::PathBuf;
use std::time::Duration;

use huddle_catalog::providers::providers;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_config::providers_file::read_enabled;
use huddle_history::History;
use huddle_keystore::port::platform_default;
use serde_json::Value;

const STATE_PATH: &str = "/huddle-router/state";
const STATE_WITHIN: Duration = Duration::from_secs(2);
/// A state answer that waits for the providers' quota asks.
const QUOTA_WITHIN: Duration = Duration::from_secs(10);
const STATE_MAX_BYTES: usize = 8 * 1024 * 1024;

/// The state a server is started with: where the files are and which port the router uses.
#[derive(Clone)]
pub struct Machine {
    pub binary: PathBuf,
    pub paths: Paths,
    pub env: Env,
    pub port: u16,
}

/// One provider as setup left it. Holds no key.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProviderSetup {
    pub id: &'static str,
    pub display: &'static str,
    /// The command prefix, such as `/huddle:zai:`.
    pub slash: &'static str,
    pub lane: bool,
    pub key_present: bool,
    pub models: Vec<&'static str>,
    pub agents: Vec<&'static str>,
}

impl Machine {
    /// The history database for reading, or none when there is no database yet.
    pub fn open_history(&self) -> Option<History> {
        let db = self.paths.state_dir.join("huddle.db");
        db.is_file().then(|| History::open_read(&db).ok()).flatten()
    }

    /// The five providers: lane in `providers.toml`, whether a key exists, models and agent files.
    pub fn provider_setups(&self) -> Vec<ProviderSetup> {
        let known: Vec<&str> = providers().iter().map(|p| p.id).collect();
        let enabled = read_enabled(&self.paths.config_dir, &known).unwrap_or_default();
        let store = platform_default(&self.paths);
        let agents_dir = self.paths.claude_dir.join("agents");
        providers()
            .iter()
            .map(|provider| {
                let env_key = provider
                    .key_env
                    .iter()
                    .any(|name| self.env.get(name).is_some_and(|v| !v.trim().is_empty()));
                let models = vec![provider.main.id, provider.flash.id];
                let agents = models
                    .iter()
                    .copied()
                    .filter(|id| agents_dir.join(format!("{id}.md")).is_file())
                    .collect();
                ProviderSetup {
                    id: provider.id,
                    display: provider.display,
                    slash: provider.slash,
                    lane: enabled.iter().any(|id| id == provider.id),
                    key_present: env_key || matches!(store.get(provider.id), Ok(Some(_))),
                    models,
                    agents,
                }
            })
            .collect()
    }

    /// The router's dashboard state as JSON, or none when the router does not answer in time.
    pub fn router_state(&self) -> Option<Value> {
        self.state_at(STATE_PATH, STATE_WITHIN)
    }

    /// The state after the router refreshed its provider quotas: the ask counts as a viewer.
    pub fn router_state_with_quota(&self) -> Option<Value> {
        self.state_at(&format!("{STATE_PATH}?quota=refresh"), QUOTA_WITHIN)
    }

    fn state_at(&self, target: &str, within: Duration) -> Option<Value> {
        let address = SocketAddr::from(([127, 0, 0, 1], self.port));
        let mut stream = TcpStream::connect_timeout(&address, STATE_WITHIN).ok()?;
        stream.set_read_timeout(Some(within)).ok()?;
        stream.set_write_timeout(Some(within)).ok()?;
        let request = format!(
            "GET {target} HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            self.port
        );
        stream.write_all(request.as_bytes()).ok()?;
        let mut answer = Vec::new();
        stream
            .take(STATE_MAX_BYTES as u64)
            .read_to_end(&mut answer)
            .ok()?;
        let text = std::str::from_utf8(&answer).ok()?;
        let (head, body) = text.split_once("\r\n\r\n")?;
        head.starts_with("HTTP/1.1 200")
            .then(|| serde_json::from_str(body).ok())
            .flatten()
    }
}

#[cfg(test)]
pub(crate) mod fixtures {
    use super::*;

    /// A machine whose folders all sit under `home`, with the router on `port`.
    pub fn machine(home: &std::path::Path, port: u16) -> Machine {
        let home_text = home.display().to_string();
        let env = Env::with(&[("HOME", home_text.as_str())]);
        Machine {
            binary: PathBuf::from("/bin/huddle"),
            paths: Paths::from_env(&env).unwrap(),
            env,
            port,
        }
    }
}
