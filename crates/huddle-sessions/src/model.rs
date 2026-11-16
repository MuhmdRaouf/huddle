//! The typed view of a session. Only the fields the admin panel shows; no message text.

use std::fmt;
use std::ops::Deref;
use std::sync::Arc;

use serde::{Serialize, Serializer};

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
#[serde(rename_all = "snake_case")]
pub enum SessionState {
    /// An unanswered `AskUserQuestion` or `ExitPlanMode`.
    NeedsYou,
    /// A request is in flight or a tool is running.
    Working,
    /// The turn ended and background tasks or agents are still open.
    Background,
    /// The turn ended in an interactive session.
    YourTurn,
    /// The turn ended in a driven session, or a subagent finished.
    Done,
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

/// Text the scanner keeps and every reading names: a clone shares the bytes, so a reading of
/// 30 000 request tags owns none of their strings.
#[derive(Clone, PartialEq, Eq, Hash)]
pub struct SharedStr(Arc<str>);

impl SharedStr {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl Deref for SharedStr {
    type Target = str;

    fn deref(&self) -> &str {
        &self.0
    }
}

impl From<&str> for SharedStr {
    fn from(text: &str) -> SharedStr {
        SharedStr(Arc::from(text))
    }
}

impl From<String> for SharedStr {
    fn from(text: String) -> SharedStr {
        SharedStr(Arc::from(text))
    }
}

impl PartialEq<str> for SharedStr {
    fn eq(&self, other: &str) -> bool {
        self.as_str() == other
    }
}

impl PartialEq<&str> for SharedStr {
    fn eq(&self, other: &&str) -> bool {
        self.as_str() == *other
    }
}

impl fmt::Debug for SharedStr {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(self.as_str(), f)
    }
}

impl fmt::Display for SharedStr {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl Serialize for SharedStr {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(self.as_str())
    }
}

/// One API request found in a log: the router joins it to the response's `request-id`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct RequestTag {
    pub request_id: SharedStr,
    pub agent_id: SharedStr,
    pub usage: Usage,
    /// The `stop_reason` of the request's last assistant line.
    pub stop: Option<SharedStr>,
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
    /// The git repository's folder name, or the working folder's name outside git.
    pub project: Option<String>,
    /// The project's key: the repository folder's path.
    pub repo: Option<String>,
    /// The checkout the session works in: a linked worktree's root, or the repository itself.
    pub worktree: Option<String>,
    pub cwd: Option<String>,
    pub branch: Option<String>,
    pub model: Option<String>,
    pub state: SessionState,
    pub started_ms: Option<u64>,
    pub last_ms: Option<u64>,
    /// When the current state began; null for `idle` and `working`.
    pub state_since_ms: Option<u64>,
    /// Background tasks and agents launched and not yet reported finished.
    pub open_tasks: u32,
    /// For `needs_you`: the name of the tool asking, never its text.
    pub question_tool: Option<String>,
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
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Worktree {
    pub path: String,
    pub project: String,
    /// The repository folder's path, the same key a session's `repo` carries.
    pub repo: String,
    pub branch: Option<String>,
    pub head: Option<String>,
    pub kind: WorktreeKind,
    pub used_by: Option<String>,
    pub changes: Option<u32>,
    pub last_commit_ms: Option<u64>,
}
