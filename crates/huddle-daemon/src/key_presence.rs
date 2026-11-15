//! Which providers hold a key, asked of the key store at most once per window and shared by every
//! reader. A store call forks a tool on macOS, so the answer is kept; only yes or no is held.

use std::collections::HashMap;
use std::sync::{Mutex, PoisonError};
use std::time::{Duration, Instant};

/// How long one answer is kept.
pub const WINDOW: Duration = Duration::from_secs(60);

type Answer = HashMap<&'static str, bool>;

pub struct KeyPresence {
    ask: Box<dyn Fn() -> Answer + Send + Sync>,
    now: Box<dyn Fn() -> Instant + Send + Sync>,
    window: Duration,
    last: Mutex<Option<(Instant, Answer)>>,
}

impl KeyPresence {
    pub fn new(
        ask: impl Fn() -> Answer + Send + Sync + 'static,
        now: impl Fn() -> Instant + Send + Sync + 'static,
        window: Duration,
    ) -> KeyPresence {
        KeyPresence {
            ask: Box::new(ask),
            now: Box::new(now),
            window,
            last: Mutex::new(None),
        }
    }

    /// The kept answer, or a fresh one when none is kept or it is `window` old. The lock is held
    /// while asking, so concurrent readers share one call. Runs on a blocking thread.
    pub fn get(&self) -> Answer {
        let mut last = self.last.lock().unwrap_or_else(PoisonError::into_inner);
        let now = (self.now)();
        if let Some((at, answer)) = last.as_ref()
            && now.saturating_duration_since(*at) < self.window
        {
            return answer.clone();
        }
        let answer = (self.ask)();
        *last = Some((now, answer.clone()));
        answer
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};

    fn counted() -> (KeyPresence, Arc<AtomicUsize>, Arc<AtomicU64>) {
        let asks = Arc::new(AtomicUsize::new(0));
        let offset = Arc::new(AtomicU64::new(0));
        let base = Instant::now();
        let presence = KeyPresence::new(
            {
                let asks = Arc::clone(&asks);
                move || {
                    asks.fetch_add(1, Ordering::SeqCst);
                    HashMap::from([("zai", true)])
                }
            },
            {
                let offset = Arc::clone(&offset);
                move || base + Duration::from_secs(offset.load(Ordering::SeqCst))
            },
            WINDOW,
        );
        (presence, asks, offset)
    }

    #[test]
    fn l20_key_presence_is_cached_for_60_seconds() {
        let (presence, asks, offset) = counted();
        assert_eq!(presence.get().get("zai"), Some(&true));
        offset.store(59, Ordering::SeqCst);
        presence.get();
        assert_eq!(asks.load(Ordering::SeqCst), 1);
        offset.store(60, Ordering::SeqCst);
        presence.get();
        assert_eq!(asks.load(Ordering::SeqCst), 2);
    }
}
