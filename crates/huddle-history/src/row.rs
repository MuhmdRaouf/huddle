/// One finished request. Times are epoch milliseconds; a token count of `None` is unknown.
/// No prompt, key or header value has a field here, so none can reach the database.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Row {
    pub at_ms: u64,
    pub session: Option<String>,
    pub project: Option<String>,
    pub agent: Option<String>,
    pub model: String,
    pub lane: String,
    pub status: u16,
    pub reason: Option<String>,
    pub ttft_ms: Option<u64>,
    pub dur_ms: u64,
    pub tokens_in: Option<u64>,
    pub cache_read: Option<u64>,
    pub cache_write: Option<u64>,
    pub tokens_out: Option<u64>,
    pub stop: Option<String>,
    pub upstream_id: Option<String>,
}

/// Usage and attribution read later from a session log, matched to a row by `upstream_id`.
/// `usage` is (input, cache read, cache write, output).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Credit {
    pub upstream_id: String,
    pub agent: Option<String>,
    pub project: Option<String>,
    pub usage: (u64, u64, u64, u64),
    pub stop: Option<String>,
}

/// A half-open time window: `from_ms <= at_ms < to_ms`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Range {
    pub from_ms: u64,
    pub to_ms: u64,
}
