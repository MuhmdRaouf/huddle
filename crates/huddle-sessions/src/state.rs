//! The session state rule, as pure functions of the last assistant line and the clock.

use crate::log::AssistantEnd;
use crate::model::{AgentState, SessionState};

const WORKING_WINDOW_MS: u64 = 10_000;
const WAITING_WINDOW_MS: u64 = 30 * 60_000;

/// `working`: the last assistant line is under 10 s old and the turn has not ended.
/// `waiting`: the turn ended within 30 min, and `waiting_since_ms` is when. Otherwise `idle`.
pub(crate) fn session_state(
    last: Option<AssistantEnd>,
    now_ms: u64,
) -> (SessionState, Option<u64>) {
    let Some(AssistantEnd {
        at_ms: Some(at),
        end_turn,
    }) = last
    else {
        return (SessionState::Idle, None);
    };
    let age = now_ms.saturating_sub(at);
    if !end_turn && age < WORKING_WINDOW_MS {
        (SessionState::Working, None)
    } else if end_turn && age <= WAITING_WINDOW_MS {
        (SessionState::Waiting, Some(at))
    } else {
        (SessionState::Idle, None)
    }
}

pub(crate) fn agent_state(state: SessionState) -> AgentState {
    match state {
        SessionState::Working => AgentState::Running,
        SessionState::Waiting => AgentState::Waiting,
        SessionState::Idle => AgentState::Idle,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn end(at_ms: u64, end_turn: bool) -> Option<AssistantEnd> {
        Some(AssistantEnd {
            at_ms: Some(at_ms),
            end_turn,
        })
    }

    #[test]
    fn working_needs_a_fresh_unfinished_turn() {
        assert_eq!(
            session_state(end(95_000, false), 100_000).0,
            SessionState::Working
        );
        assert_eq!(
            session_state(end(80_000, false), 100_000).0,
            SessionState::Idle
        );
    }

    #[test]
    fn waiting_lasts_thirty_minutes_after_end_turn() {
        let now = 10_000_000;
        assert_eq!(
            session_state(end(now, true), now),
            (SessionState::Waiting, Some(now))
        );
        assert_eq!(
            session_state(end(now - 1_800_000, true), now).0,
            SessionState::Waiting
        );
        assert_eq!(
            session_state(end(now - 1_800_001, true), now).0,
            SessionState::Idle
        );
    }

    #[test]
    fn no_assistant_line_is_idle() {
        assert_eq!(session_state(None, 5), (SessionState::Idle, None));
    }
}
