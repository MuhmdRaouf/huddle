//! huddle-router: keycache. The lane's provider key: the environment first, then the OS store
//! under its share of the budget, then the key file. One lookup runs at a time and every reader
//! joins it; a found key is kept until the provider refuses it.

use std::sync::{
    Arc, Mutex, MutexGuard, PoisonError,
    atomic::{AtomicBool, Ordering},
};
use std::time::Duration;

use huddle_catalog::providers::Provider;
use huddle_config::{env::Env, paths::Paths};
use huddle_keystore::port::{Keystore, KeystoreError, SecretString, platform_parts};
use tokio::sync::watch;

/// How long one request waits for the lane's key lookup.
pub const KEY_BUDGET: Duration = Duration::from_millis(2000);

/// How long one lookup waits on the OS store before the key file gets its turn.
pub const STORE_SHARE: Duration = Duration::from_millis(1500);

/// One lookup's outcome.
#[derive(Clone)]
pub enum KeyAnswer {
    /// The key a request signs with.
    Found(Arc<SecretString>),
    /// No key is set anywhere.
    NoKey,
    /// A key file that cannot be used; the text names the path and the cure, never a key.
    Unusable(String),
    /// The lookup did not answer within the caller's budget.
    TimedOut,
}

/// One read of a key source, run on a blocking thread.
pub type KeyRead = Arc<dyn Fn() -> Result<Option<SecretString>, KeystoreError> + Send + Sync>;

/// Where one lane's key comes from, in lookup order.
pub struct KeySources {
    pub env_key: Option<SecretString>,
    pub os: Option<KeyRead>,
    pub file: KeyRead,
}

/// The state every reader of one lane shares. `held` keeps a `Found` answer until `clear`;
/// `pending` is the one lookup in flight; `os_busy` keeps a second OS read from piling on a
/// hung one.
struct CacheState {
    held: Option<KeyAnswer>,
    pending: Option<watch::Receiver<Option<KeyAnswer>>>,
    os_busy: Arc<AtomicBool>,
}

/// The lane's provider key: one in-flight lookup every reader shares, its answer kept until the
/// provider refuses it. The lookup runs under no caller's budget, so one request's end never
/// ends the shared read, and a hung store is asked once instead of once per request.
pub struct KeyCache {
    sources: KeySources,
    store_share: Duration,
    state: Arc<Mutex<CacheState>>,
}

/// The state lock, kept through a poisoning: the state is plain data no writer leaves broken.
fn lock(state: &Mutex<CacheState>) -> MutexGuard<'_, CacheState> {
    state.lock().unwrap_or_else(PoisonError::into_inner)
}

impl KeyCache {
    pub fn new(sources: KeySources, store_share: Duration) -> KeyCache {
        KeyCache {
            sources,
            store_share,
            state: Arc::new(Mutex::new(CacheState {
                held: None,
                pending: None,
                os_busy: Arc::new(AtomicBool::new(false)),
            })),
        }
    }

    /// The kept answer at once, else joins or starts the one lookup and waits up to `budget`.
    /// The budget bounds this wait, never the lookup: it runs on under its own steps and later
    /// requests join it.
    pub async fn get(&self, budget: Duration) -> KeyAnswer {
        let mut rx = {
            let mut state = lock(&self.state);
            if let Some(held) = &state.held {
                return held.clone();
            }
            match &state.pending {
                Some(rx) => rx.clone(),
                None => {
                    let (tx, rx) = watch::channel(None);
                    state.pending = Some(rx.clone());
                    let lookup = Lookup {
                        env_key: self.sources.env_key.clone(),
                        os: self.sources.os.clone(),
                        file: Arc::clone(&self.sources.file),
                        store_share: self.store_share,
                        os_busy: Arc::clone(&state.os_busy),
                        state: Arc::clone(&self.state),
                    };
                    tokio::spawn(run_lookup(tx, lookup));
                    rx
                }
            }
        };
        match tokio::time::timeout(budget, rx.wait_for(Option::is_some)).await {
            Ok(Ok(answer)) => (*answer).clone().unwrap_or(KeyAnswer::TimedOut),
            // The budget ended, or the lookup went away without an answer: the request is
            // answered by a timeout, never a silent no key.
            Ok(Err(_)) | Err(_) => KeyAnswer::TimedOut,
        }
    }

    /// Forgets a kept key. A lookup in flight keeps its place.
    pub fn clear(&self) {
        lock(&self.state).held = None;
    }
}

/// The one lookup a lane runs, on its own steps and under no caller's budget.
struct Lookup {
    env_key: Option<SecretString>,
    os: Option<KeyRead>,
    file: KeyRead,
    store_share: Duration,
    os_busy: Arc<AtomicBool>,
    state: Arc<Mutex<CacheState>>,
}

/// Runs one lookup, keeps a found answer and hands the outcome to every waiter.
async fn run_lookup(tx: watch::Sender<Option<KeyAnswer>>, lookup: Lookup) {
    let answer = lookup.run().await;
    let mut state = lock(&lookup.state);
    if matches!(answer, KeyAnswer::Found(_)) {
        state.held = Some(answer.clone());
    }
    state.pending = None;
    drop(state);
    let _ = tx.send(Some(answer));
}

impl Lookup {
    /// The sources in order: the environment, the OS store under its share, then the file.
    async fn run(&self) -> KeyAnswer {
        if let Some(key) = &self.env_key {
            return KeyAnswer::Found(Arc::new(key.clone()));
        }
        let mut store_silent = false;
        if let Some(os) = &self.os {
            // One OS read at a time: a hung store's blocking thread stays alone until it
            // finishes, so the next lookup reads the file instead of piling on the store.
            if !self.os_busy.swap(true, Ordering::SeqCst) {
                match tokio::time::timeout(
                    self.store_share,
                    spawn_read(Arc::clone(os), Arc::clone(&self.os_busy)),
                )
                .await
                {
                    Ok(Ok(Ok(Some(key)))) => return KeyAnswer::Found(Arc::new(key)),
                    // No key in the store, or the store refused: the file still gets its turn.
                    Ok(Ok(Ok(None))) | Ok(Ok(Err(_))) => {}
                    // The store did not answer in its share; its thread stays alone and the
                    // file gets the rest of the budget.
                    Err(_share) => store_silent = true,
                    // The read itself died; nothing else clears the busy flag.
                    Ok(Err(_join)) => self.os_busy.store(false, Ordering::SeqCst),
                }
            }
        }
        let file = Arc::clone(&self.file);
        match tokio::task::spawn_blocking(move || file()).await {
            Ok(Ok(Some(key))) => KeyAnswer::Found(Arc::new(key)),
            // The file cannot be used: name the path and the cure, never a silent no key.
            Ok(Err(error)) => KeyAnswer::Unusable(error.to_string()),
            // No key anywhere; a store that stayed silent turns none into a timeout.
            Ok(Ok(None)) if store_silent => KeyAnswer::TimedOut,
            Ok(Ok(None)) => KeyAnswer::NoKey,
            Err(join) => KeyAnswer::Unusable(join.to_string()),
        }
    }
}

/// Runs one read on a blocking thread and clears the busy flag once it finishes.
fn spawn_read(
    read: KeyRead,
    busy: Arc<AtomicBool>,
) -> tokio::task::JoinHandle<Result<Option<SecretString>, KeystoreError>> {
    tokio::task::spawn_blocking(move || {
        let answer = read();
        busy.store(false, Ordering::SeqCst);
        answer
    })
}

/// Builds one lane's sources: the first of `key_env` set and non-empty after trim, then the OS
/// store, then the file.
pub fn sources_for(provider: &'static Provider, env: &Env, paths: &Paths) -> KeySources {
    let env_key = provider.key_env.iter().find_map(|name| {
        env.get(name)
            .map(|value| value.trim())
            .filter(|value| !value.is_empty())
            .map(|value| SecretString::new(value.to_string()))
    });
    let (os, file) = platform_parts(paths);
    KeySources {
        env_key,
        os: os.map(|store| -> KeyRead { Arc::new(move || store.get(provider.id)) }),
        file: Arc::new(move || file.get(provider.id)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_keystore::port::{KeystoreError, SecretString};
    use std::{
        sync::atomic::{AtomicUsize, Ordering},
        time::Duration,
    };

    fn key(value: &str) -> SecretString {
        SecretString::new(value.to_string())
    }

    fn counted(calls: Arc<AtomicUsize>, answer: Option<&'static str>) -> KeyRead {
        Arc::new(move || {
            calls.fetch_add(1, Ordering::SeqCst);
            Ok(answer.map(key))
        })
    }

    fn sources(env_key: Option<SecretString>, os: Option<KeyRead>, file: KeyRead) -> KeySources {
        KeySources { env_key, os, file }
    }

    #[tokio::test]
    async fn an_environment_key_wins_and_nothing_else_is_read() {
        let file_calls = Arc::new(AtomicUsize::new(0));
        let cache = KeyCache::new(
            sources(
                Some(key("from-env")),
                None,
                counted(Arc::clone(&file_calls), Some("from-file")),
            ),
            STORE_SHARE,
        );
        match cache.get(KEY_BUDGET).await {
            KeyAnswer::Found(found) => assert_eq!(found.expose(), "from-env"),
            _ => panic!("want the env key"),
        }
        assert_eq!(file_calls.load(Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn the_os_store_is_read_before_the_file() {
        let file_calls = Arc::new(AtomicUsize::new(0));
        let os: KeyRead = Arc::new(|| Ok(Some(key("from-os"))));
        let cache = KeyCache::new(
            sources(
                None,
                Some(os),
                counted(Arc::clone(&file_calls), Some("from-file")),
            ),
            STORE_SHARE,
        );
        match cache.get(KEY_BUDGET).await {
            KeyAnswer::Found(found) => assert_eq!(found.expose(), "from-os"),
            _ => panic!("want the os key"),
        }
        assert_eq!(file_calls.load(Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn a_found_key_is_kept_and_only_read_once() {
        let calls = Arc::new(AtomicUsize::new(0));
        let cache = KeyCache::new(
            sources(
                None,
                None,
                counted(Arc::clone(&calls), Some("kept-key-kept-key")),
            ),
            STORE_SHARE,
        );
        for _ in 0..3 {
            assert!(matches!(cache.get(KEY_BUDGET).await, KeyAnswer::Found(_)));
        }
        assert_eq!(calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_lookup_that_found_nothing_is_not_kept() {
        let calls = Arc::new(AtomicUsize::new(0));
        let cache = KeyCache::new(
            sources(None, None, counted(Arc::clone(&calls), None)),
            STORE_SHARE,
        );
        assert!(matches!(cache.get(KEY_BUDGET).await, KeyAnswer::NoKey));
        assert!(matches!(cache.get(KEY_BUDGET).await, KeyAnswer::NoKey));
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    #[tokio::test]
    async fn clear_forgets_the_kept_key() {
        let calls = Arc::new(AtomicUsize::new(0));
        let cache = KeyCache::new(
            sources(
                None,
                None,
                counted(Arc::clone(&calls), Some("key-before-rotate")),
            ),
            STORE_SHARE,
        );
        assert!(matches!(cache.get(KEY_BUDGET).await, KeyAnswer::Found(_)));
        cache.clear();
        assert!(matches!(cache.get(KEY_BUDGET).await, KeyAnswer::Found(_)));
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    #[tokio::test]
    async fn concurrent_readers_share_one_lookup() {
        let calls = Arc::new(AtomicUsize::new(0));
        let inner = Arc::clone(&calls);
        let slow: KeyRead = Arc::new(move || {
            inner.fetch_add(1, Ordering::SeqCst);
            std::thread::sleep(Duration::from_millis(50));
            Ok(Some(key("shared-key-shared")))
        });
        let cache = Arc::new(KeyCache::new(sources(None, None, slow), STORE_SHARE));
        let mut tasks = Vec::new();
        for _ in 0..5 {
            let cache = Arc::clone(&cache);
            tasks.push(tokio::spawn(async move { cache.get(KEY_BUDGET).await }));
        }
        for task in tasks {
            assert!(matches!(task.await.expect("join"), KeyAnswer::Found(_)));
        }
        assert_eq!(calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_store_that_hangs_yields_the_file_and_a_second_lookup_never_piles_on() {
        let os_calls = Arc::new(AtomicUsize::new(0));
        let inner = Arc::clone(&os_calls);
        let hung: KeyRead = Arc::new(move || {
            inner.fetch_add(1, Ordering::SeqCst);
            std::thread::sleep(Duration::from_millis(200));
            Ok(None)
        });
        let cache = KeyCache::new(
            sources(
                None,
                Some(hung),
                Arc::new(|| Ok(Some(key("file-key-file-key")))),
            ),
            Duration::from_millis(20),
        );
        match cache.get(Duration::from_secs(1)).await {
            KeyAnswer::Found(found) => assert_eq!(found.expose(), "file-key-file-key"),
            _ => panic!("want the file key past the store's share"),
        }
        assert_eq!(os_calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_store_that_hangs_with_no_file_key_is_a_timeout_not_a_missing_key() {
        let hung: KeyRead = Arc::new(|| {
            std::thread::sleep(Duration::from_millis(100));
            Ok(None)
        });
        let cache = KeyCache::new(
            sources(None, Some(hung), Arc::new(|| Ok(None))),
            Duration::from_millis(10),
        );
        assert!(matches!(
            cache.get(Duration::from_secs(1)).await,
            KeyAnswer::TimedOut
        ));
    }

    #[tokio::test]
    async fn a_budget_that_ends_first_is_a_timeout_and_the_lookup_keeps_running() {
        let calls = Arc::new(AtomicUsize::new(0));
        let inner = Arc::clone(&calls);
        let slow: KeyRead = Arc::new(move || {
            inner.fetch_add(1, Ordering::SeqCst);
            std::thread::sleep(Duration::from_millis(100));
            Ok(Some(key("late-key-late-key")))
        });
        let cache = KeyCache::new(sources(None, None, slow), STORE_SHARE);
        assert!(matches!(
            cache.get(Duration::from_millis(10)).await,
            KeyAnswer::TimedOut
        ));
        tokio::time::sleep(Duration::from_millis(150)).await;
        assert!(matches!(cache.get(KEY_BUDGET).await, KeyAnswer::Found(_)));
        assert_eq!(calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn an_unusable_key_file_says_so_instead_of_no_key() {
        let bad: KeyRead = Arc::new(|| {
            Err(KeystoreError::InsecureFile {
                path: "/x/env".into(),
                mode: 0o644,
            })
        });
        let cache = KeyCache::new(sources(None, None, bad), STORE_SHARE);
        match cache.get(KEY_BUDGET).await {
            KeyAnswer::Unusable(text) => assert!(text.contains("chmod 600"), "{text}"),
            _ => panic!("want Unusable"),
        }
    }
}
