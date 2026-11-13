//! A read-only, typed view of Claude Code's session logs and of the projects' git worktrees.
//!
//! Only the fields the admin panel shows are decoded: message text, tool input and tool output
//! are never read into memory.

mod log;
mod model;
mod repo;
mod scan;
mod state;
mod tail;
mod time;
mod usage_for;
mod worktrees;

pub use model::{
    Agent, AgentState, Limits, RequestTag, Session, SessionState, Usage, Worktree, WorktreeKind,
};
pub use scan::scan;
pub use time::parse_iso_ms;
pub use usage_for::{LogUsage, usage_for};
pub use worktrees::worktrees;
