//! huddle-router: router events. The last event lines and the Anthropic path's state, held in
//! memory for the dashboard. The core takes every timestamp as a parameter and does no I/O.

use std::collections::VecDeque;
use std::sync::{Mutex, PoisonError};

/// Event lines the snapshot carries.
pub const EVENTS_KEPT: usize = 20;

/// The prefix every event line carries; the dashboard shows the text after it.
const PREFIX: &str = "huddle-router: ";

/// One event line: when it was logged, its kind and its text without the prefix.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct EventRow {
    pub at_ms: u64,
    pub kind: &'static str,
    pub text: String,
}

/// The Anthropic path while it fails: since when, where, and how many requests it cost.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct PathFailing {
    pub since_ms: u64,
    pub code: String,
    pub phase: String,
    pub peer: Option<String>,
    pub failed: u32,
    pub hint: String,
}

/// The last recovery: when it came, how long the path was down and how many requests failed.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
pub struct PathRecovery {
    pub at_ms: u64,
    pub after_ms: u64,
    pub failed: u32,
}

/// `router.path` in the snapshot.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct PathView {
    /// `ok` or `failing`.
    pub state: &'static str,
    #[serde(flatten)]
    pub failing: Option<PathFailing>,
    pub last_recovery: Option<PathRecovery>,
}

#[derive(Default)]
struct Inner {
    rows: VecDeque<EventRow>,
    failing: Option<PathFailing>,
    recovery: Option<PathRecovery>,
}

/// The ring of recent events and the path state. One mutex, held only inside a call.
#[derive(Default)]
pub struct RouterEvents {
    inner: Mutex<Inner>,
}

impl RouterEvents {
    /// Files one event line (with its prefix) at `now_ms`, dropping the oldest past the cap.
    pub fn note(&self, now_ms: u64, line: &str) {
        let text = line.strip_prefix(PREFIX).unwrap_or(line);
        let mut inner = self.lock();
        if inner.rows.len() == EVENTS_KEPT {
            inner.rows.pop_front();
        }
        inner.rows.push_back(EventRow {
            at_ms: now_ms,
            kind: kind_of(text),
            text: text.to_owned(),
        });
    }

    /// The path fails; called again with the new count for each further failure.
    pub fn fail(&self, failing: PathFailing) {
        self.lock().failing = Some(failing);
    }

    /// The path answers again.
    pub fn recover(&self, recovery: PathRecovery) {
        let mut inner = self.lock();
        inner.failing = None;
        inner.recovery = Some(recovery);
    }

    /// The events, oldest first, and the path state.
    pub fn view(&self) -> (Vec<EventRow>, PathView) {
        let inner = self.lock();
        let path = PathView {
            state: if inner.failing.is_some() {
                "failing"
            } else {
                "ok"
            },
            failing: inner.failing.clone(),
            last_recovery: inner.recovery,
        };
        (inner.rows.iter().cloned().collect(), path)
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

/// The kind of an event line by its text.
fn kind_of(text: &str) -> &'static str {
    const KINDS: [(&str, &str); 8] = [
        ("start ", "start"),
        ("exit ", "exit"),
        ("Anthropic path failing", "failing"),
        ("Anthropic path recovered", "recovered"),
        ("probe ", "probe"),
        ("took over", "handover"),
        ("handed over", "handover"),
        ("output: ", "output"),
    ];
    KINDS
        .iter()
        .find(|(head, _)| text.starts_with(head))
        .map_or("event", |(_, kind)| kind)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn kinds_follow_the_line_text() {
        let events = RouterEvents::default();
        for line in [
            "huddle-router: start v0.0.6 pid=1 port=4835 by=setup",
            "huddle-router: took over from pid=7",
            "huddle-router: handed over to pid 9; draining",
            "huddle-router: probe api.anthropic.com dns=ok(3 ms)",
            "huddle-router: exit pid=1 reason=SIGTERM",
            "huddle-router: now serving zai",
            "huddle-router: output: thread panicked",
        ] {
            events.note(5, line);
        }
        let kinds: Vec<_> = events.view().0.iter().map(|row| row.kind).collect();
        assert_eq!(
            kinds,
            [
                "start", "handover", "handover", "probe", "exit", "event", "output"
            ]
        );
    }

    #[test]
    fn a_recovery_clears_the_failing_state_and_keeps_the_last_recovery() {
        let events = RouterEvents::default();
        events.fail(PathFailing {
            since_ms: 1,
            code: "ETIMEDOUT".into(),
            phase: "connect".into(),
            peer: None,
            failed: 3,
            hint: "h".into(),
        });
        assert_eq!(events.view().1.state, "failing");
        let recovery = PathRecovery {
            at_ms: 9,
            after_ms: 8,
            failed: 3,
        };
        events.recover(recovery);
        let path = events.view().1;
        assert_eq!((path.state, path.failing), ("ok", None));
        assert_eq!(path.last_recovery, Some(recovery));
    }
}
