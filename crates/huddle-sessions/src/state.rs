//! The session state rule, as pure functions of what the log says and the clock.

use crate::log::{Phase, Summary};
use crate::model::{AgentState, SessionState};

/// A request in flight or a tool still running; past this the process is taken as gone.
const WORKING_WINDOW_MS: u64 = 10 * 60_000;
/// How long an ended turn keeps showing as `your_turn` or `done`.
const ENDED_WINDOW_MS: u64 = 30 * 60_000;
/// A background task with no notification after this long is taken as lost.
const TASK_MAX_AGE_MS: u64 = 2 * 3_600_000;

/// A session's state with what the page needs to explain it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Standing {
    pub state: SessionState,
    pub since_ms: Option<u64>,
    pub open_tasks: u32,
    pub question_tool: Option<String>,
}

/// In order:
/// 1. `needs_you`: a question or plan approval has no answer yet (never for a subagent).
/// 2. `working`: a request is in flight or a tool is running, and the last event is under 10 min old.
/// 3. `background`: the turn ended and background tasks are open.
/// 4. `done`: the turn ended in a driven session or a subagent, within 30 min.
/// 5. `your_turn`: the turn ended in an interactive session, within 30 min.
/// 6. Otherwise `idle`.
pub(crate) fn standing(summary: &Summary, sidechain: bool, now_ms: u64) -> Standing {
    let open_tasks = summary
        .tasks
        .iter()
        .filter(|task| {
            task.at_ms
                .is_none_or(|at| now_ms.saturating_sub(at) < TASK_MAX_AGE_MS)
        })
        .count();
    let open_tasks = u32::try_from(open_tasks).unwrap_or(u32::MAX);
    let mut standing = Standing {
        state: SessionState::Idle,
        since_ms: None,
        open_tasks,
        question_tool: None,
    };
    if !sidechain && let Some(question) = summary.questions.first() {
        standing.state = SessionState::NeedsYou;
        standing.since_ms = question.at_ms;
        standing.question_tool = Some(question.tool.clone());
        return standing;
    }
    let Some(turn) = summary.last_turn else {
        return standing;
    };
    let Some(at) = turn.at_ms else {
        return standing;
    };
    let age = now_ms.saturating_sub(at);
    match turn.phase {
        Phase::Asked | Phase::Tool if age < WORKING_WINDOW_MS => {
            standing.state = SessionState::Working;
        }
        Phase::Ended if open_tasks > 0 && !sidechain => {
            standing.state = SessionState::Background;
            standing.since_ms = Some(at);
        }
        Phase::Ended if age <= ENDED_WINDOW_MS => {
            standing.state = if sidechain || summary.driven {
                SessionState::Done
            } else {
                SessionState::YourTurn
            };
            standing.since_ms = Some(at);
        }
        _ => {}
    }
    standing
}

pub(crate) fn agent_state(state: SessionState) -> AgentState {
    match state {
        SessionState::Working | SessionState::Background => AgentState::Running,
        SessionState::NeedsYou | SessionState::YourTurn => AgentState::Waiting,
        SessionState::Done => AgentState::Done,
        SessionState::Idle => AgentState::Idle,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    use crate::log::Summary;
    use crate::time::parse_iso_ms;

    fn now() -> u64 {
        parse_iso_ms("2026-11-11T12:00:00.000Z").expect("instant")
    }

    fn at(hhmmss: &str) -> u64 {
        parse_iso_ms(&format!("2026-11-11T{hhmmss}.000Z")).expect("instant")
    }

    fn assistant(ts: &str, stop: &str, content: &str) -> String {
        format!(
            r#"{{"type":"assistant","timestamp":"2026-11-11T{ts}.000Z","entrypoint":"cli","message":{{"model":"m","stop_reason":"{stop}","content":[{content}]}}}}"#
        )
    }

    fn tool_use(id: &str, name: &str, input: &str) -> String {
        format!(r#"{{"type":"tool_use","id":"{id}","name":"{name}","input":{input}}}"#)
    }

    fn result(ts: &str, id: &str, top: &str) -> String {
        format!(
            r#"{{"type":"user","timestamp":"2026-11-11T{ts}.000Z","message":{{"content":[{{"type":"tool_result","tool_use_id":"{id}","content":"SECRET OUTPUT"}}]}}{top}}}"#
        )
    }

    fn notification(ts: &str, task: &str) -> String {
        format!(
            r#"{{"type":"user","timestamp":"2026-11-11T{ts}.000Z","origin":{{"kind":"task-notification"}},"message":{{"content":"<task-notification><task-id>{task}</task-id><status>completed</status></task-notification>"}}}}"#
        )
    }

    fn standing_of(lines: &[String], sidechain: bool) -> Standing {
        standing(&Summary::parse(&lines.join("\n")), sidechain, now())
    }

    #[test]
    fn d3_an_unanswered_question_needs_you() {
        let lines = [assistant(
            "11:59:00",
            "tool_use",
            &tool_use("q1", "AskUserQuestion", r#"{"questions":[]}"#),
        )];
        let got = standing_of(&lines, false);
        assert_eq!(got.state, SessionState::NeedsYou);
        assert_eq!(got.since_ms, Some(at("11:59:00")));
        assert_eq!(got.question_tool.as_deref(), Some("AskUserQuestion"));
        let plan = [assistant(
            "11:59:00",
            "tool_use",
            &tool_use("p1", "ExitPlanMode", "{}"),
        )];
        assert_eq!(
            standing_of(&plan, false).question_tool.as_deref(),
            Some("ExitPlanMode")
        );
    }

    #[test]
    fn d3_an_answered_question_does_not() {
        let lines = [
            assistant(
                "11:58:00",
                "tool_use",
                &tool_use("q1", "AskUserQuestion", "{}"),
            ),
            result("11:58:30", "q1", ""),
            assistant("11:59:00", "end_turn", ""),
        ];
        let got = standing_of(&lines, false);
        assert_ne!(got.state, SessionState::NeedsYou);
        assert_eq!(got.question_tool, None);
    }

    #[test]
    fn d3_open_background_tasks_mean_background() {
        let lines = [
            assistant(
                "11:50:00",
                "tool_use",
                &tool_use("b1", "Bash", r#"{"run_in_background":true}"#),
            ),
            result(
                "11:50:01",
                "b1",
                r#","toolUseResult":{"backgroundTaskId":"t-1"}"#,
            ),
            assistant("11:50:10", "tool_use", &tool_use("a1", "Agent", "{}")),
            result(
                "11:50:11",
                "a1",
                r#","toolUseResult":{"status":"async_launched","agentId":"ag-1"}"#,
            ),
            assistant("11:51:00", "end_turn", ""),
        ];
        let got = standing_of(&lines, false);
        assert_eq!((got.state, got.open_tasks), (SessionState::Background, 2));
        let mut closed = lines.to_vec();
        closed.push(notification("11:55:00", "t-1"));
        closed.push(notification("11:55:01", "ag-1"));
        closed.push(assistant("11:56:00", "end_turn", ""));
        let got = standing_of(&closed, false);
        assert_eq!((got.state, got.open_tasks), (SessionState::YourTurn, 0));
    }

    #[test]
    fn d3_background_tasks_older_than_two_hours_are_ignored() {
        let lines = [
            format!(
                r#"{{"type":"assistant","timestamp":"2026-11-11T08:00:00.000Z","message":{{"stop_reason":"tool_use","content":[{}]}}}}"#,
                tool_use("b1", "Bash", r#"{"run_in_background":true}"#)
            ),
            result(
                "08:00:01",
                "b1",
                r#","toolUseResult":{"backgroundTaskId":"t-1"}"#,
            ),
            assistant("11:59:00", "end_turn", ""),
        ];
        let got = standing_of(&lines, false);
        assert_eq!((got.state, got.open_tasks), (SessionState::YourTurn, 0));
    }

    #[test]
    fn d3_a_turn_ended_in_an_interactive_session_is_your_turn() {
        let lines = [assistant("11:59:00", "end_turn", "")];
        let got = standing_of(&lines, false);
        assert_eq!(got.state, SessionState::YourTurn);
        assert_eq!(got.since_ms, Some(at("11:59:00")));
    }

    #[test]
    fn d3_a_driven_session_that_ended_is_done() {
        let sdk = r#"{"type":"user","timestamp":"2026-11-11T11:58:00.000Z","entrypoint":"sdk-cli","promptSource":"sdk","message":{"content":"go"}}"#
            .to_string();
        let lines = [sdk, assistant("11:59:00", "end_turn", "")];
        assert_eq!(standing_of(&lines, false).state, SessionState::Done);
        let typed_sdk = [r#"{"type":"assistant","timestamp":"2026-11-11T11:59:00.000Z","entrypoint":"sdk-ts","message":{"stop_reason":"end_turn"}}"#.to_string()];
        assert_eq!(standing_of(&typed_sdk, false).state, SessionState::Done);
    }

    #[test]
    fn d3_a_subagent_never_needs_you() {
        let asking = [assistant(
            "11:59:00",
            "tool_use",
            &tool_use("q1", "AskUserQuestion", "{}"),
        )];
        assert_eq!(standing_of(&asking, true).state, SessionState::Working);
        let ended = [assistant("11:59:00", "end_turn", "")];
        assert_eq!(standing_of(&ended, true).state, SessionState::Done);
    }

    #[test]
    fn d3_the_summary_never_keeps_message_text() {
        let lines = [result("11:59:00", "x", "")];
        let summary = Summary::parse(&lines.join("\n"));
        assert!(!format!("{summary:?}").contains("SECRET"));
    }

    #[test]
    fn no_turn_is_idle() {
        assert_eq!(standing_of(&[], false).state, SessionState::Idle);
    }

    #[test]
    fn a_running_tool_is_working_for_ten_minutes_and_an_ended_turn_waits_thirty() {
        let tool = |ts| [assistant(ts, "tool_use", &tool_use("t", "Read", "{}"))];
        assert_eq!(
            standing_of(&tool("11:50:01"), false).state,
            SessionState::Working
        );
        assert_eq!(
            standing_of(&tool("11:50:00"), false).state,
            SessionState::Idle
        );
        let ended = |ts| [assistant(ts, "end_turn", "")];
        assert_eq!(
            standing_of(&ended("11:30:00"), false).state,
            SessionState::YourTurn
        );
        assert_eq!(
            standing_of(&ended("11:29:59"), false).state,
            SessionState::Idle
        );
    }
}
