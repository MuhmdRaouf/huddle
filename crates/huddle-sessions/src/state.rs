//! The session state rule, as pure functions of the last turn event and the clock.

use crate::log::{Phase, Turn};
use crate::model::{AgentState, SessionState};

/// A request in flight or a tool still running; past this the process is taken as gone.
const WORKING_WINDOW_MS: u64 = 10 * 60_000;
const WAITING_WINDOW_MS: u64 = 30 * 60_000;

/// `working`: a request is in flight or a tool is running, and the last event is under 10 min old.
/// `waiting`: the turn ended or was interrupted within 30 min, and `waiting_since_ms` is when.
/// Otherwise `idle`.
pub(crate) fn session_state(last: Option<Turn>, now_ms: u64) -> (SessionState, Option<u64>) {
    let Some(Turn {
        at_ms: Some(at),
        phase,
    }) = last
    else {
        return (SessionState::Idle, None);
    };
    let age = now_ms.saturating_sub(at);
    match phase {
        Phase::Asked | Phase::Tool if age < WORKING_WINDOW_MS => (SessionState::Working, None),
        Phase::Ended if age <= WAITING_WINDOW_MS => (SessionState::Waiting, Some(at)),
        _ => (SessionState::Idle, None),
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

    const NOW: u64 = 10_000_000;

    fn turn(at_ms: u64, phase: Phase) -> Option<Turn> {
        Some(Turn {
            at_ms: Some(at_ms),
            phase,
        })
    }

    #[test]
    fn a_request_in_flight_is_working() {
        assert_eq!(
            session_state(turn(NOW - 90_000, Phase::Asked), NOW).0,
            SessionState::Working
        );
    }

    #[test]
    fn a_running_tool_is_working_for_ten_minutes() {
        assert_eq!(
            session_state(turn(NOW - 599_999, Phase::Tool), NOW).0,
            SessionState::Working
        );
        assert_eq!(
            session_state(turn(NOW - 600_000, Phase::Tool), NOW).0,
            SessionState::Idle
        );
    }

    #[test]
    fn waiting_lasts_thirty_minutes_after_the_turn_ends() {
        assert_eq!(
            session_state(turn(NOW, Phase::Ended), NOW),
            (SessionState::Waiting, Some(NOW))
        );
        assert_eq!(
            session_state(turn(NOW - 1_800_000, Phase::Ended), NOW).0,
            SessionState::Waiting
        );
        assert_eq!(
            session_state(turn(NOW - 1_800_001, Phase::Ended), NOW).0,
            SessionState::Idle
        );
    }

    #[test]
    fn no_turn_is_idle() {
        assert_eq!(session_state(None, 5), (SessionState::Idle, None));
    }
}
