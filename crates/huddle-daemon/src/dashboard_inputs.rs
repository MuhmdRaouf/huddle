//! What the dashboard shows about the machine around the router: whether Claude Code points at
//! it, which providers hold a key, and the delegated jobs. The reads touch the disk and the key
//! store, so they run on a blocking thread, and only when a snapshot asks for them and the last
//! reading is older than 10 seconds. The snapshot gets the last reading at once. With no page
//! open nothing is asked, so nothing runs.

use std::collections::HashMap;
use std::path::Path;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::{Duration, Instant};

use huddle_catalog::providers::providers;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_jobs::id::now_ms;
use huddle_jobs::overview::overview;
use huddle_keystore::port::{Keystore, platform_default};
use huddle_router::dashboard::DashboardInputs;
use huddle_router::live::{JobRow, Wiring};
use tokio::runtime::Handle;

/// How long a reading is kept before a snapshot starts the next one.
const WINDOW: Duration = Duration::from_secs(10);

/// The wiring card's state: `Checking` until the first read lands, then `Ok` when Claude Code
/// points at the router with a CA that exists, else `Bad`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum WiringState {
    #[default]
    Checking,
    Ok,
    Bad,
}

/// The state a finished wiring read shows.
fn wiring_state_of(wiring: Wiring) -> WiringState {
    if wiring.proxy && wiring.ca {
        WiringState::Ok
    } else {
        WiringState::Bad
    }
}

/// Answers the wiring card's state from the cache.
pub type WiringStateFn = Arc<dyn Fn() -> WiringState + Send + Sync>;

/// One read of everything the dashboard asks of the machine.
#[derive(Default)]
struct Reading {
    wiring_state: WiringState,
    wiring: Wiring,
    keys: HashMap<&'static str, bool>,
    jobs: Vec<JobRow>,
}

type Read = Arc<dyn Fn() -> Reading + Send + Sync>;

struct State {
    reading: Arc<Reading>,
    read_at: Option<Instant>,
    busy: bool,
}

/// The last reading, refreshed on demand: a request that finds it older than `window` starts
/// one background read and still gets the old reading.
struct Cache {
    read: Read,
    window: Duration,
    runtime: Handle,
    state: Mutex<State>,
}

/// Clears `busy` when a read ends, also when it panics.
struct Done<'a>(&'a Cache);

impl Drop for Done<'_> {
    fn drop(&mut self) {
        self.0.lock().busy = false;
    }
}

impl Cache {
    fn new(read: Read, window: Duration, runtime: Handle) -> Arc<Cache> {
        Arc::new(Cache {
            read,
            window,
            runtime,
            state: Mutex::new(State {
                reading: Arc::new(Reading::default()),
                read_at: None,
                busy: false,
            }),
        })
    }

    fn lock(&self) -> MutexGuard<'_, State> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn latest(self: &Arc<Cache>) -> Arc<Reading> {
        let mut state = self.lock();
        let stale = state.read_at.is_none_or(|at| at.elapsed() >= self.window);
        if stale && !state.busy {
            state.busy = true;
            let cache = Arc::clone(self);
            self.runtime.spawn_blocking(move || {
                let _done = Done(&cache);
                let reading = Arc::new((cache.read)());
                let mut state = cache.lock();
                state.reading = reading;
                state.read_at = Some(Instant::now());
            });
        }
        Arc::clone(&state.reading)
    }
}

/// The dashboard's inputs. Until the first read lands, the wiring is off, no lane has a key
/// answer and no job shows. Called inside the runtime.
pub fn start(version: String, env: Env, paths: Paths, port: u16) -> DashboardInputs {
    start_with_state(version, env, paths, port).0
}

/// `start`, and the wiring card's state read from the same cache.
pub fn start_with_state(
    version: String,
    env: Env,
    paths: Paths,
    port: u16,
) -> (DashboardInputs, WiringStateFn) {
    let read: Read = Arc::new(move || read_all(&env, &paths, port));
    let cache = Cache::new(read, WINDOW, Handle::current());
    let wiring_state: WiringStateFn = Arc::new({
        let cache = Arc::clone(&cache);
        move || cache.latest().wiring_state
    });
    let inputs = DashboardInputs {
        version,
        wiring: Arc::new({
            let cache = Arc::clone(&cache);
            move || cache.latest().wiring
        }),
        key_present: Arc::new({
            let cache = Arc::clone(&cache);
            move |id| cache.latest().keys.get(id).copied()
        }),
        jobs: Arc::new(move || cache.latest().jobs.clone()),
        hosts: Arc::new(Vec::new),
        // The router counts its own connections.
        connections: Arc::new(|| 0),
    };
    (inputs, wiring_state)
}

fn read_all(env: &Env, paths: &Paths, port: u16) -> Reading {
    let store = platform_default(paths);
    let wiring = read_wiring(paths, port);
    Reading {
        wiring_state: wiring_state_of(wiring),
        wiring,
        keys: providers()
            .iter()
            .map(|provider| {
                let env_key = provider
                    .key_env
                    .iter()
                    .any(|name| env.get(name).is_some_and(|v| !v.trim().is_empty()));
                (provider.id, env_key || store_has_key(&*store, provider.id))
            })
            .collect(),
        jobs: overview(paths, now_ms())
            .into_iter()
            .map(|row| JobRow {
                id: row.id,
                harness: row.harness,
                repo: row.repo,
                title: row.title,
                state: row.state.to_string(),
                started_ms: row.started_ms,
            })
            .collect(),
    }
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
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn counting_cache(window: Duration) -> (Arc<Cache>, Arc<AtomicUsize>) {
        let reads = Arc::new(AtomicUsize::new(0));
        let read: Read = Arc::new({
            let reads = Arc::clone(&reads);
            move || {
                reads.fetch_add(1, Ordering::SeqCst);
                std::thread::sleep(Duration::from_millis(150));
                Reading::default()
            }
        });
        (Cache::new(read, window, Handle::current()), reads)
    }

    #[tokio::test]
    async fn no_read_runs_while_no_snapshot_asks() {
        let (_cache, reads) = counting_cache(Duration::ZERO);
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(reads.load(Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn concurrent_snapshots_after_the_window_start_one_read() {
        let (cache, reads) = counting_cache(Duration::ZERO);
        let callers: Vec<_> = (0..8)
            .map(|_| {
                let cache = Arc::clone(&cache);
                tokio::spawn(async move { cache.latest() })
            })
            .collect();
        for caller in callers {
            caller.await.expect("caller");
        }
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(reads.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_fresh_reading_is_not_read_again() {
        let (cache, reads) = counting_cache(Duration::from_secs(60));
        cache.latest();
        tokio::time::sleep(Duration::from_millis(400)).await;
        cache.latest();
        tokio::time::sleep(Duration::from_millis(100)).await;
        assert_eq!(reads.load(Ordering::SeqCst), 1);
    }

    fn reading_with(wiring: Wiring) -> Reading {
        Reading {
            wiring_state: wiring_state_of(wiring),
            wiring,
            ..Reading::default()
        }
    }

    #[tokio::test]
    async fn the_wiring_state_is_checking_until_the_first_read_lands() {
        let read: Read = Arc::new(|| {
            std::thread::sleep(Duration::from_millis(100));
            reading_with(Wiring {
                proxy: true,
                ca: true,
            })
        });
        let cache = Cache::new(read, Duration::from_secs(60), Handle::current());
        assert_eq!(cache.latest().wiring_state, WiringState::Checking);
        tokio::time::sleep(Duration::from_millis(400)).await;
        assert_eq!(cache.latest().wiring_state, WiringState::Ok);
    }

    #[test]
    fn a_finished_read_is_ok_only_with_the_proxy_and_the_ca() {
        let only_proxy = Wiring {
            proxy: true,
            ca: false,
        };
        assert_eq!(wiring_state_of(only_proxy), WiringState::Bad);
        assert_eq!(wiring_state_of(Wiring::default()), WiringState::Bad);
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
