//! The typed view of a session. Only the fields the admin panel shows; no message text.

use serde::Serialize;

/// How many sessions to list, how old a log may be and how much of each log's end to read.
#[derive(Debug, Clone, Copy)]
pub struct Limits {
    pub max_age_ms: u64,
    pub max_sessions: usize,
    pub tail_bytes: u64,
}

impl Default for Limits {
    fn default() -> Limits {
        Limits {
            max_age_ms: 24 * 3_600_000,
            max_sessions: 30,
            tail_bytes: 512 * 1024,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum SessionState {
    Working,
    Waiting,
    Idle,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum AgentState {
    Running,
    Done,
    Waiting,
    Idle,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
pub struct Usage {
    pub input: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub output: u64,
}

/// One API request found in a log: the router joins it to the response's `request-id`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct RequestTag {
    pub request_id: String,
    pub agent_id: String,
    pub usage: Usage,
    /// The `stop_reason` of the request's last assistant line.
    pub stop: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Agent {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub description: String,
    pub model: Option<String>,
    pub state: AgentState,
    pub started_ms: Option<u64>,
    pub ended_ms: Option<u64>,
    pub worktree: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Session {
    pub id: String,
    pub title: String,
    pub project: Option<String>,
    pub cwd: Option<String>,
    pub branch: Option<String>,
    pub model: Option<String>,
    pub state: SessionState,
    pub started_ms: Option<u64>,
    pub last_ms: Option<u64>,
    pub waiting_since_ms: Option<u64>,
    pub context_tokens: Option<u64>,
    pub context_limit: u64,
    pub cache_ttl_ms: u64,
    pub last_request_ms: Option<u64>,
    pub agents: Vec<Agent>,
    /// Stays in the daemon: the page never receives request ids.
    #[serde(skip)]
    pub request_ids: Vec<RequestTag>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum WorktreeKind {
    Checkout,
    Agent,
    Job,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Worktree {
    pub path: String,
    pub project: String,
    pub branch: Option<String>,
    pub head: Option<String>,
    pub kind: WorktreeKind,
    pub used_by: Option<String>,
    pub changes: Option<u32>,
    pub last_commit_ms: Option<u64>,
}
