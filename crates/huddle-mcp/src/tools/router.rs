//! `huddle_router_status`, `huddle_errors` and `huddle_router_log`: whether the router is up
//! and how its Anthropic path fares, which requests failed and why, and the router's log
//! lines. Nothing here carries a key, a prompt or a header value.

use std::collections::BTreeMap;

use huddle_history::{Filter, History, Page, StatusClass};
use rmcp::handler::server::wrapper::Parameters;
use rmcp::schemars::{self, JsonSchema};
use rmcp::{ErrorData, Json, tool, tool_router};
use serde::{Deserialize, Serialize};

use super::usage::{RangeInfo, UsageArgs, now_ms, range_of};
use crate::router_log::{Kind, Query, lines_of, window_ms};
use crate::server::HuddleServer;
use crate::sources::Sources;

const START_COMMAND: &str = "huddle router start";
const EVENTS_SHOWN: usize = 5;
const PAGE: u64 = 500;
const MAX_ROWS: u64 = 20_000;

/// The Anthropic path: whether requests to Anthropic get through.
#[derive(Debug, Serialize, JsonSchema)]
pub struct AnthropicPath {
    /// `ok` or `failing`.
    pub state: String,
    /// When the path started failing, epoch milliseconds.
    pub since_ms: Option<u64>,
    /// The error code, such as `ETIMEDOUT`.
    pub code: Option<String>,
    /// Where the failure happened, such as `connect`.
    pub phase: Option<String>,
    /// The address that did not answer.
    pub peer: Option<String>,
    pub failed: Option<u64>,
    pub hint: Option<String>,
}

/// One recent router event.
#[derive(Debug, Serialize, JsonSchema)]
pub struct RouterEvent {
    pub at_ms: u64,
    pub kind: String,
    pub text: String,
}

/// What `huddle_router_status` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct RouterStatus {
    pub up: bool,
    pub pid: Option<u64>,
    pub version: Option<String>,
    pub uptime_ms: Option<u64>,
    /// The lanes the router serves, Anthropic included.
    pub providers: Vec<String>,
    /// Absent while the router is down.
    pub anthropic_path: Option<AnthropicPath>,
    /// The last five events, oldest first.
    pub events: Vec<RouterEvent>,
    /// While the router is down: the last exit line in its log.
    pub last_exit: Option<String>,
    /// While the router is down: the command that starts it.
    pub start_command: Option<String>,
}

#[derive(Debug, Default, Deserialize)]
struct StateShape {
    pid: Option<u64>,
    version: Option<String>,
    started_ms: Option<u64>,
    now_ms: Option<u64>,
    #[serde(default)]
    router: RouterShape,
    #[serde(default)]
    lanes: Vec<LaneShape>,
}

#[derive(Debug, Default, Deserialize)]
struct RouterShape {
    #[serde(default)]
    events: Vec<EventShape>,
    path: Option<PathShape>,
}

#[derive(Debug, Deserialize)]
struct EventShape {
    at_ms: u64,
    kind: String,
    text: String,
}

#[derive(Debug, Deserialize)]
struct PathShape {
    state: String,
    since_ms: Option<u64>,
    code: Option<String>,
    phase: Option<String>,
    peer: Option<String>,
    failed: Option<u64>,
    hint: Option<String>,
}

#[derive(Debug, Deserialize)]
struct LaneShape {
    id: String,
    #[serde(default)]
    enabled: bool,
}

pub fn router_status(sources: &dyn Sources) -> RouterStatus {
    let state = sources
        .router_state()
        .and_then(|value| serde_json::from_value::<StateShape>(value).ok());
    let Some(state) = state else {
        return RouterStatus {
            up: sources.router_up(),
            pid: None,
            version: None,
            uptime_ms: None,
            providers: Vec::new(),
            anthropic_path: None,
            events: Vec::new(),
            last_exit: last_exit(&sources.state_dir()),
            start_command: Some(START_COMMAND.to_string()),
        };
    };
    let uptime_ms = match (state.now_ms, state.started_ms) {
        (Some(now), Some(started)) => Some(now.saturating_sub(started)),
        _ => None,
    };
    let skip = state.router.events.len().saturating_sub(EVENTS_SHOWN);
    RouterStatus {
        up: true,
        pid: state.pid,
        version: state.version,
        uptime_ms,
        providers: state
            .lanes
            .into_iter()
            .filter(|lane| lane.enabled)
            .map(|lane| lane.id)
            .collect(),
        anthropic_path: state.router.path.map(|path| AnthropicPath {
            state: path.state,
            since_ms: path.since_ms,
            code: path.code,
            phase: path.phase,
            peer: path.peer,
            failed: path.failed,
            hint: path.hint,
        }),
        events: state
            .router
            .events
            .into_iter()
            .skip(skip)
            .map(|event| RouterEvent {
                at_ms: event.at_ms,
                kind: event.kind,
                text: event.text,
            })
            .collect(),
        last_exit: None,
        start_command: None,
    }
}

/// The newest exit line of the router's event log, with its timestamp.
fn last_exit(state_dir: &std::path::Path) -> Option<String> {
    let query = Query {
        kind: Kind::Events,
        since: None,
    };
    lines_of(state_dir, &query)
        .into_iter()
        .rev()
        .find(|line| crate::router_log::text_of(line).starts_with("huddle-router: exit "))
}

/// What `huddle_errors` takes.
#[derive(Debug, Default, Deserialize, JsonSchema)]
pub struct ErrorsArgs {
    /// A preset window ending now: 15m, 1h, 24h, 7d, 30d or 90d. Default 24h. Ignored when
    /// `from_ms` is given.
    pub range: Option<String>,
    /// Start of a custom window, epoch milliseconds, inclusive.
    pub from_ms: Option<u64>,
    /// End of a custom window, epoch milliseconds, exclusive. Default now.
    pub to_ms: Option<u64>,
}

/// Failed requests of one provider, model, status and reason.
#[derive(Debug, Serialize, JsonSchema)]
pub struct ErrorGroup {
    pub provider: String,
    pub model: String,
    pub status: u16,
    /// A short plain reason such as `quota used up` or `overloaded`.
    pub reason: String,
    pub count: u64,
    pub first_ms: u64,
    pub last_ms: u64,
}

/// What `huddle_errors` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct ErrorsAnswer {
    pub range: RangeInfo,
    /// Failed requests in the window.
    pub total: u64,
    /// Most requests first.
    pub groups: Vec<ErrorGroup>,
    /// True when the window holds more failures than the groups cover.
    pub truncated: bool,
}

/// The plain reason a failed request gets, from its status and the upstream's own words.
fn reason_label(status: u16, upstream: Option<&str>) -> &'static str {
    let said = upstream.unwrap_or_default().to_lowercase();
    let mentions = |words: &[&str]| words.iter().any(|word| said.contains(word));
    if status == 529 || status == 503 || mentions(&["overload"]) {
        "overloaded"
    } else if status == 429 && mentions(&["quota", "balance", "insufficient", "exceed", "used up"])
    {
        "quota used up"
    } else if status == 429 {
        "rate limited"
    } else if status == 401 || status == 403 {
        "key or access refused"
    } else if (400..500).contains(&status) {
        "request refused"
    } else if (500..600).contains(&status) {
        "provider error"
    } else {
        "failed"
    }
}

pub fn errors(
    args: &ErrorsArgs,
    now_ms: u64,
    sources: &dyn Sources,
) -> Result<ErrorsAnswer, String> {
    let range = range_of(
        &UsageArgs {
            range: args.range.clone(),
            from_ms: args.from_ms,
            to_ms: args.to_ms,
            group_by: None,
        },
        now_ms,
    )?;
    let info = RangeInfo {
        from_ms: range.from_ms,
        to_ms: range.to_ms,
    };
    let Some(history) = sources.open_history() else {
        return Ok(ErrorsAnswer {
            range: info,
            total: 0,
            groups: Vec::new(),
            truncated: false,
        });
    };
    let (groups, total) = group_errors(&history, range).map_err(|error| error.to_string())?;
    let covered: u64 = groups.iter().map(|group| group.count).sum();
    Ok(ErrorsAnswer {
        range: info,
        total,
        truncated: covered < total,
        groups,
    })
}

fn group_errors(
    history: &History,
    range: huddle_history::Range,
) -> Result<(Vec<ErrorGroup>, u64), huddle_history::HistoryError> {
    let mut filter = Filter::new(range);
    filter.status = StatusClass::Error;
    let mut by_key: BTreeMap<(String, String, u16, &'static str), ErrorGroup> = BTreeMap::new();
    let mut offset = 0;
    let mut total = 0;
    while offset < MAX_ROWS {
        let page = history.search(
            filter.clone(),
            Page {
                limit: PAGE,
                offset,
            },
        )?;
        total = page.total;
        for row in &page.rows {
            let reason = reason_label(row.status, row.reason.as_deref());
            let key = (row.lane.clone(), row.model.clone(), row.status, reason);
            let group = by_key.entry(key).or_insert_with(|| ErrorGroup {
                provider: row.lane.clone(),
                model: row.model.clone(),
                status: row.status,
                reason: reason.to_string(),
                count: 0,
                first_ms: row.at_ms,
                last_ms: row.at_ms,
            });
            group.count += 1;
            group.first_ms = group.first_ms.min(row.at_ms);
            group.last_ms = group.last_ms.max(row.at_ms);
        }
        offset += PAGE;
        if (page.rows.len() as u64) < PAGE {
            break;
        }
    }
    let mut groups: Vec<ErrorGroup> = by_key.into_values().collect();
    groups.sort_by(|a, b| b.count.cmp(&a.count).then(b.last_ms.cmp(&a.last_ms)));
    Ok((groups, total))
}

/// Which log lines `huddle_router_log` reads.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "lowercase")]
pub enum LogKind {
    #[default]
    Events,
    Requests,
    All,
}

/// What `huddle_router_log` takes.
#[derive(Debug, Default, Deserialize, JsonSchema)]
pub struct LogArgs {
    /// `events` (default), `requests` or `all`.
    pub kind: Option<LogKind>,
    /// Only lines from the last window: `30m` or `2h`. Without it, the last 40 lines.
    pub since: Option<String>,
    /// Keep only the newest this many lines.
    pub lines: Option<usize>,
}

/// What `huddle_router_log` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct LogAnswer {
    /// Oldest first, as `huddle router log` prints them.
    pub lines: Vec<String>,
}

pub fn router_log(args: &LogArgs, now_ms: u64, sources: &dyn Sources) -> Result<LogAnswer, String> {
    let since = match args.since.as_deref() {
        None => None,
        Some(text) => {
            let window = window_ms(text).ok_or("since must look like 30m or 2h")?;
            Some(huddle_jobs::id::iso_ms(
                now_ms.saturating_sub(window as u64),
            ))
        }
    };
    let kind = match args.kind.unwrap_or_default() {
        LogKind::Events => Kind::Events,
        LogKind::Requests => Kind::Requests,
        LogKind::All => Kind::All,
    };
    let mut lines = lines_of(&sources.state_dir(), &Query { kind, since });
    if let Some(keep) = args.lines {
        lines.drain(..lines.len().saturating_sub(keep));
    }
    Ok(LogAnswer { lines })
}

#[tool_router(router = router_router, vis = "pub(crate)")]
impl HuddleServer {
    /// Whether the router is up, its pid, version, uptime, the providers it serves, whether
    /// the Anthropic path works, and its last five events. When it is down: its last exit
    /// line and the command that starts it.
    #[tool(name = "huddle_router_status")]
    async fn huddle_router_status(&self) -> Result<Json<RouterStatus>, ErrorData> {
        let status = self.with_sources(|sources| router_status(sources)).await?;
        Ok(Json(status))
    }

    /// Failed requests from the request history, grouped by provider, model, status and plain
    /// reason, with count and first and last time seen. Works with the router down.
    #[tool(name = "huddle_errors")]
    async fn huddle_errors(
        &self,
        Parameters(args): Parameters<ErrorsArgs>,
    ) -> Result<Json<ErrorsAnswer>, ErrorData> {
        let answer = self
            .with_sources(move |sources| errors(&args, now_ms(), sources))
            .await?;
        answer
            .map(Json)
            .map_err(|message| ErrorData::invalid_params(message, None))
    }

    /// The router's log lines, as `huddle router log` prints them: event lines by default,
    /// request lines when asked.
    #[tool(name = "huddle_router_log")]
    async fn huddle_router_log(
        &self,
        Parameters(args): Parameters<LogArgs>,
    ) -> Result<Json<LogAnswer>, ErrorData> {
        let answer = self
            .with_sources(move |sources| router_log(&args, now_ms(), sources))
            .await?;
        answer
            .map(Json)
            .map_err(|message| ErrorData::invalid_params(message, None))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reasons_read_in_plain_words() {
        assert_eq!(reason_label(429, Some("Quota exceeded")), "quota used up");
        assert_eq!(reason_label(429, None), "rate limited");
        assert_eq!(reason_label(529, None), "overloaded");
        assert_eq!(reason_label(500, Some("server overloaded")), "overloaded");
        assert_eq!(reason_label(401, None), "key or access refused");
        assert_eq!(reason_label(422, None), "request refused");
        assert_eq!(reason_label(502, None), "provider error");
        assert_eq!(reason_label(0, None), "failed");
    }

    #[test]
    fn a_dead_router_gives_the_start_command() {
        let home = std::env::temp_dir().join("huddle-mcp-router-unused");
        let sources = crate::sources::DiskSources {
            machine: crate::machine::fixtures::machine(&home, 1),
        };
        let status = router_status(&sources);
        assert!(!status.up);
        assert_eq!(status.start_command.as_deref(), Some(START_COMMAND));
        assert!(status.anthropic_path.is_none());
    }

    #[test]
    fn a_bad_window_is_refused() {
        let home = std::env::temp_dir().join("huddle-mcp-router-unused");
        let sources = crate::sources::DiskSources {
            machine: crate::machine::fixtures::machine(&home, 1),
        };
        let args = LogArgs {
            since: Some("soon".into()),
            ..LogArgs::default()
        };
        assert!(router_log(&args, 1_000, &sources).is_err());
    }
}
