//! `huddle_usage`: tokens, requests, errors and API-price cost from the request history.

use std::time::{SystemTime, UNIX_EPOCH};

use huddle_catalog::prices::{TokenCounts, cost_usd};
use huddle_history::{GroupBy, Range, Total};
use rmcp::handler::server::wrapper::Parameters;
use rmcp::schemars::{self, JsonSchema};
use rmcp::{ErrorData, Json, tool, tool_router};
use serde::{Deserialize, Serialize};

use crate::server::HuddleServer;
use crate::sources::Sources;

const MINUTE: u64 = 60_000;
const HOUR: u64 = 60 * MINUTE;
const DAY: u64 = 24 * HOUR;

/// What a group is keyed by.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Deserialize, Serialize, JsonSchema)]
#[serde(rename_all = "lowercase")]
pub enum UsageGroupBy {
    #[default]
    Provider,
    Model,
    Session,
    Project,
}

impl UsageGroupBy {
    fn history(self) -> GroupBy {
        match self {
            UsageGroupBy::Provider => GroupBy::Lane,
            UsageGroupBy::Model => GroupBy::Model,
            UsageGroupBy::Session => GroupBy::Session,
            UsageGroupBy::Project => GroupBy::Project,
        }
    }
}

/// What `huddle_usage` takes.
#[derive(Debug, Default, Deserialize, JsonSchema)]
pub struct UsageArgs {
    /// A preset window ending now: 15m, 1h, 24h, 7d, 30d or 90d. Default 24h. Ignored when
    /// `from_ms` is given.
    pub range: Option<String>,
    /// Start of a custom window, epoch milliseconds, inclusive.
    pub from_ms: Option<u64>,
    /// End of a custom window, epoch milliseconds, exclusive. Default now.
    pub to_ms: Option<u64>,
    /// What to group by: provider, model, session or project. Default provider.
    pub group_by: Option<UsageGroupBy>,
}

/// The window the answer covers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct RangeInfo {
    pub from_ms: u64,
    pub to_ms: u64,
}

/// Token sums. `fresh` is input that did not come from the cache.
#[derive(Debug, Default, Serialize, JsonSchema)]
pub struct Tokens {
    pub fresh: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub output: u64,
}

/// One group's sums.
#[derive(Debug, Serialize, JsonSchema)]
pub struct UsageGroup {
    /// The provider, model, session or project; `unknown` when the row has none.
    pub key: String,
    pub requests: u64,
    pub errors: u64,
    pub tokens: Tokens,
    /// Cost at API list price. Plans bill flat, so this is what the traffic would cost on the API.
    pub cost_usd: f64,
}

/// What `huddle_usage` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct UsageAnswer {
    pub range: RangeInfo,
    pub group_by: UsageGroupBy,
    pub groups: Vec<UsageGroup>,
    pub total: UsageGroup,
}

fn preset_ms(text: &str) -> Option<u64> {
    Some(match text {
        "15m" => 15 * MINUTE,
        "1h" => HOUR,
        "24h" => DAY,
        "7d" => 7 * DAY,
        "30d" => 30 * DAY,
        "90d" => 90 * DAY,
        _ => return None,
    })
}

/// The window the arguments name, at time `now_ms`.
pub fn range_of(args: &UsageArgs, now_ms: u64) -> Result<Range, String> {
    if let Some(from_ms) = args.from_ms {
        let to_ms = args.to_ms.unwrap_or(now_ms + 1);
        if to_ms <= from_ms {
            return Err("to_ms must be after from_ms".to_string());
        }
        return Ok(Range { from_ms, to_ms });
    }
    let length = match args.range.as_deref() {
        None => DAY,
        Some(text) => preset_ms(text)
            .ok_or_else(|| "range must be 15m, 1h, 24h, 7d, 30d or 90d".to_string())?,
    };
    Ok(Range {
        from_ms: now_ms.saturating_sub(length),
        to_ms: args.to_ms.unwrap_or(now_ms + 1),
    })
}

fn group_of(total: &Total) -> UsageGroup {
    let cost = total
        .models
        .iter()
        .map(|usage| {
            let counts = TokenCounts {
                input: usage.tok_in,
                output: usage.tok_out,
                cache_read: usage.cache_read,
                cache_write: usage.cache_write,
            };
            cost_usd(&usage.model, &counts)
        })
        .fold(0.0, |sum, cost| sum + cost);
    UsageGroup {
        key: if total.key.is_empty() {
            "unknown".to_string()
        } else {
            total.key.clone()
        },
        requests: total.n,
        errors: total.err,
        tokens: Tokens {
            fresh: total.tok_in,
            cache_read: total.cache_read,
            cache_write: total.cache_write,
            output: total.tok_out,
        },
        cost_usd: cost,
    }
}

/// Usage for the window and grouping the arguments name; an empty answer without a database.
pub fn usage(args: &UsageArgs, now_ms: u64, sources: &dyn Sources) -> Result<UsageAnswer, String> {
    let range = range_of(args, now_ms)?;
    let group_by = args.group_by.unwrap_or_default();
    let totals = match sources.open_history() {
        None => Vec::new(),
        Some(history) => history
            .totals(range, group_by.history())
            .map_err(|err| format!("cannot read the history: {err}"))?,
    };
    let mut groups: Vec<UsageGroup> = totals.iter().map(group_of).collect();
    groups.sort_by(|a, b| b.requests.cmp(&a.requests).then(a.key.cmp(&b.key)));
    let mut total = UsageGroup {
        key: "total".to_string(),
        requests: 0,
        errors: 0,
        tokens: Tokens::default(),
        cost_usd: 0.0,
    };
    for group in &groups {
        total.requests += group.requests;
        total.errors += group.errors;
        total.tokens.fresh += group.tokens.fresh;
        total.tokens.cache_read += group.tokens.cache_read;
        total.tokens.cache_write += group.tokens.cache_write;
        total.tokens.output += group.tokens.output;
        total.cost_usd += group.cost_usd;
    }
    Ok(UsageAnswer {
        range: RangeInfo {
            from_ms: range.from_ms,
            to_ms: range.to_ms,
        },
        group_by,
        groups,
        total,
    })
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|elapsed| elapsed.as_millis() as u64)
        .unwrap_or(0)
}

#[tool_router(router = usage_router, vis = "pub(crate)")]
impl HuddleServer {
    /// Tokens, requests, errors and cost at API price from the request history, grouped by
    /// provider, model, session or project, for a preset or custom window.
    #[tool(name = "huddle_usage")]
    async fn huddle_usage(
        &self,
        Parameters(args): Parameters<UsageArgs>,
    ) -> Result<Json<UsageAnswer>, ErrorData> {
        let answer = self
            .with_sources(move |sources| usage(&args, now_ms(), sources))
            .await?;
        answer
            .map(Json)
            .map_err(|message| ErrorData::invalid_params(message, None))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn args(range: Option<&str>, from: Option<u64>, to: Option<u64>) -> UsageArgs {
        UsageArgs {
            range: range.map(str::to_string),
            from_ms: from,
            to_ms: to,
            group_by: None,
        }
    }

    #[test]
    fn presets_end_now_and_default_to_a_day() {
        let now = 10 * DAY;
        let day = range_of(&args(None, None, None), now).unwrap();
        assert_eq!((day.from_ms, day.to_ms), (9 * DAY, now + 1));
        let quarter_hour = range_of(&args(Some("15m"), None, None), now).unwrap();
        assert_eq!(quarter_hour.from_ms, now - 15 * MINUTE);
    }

    #[test]
    fn a_custom_window_wins_over_a_preset() {
        let range = range_of(&args(Some("7d"), Some(5), Some(9)), 1_000).unwrap();
        assert_eq!((range.from_ms, range.to_ms), (5, 9));
    }

    #[test]
    fn bad_windows_are_refused() {
        assert!(range_of(&args(Some("2w"), None, None), 1_000).is_err());
        assert!(range_of(&args(None, Some(9), Some(5)), 1_000).is_err());
    }
}
