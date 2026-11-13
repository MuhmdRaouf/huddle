//! What the dashboard shows about the machine around the router: whether Claude Code points at
//! it, which providers hold a key, the delegated jobs, Claude Code's sessions and the projects'
//! worktrees, the plugin and harness checks and the router's own load. The reads touch the
//! disk, git, `ps` and the key store, so each runs on a blocking thread behind a `Cache`, and
//! only when a snapshot asks for it. With no page open nothing is asked, so nothing runs.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use huddle_catalog::providers::providers;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_jobs::id::now_ms;
use huddle_jobs::overview::overview;
use huddle_keystore::port::{Keystore, platform_default};
use huddle_router::dashboard::{DashboardInputs, Machine};
use huddle_router::live::{JobRow, Wiring, WiringState};
use huddle_sessions::{Limits, scan, worktrees};
use tokio::runtime::Handle;

use crate::checks::harnesses::{self};
use crate::checks::plugins::{self, PluginsReport};
use crate::history_upkeep;
use crate::read_cache::{Cache, Read};
use crate::rss::read_usage;

/// How long the wiring, key and job reading is kept.
const WINDOW: Duration = Duration::from_secs(10);
/// How long the session and worktree reading is kept.
const SESSIONS_WINDOW: Duration = Duration::from_secs(5);
/// How long the router's own CPU and memory reading is kept.
const PROCESS_WINDOW: Duration = Duration::from_secs(5);
/// How long the plugin checks are kept.
const PLUGINS_WINDOW: Duration = Duration::from_secs(30);
/// How long the harness checks are kept: each one runs the harness's version probe.
const HARNESSES_WINDOW: Duration = Duration::from_secs(600);

/// The state a finished wiring read shows.
fn wiring_state_of(wiring: Wiring) -> WiringState {
    if wiring.proxy && wiring.ca {
        WiringState::Ok
    } else {
        WiringState::Bad
    }
}

/// One read of the wiring, the keys and the jobs.
#[derive(Default)]
struct Reading {
    wiring_state: WiringState,
    wiring: Wiring,
    keys: HashMap<&'static str, bool>,
    jobs: Vec<JobRow>,
}

/// The dashboard's inputs. Until the first read lands, the wiring is off and checking, no lane
/// has a key answer and no job, session or check shows. Called inside the runtime.
pub fn start(version: String, env: Env, paths: Paths, port: u16) -> DashboardInputs {
    let runtime = Handle::current();
    let reading: Read<Reading> = {
        let (env, paths) = (env.clone(), paths.clone());
        Arc::new(move || read_all(&env, &paths, port))
    };
    let cache = Cache::new(reading, WINDOW, runtime.clone());
    let sessions = Cache::new(
        Arc::new({
            let paths = paths.clone();
            move || read_machine(&paths)
        }),
        SESSIONS_WINDOW,
        runtime.clone(),
    );
    let process = Cache::new(
        Arc::new(|| read_usage(std::process::id()).unwrap_or_default()),
        PROCESS_WINDOW,
        runtime.clone(),
    );
    let plugin_checks = Cache::new(
        Arc::new({
            let (env, paths, version) = (env.clone(), paths.clone(), version.clone());
            move || read_plugins(&env, &paths, &version, port)
        }),
        PLUGINS_WINDOW,
        runtime.clone(),
    );
    let harness_checks = Cache::new(
        Arc::new(move || harnesses::read(&env, &paths)),
        HARNESSES_WINDOW,
        runtime,
    );
    DashboardInputs {
        version,
        wiring: Arc::new({
            let cache = Arc::clone(&cache);
            move || cache.latest().wiring
        }),
        wiring_state: Arc::new({
            let cache = Arc::clone(&cache);
            move || cache.latest().wiring_state
        }),
        key_present: Arc::new({
            let cache = Arc::clone(&cache);
            move |id| cache.latest().keys.get(id).copied()
        }),
        jobs: Arc::new(move || cache.latest().jobs.clone()),
        hosts: Arc::new(Vec::new),
        // The router counts its own connections.
        connections: Arc::new(|| 0),
        process: Arc::new(move || *process.latest()),
        sessions: Arc::new(move || sessions.latest().as_ref().clone()),
        plugins: Arc::new(move || json_of(&*plugin_checks.latest())),
        harnesses: Arc::new(move || json_of(&*harness_checks.latest())),
        // The daemon points this at the database file once it knows the state folder.
        history: Arc::new(|| None),
        // The daemon points this at the config file once it knows the config folder.
        keep_days: Arc::new(|| history_upkeep::DEFAULT_KEEP_DAYS),
    }
}

/// The reports are plain data; a failed encoding leaves the page on `null`, which it reads as
/// "checking".
fn json_of<T: serde::Serialize>(report: &T) -> serde_json::Value {
    serde_json::to_value(report).unwrap_or_default()
}

fn read_all(env: &Env, paths: &Paths, port: u16) -> Reading {
    let wiring = read_wiring(paths, port);
    Reading {
        wiring_state: wiring_state_of(wiring),
        wiring,
        keys: read_keys(env, paths),
        jobs: overview(paths, now_ms())
            .into_iter()
            .map(|row| JobRow {
                id: row.id,
                harness: row.harness,
                repo: row.repo,
                title: row.title,
                state: row.state.to_string(),
                started_ms: row.started_ms,
                ended_ms: row.ended_ms,
                files: row.files,
                add: row.add,
                del: row.del,
                why: row.why,
            })
            .collect(),
    }
}

/// Yes or no per provider: the key itself is never held.
fn read_keys(env: &Env, paths: &Paths) -> HashMap<&'static str, bool> {
    let store = platform_default(paths);
    providers()
        .iter()
        .map(|provider| {
            let env_key = provider
                .key_env
                .iter()
                .any(|name| env.get(name).is_some_and(|v| !v.trim().is_empty()));
            (provider.id, env_key || store_has_key(&*store, provider.id))
        })
        .collect()
}

fn read_machine(paths: &Paths) -> Machine {
    let sessions = scan(&paths.claude_dir, now_ms(), Limits::default());
    let cwds: Vec<PathBuf> = sessions
        .iter()
        .filter_map(|session| session.cwd.as_deref().map(PathBuf::from))
        .collect();
    Machine {
        worktrees: worktrees(&cwds),
        sessions,
    }
}

/// The wiring and keys are read afresh: the plugin checks must not depend on which cache lands
/// first.
fn read_plugins(env: &Env, paths: &Paths, version: &str, port: u16) -> PluginsReport {
    plugins::read(
        paths,
        version,
        port,
        read_wiring(paths, port),
        &read_keys(env, paths),
    )
}

/// Yes or no only: the key is dropped as soon as it is found.
fn store_has_key(store: &dyn Keystore, provider: &str) -> bool {
    matches!(store.get(provider), Ok(Some(_)))
}

fn read_wiring(paths: &Paths, port: u16) -> Wiring {
    let text = std::fs::read_to_string(paths.claude_dir.join("settings.json")).ok();
    wiring_of(text.as_deref(), port, |ca| Path::new(ca).is_file())
}

/// `proxy`: `env.HTTPS_PROXY` names this router's loopback port. `ca`: `env.NODE_EXTRA_CA_CERTS`
/// names a file that exists.
fn wiring_of(settings: Option<&str>, port: u16, ca_exists: impl Fn(&str) -> bool) -> Wiring {
    let Some(env) = settings
        .and_then(|text| serde_json::from_str::<serde_json::Value>(text).ok())
        .and_then(|doc| doc.get("env").cloned())
    else {
        return Wiring::default();
    };
    let text_of = |name: &str| env.get(name).and_then(|v| v.as_str()).map(str::to_string);
    Wiring {
        proxy: text_of("HTTPS_PROXY").is_some_and(|url| names_router(&url, port)),
        ca: text_of("NODE_EXTRA_CA_CERTS").is_some_and(|path| ca_exists(&path)),
    }
}

fn names_router(url: &str, port: u16) -> bool {
    let rest = url
        .strip_prefix("http://")
        .or_else(|| url.strip_prefix("https://"))
        .unwrap_or(url);
    let host_port = rest.strip_suffix('/').unwrap_or(rest);
    host_port == format!("127.0.0.1:{port}") || host_port == format!("localhost:{port}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_finished_read_is_ok_only_with_the_proxy_and_the_ca() {
        let only_proxy = Wiring {
            proxy: true,
            ca: false,
        };
        assert_eq!(wiring_state_of(only_proxy), WiringState::Bad);
        assert_eq!(wiring_state_of(Wiring::default()), WiringState::Bad);
        let both = Wiring {
            proxy: true,
            ca: true,
        };
        assert_eq!(wiring_state_of(both), WiringState::Ok);
    }

    #[tokio::test]
    async fn the_wiring_state_is_checking_until_the_first_read_lands() {
        let read: Read<Reading> = Arc::new(|| {
            std::thread::sleep(Duration::from_millis(100));
            let wiring = Wiring {
                proxy: true,
                ca: true,
            };
            Reading {
                wiring_state: wiring_state_of(wiring),
                wiring,
                ..Reading::default()
            }
        });
        let cache = Cache::new(read, Duration::from_secs(60), Handle::current());
        assert_eq!(cache.latest().wiring_state, WiringState::Checking);
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(cache.latest().wiring_state, WiringState::Ok);
    }

    #[test]
    fn the_wiring_state_serializes_as_a_lowercase_word() {
        let json = serde_json::to_string(&WiringState::Checking).expect("json");
        assert_eq!(json, "\"checking\"");
    }

    const PROXY: &str =
        r#"{"env":{"HTTPS_PROXY":"http://127.0.0.1:4835","NODE_EXTRA_CA_CERTS":"/ca.pem"}}"#;

    #[test]
    fn a_proxy_on_this_port_and_an_existing_ca_is_wired() {
        let wiring = wiring_of(Some(PROXY), 4835, |_| true);
        assert_eq!(
            wiring,
            Wiring {
                proxy: true,
                ca: true
            }
        );
    }

    #[test]
    fn another_port_or_a_missing_ca_file_is_not_wired() {
        let wiring = wiring_of(Some(PROXY), 4836, |_| false);
        assert_eq!(wiring, Wiring::default());
    }

    #[test]
    fn no_settings_or_broken_json_is_not_wired() {
        assert_eq!(wiring_of(None, 4835, |_| true), Wiring::default());
        assert_eq!(wiring_of(Some("{"), 4835, |_| true), Wiring::default());
        assert_eq!(wiring_of(Some("{}"), 4835, |_| true), Wiring::default());
    }

    #[test]
    fn a_trailing_slash_and_localhost_name_the_router() {
        assert!(names_router("http://localhost:4835/", 4835));
        assert!(!names_router("http://example.com:4835", 4835));
    }
}
