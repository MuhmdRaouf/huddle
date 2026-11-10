//! The harness table: one `Entry` per harness, plus the small shared types every run uses.

use std::collections::BTreeMap;
use std::time::Duration;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Access {
    Readonly,
    Exec,
    Write,
}

impl Access {
    /// "readonly", "exec" or "write".
    pub fn as_str(self) -> &'static str {
        match self {
            Access::Readonly => "readonly",
            Access::Exec => "exec",
            Access::Write => "write",
        }
    }

    /// The word, or None for anything else. Case-sensitive.
    pub fn parse(text: &str) -> Option<Access> {
        match text {
            "readonly" => Some(Access::Readonly),
            "exec" => Some(Access::Exec),
            "write" => Some(Access::Write),
            _ => None,
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    pub id: String,
    pub label: String,
    pub binary: Binary,
    pub check: Check,
    pub run: Run,
    pub wire: Wire,
    pub caps: Caps,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Binary {
    /// PATH names, tried in order.
    pub names: Vec<String>,
    /// A harness's own override variable ("ZCODE_CLI"); checked after HUDDLE_<ID>_BIN.
    pub env_var: Option<String>,
    /// Fixed install paths, tried after PATH when `os` equals std::env::consts::OS.
    pub fixed: Vec<FixedPath>,
    pub launcher: Launcher,
    /// Set only when the Electron runner is used.
    pub launch_env: Vec<(String, String)>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct FixedPath {
    /// A std::env::consts::OS value: "macos" or "linux".
    pub os: String,
    /// Absolute, or starting "~/" (expanded from the env's HOME).
    pub path: String,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Launcher {
    /// Run the binary itself.
    None,
    /// A .js, .cjs or .mjs script runs on the app's own Electron beside it, else on node (ZCode).
    ElectronOrNode,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Check {
    /// Args of the version probe, usually ["--version"].
    pub version: Vec<String>,
    /// Shown when the binary is missing.
    pub install_hint: String,
    /// Shown when the version probe or smoke run fails.
    pub login_hint: String,
    /// False: flag spellings come from paradev's table and no real CLI checked them yet.
    pub verified: bool,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Run {
    /// Always first after the cwd flag.
    pub args: Vec<String>,
    /// Flag that takes the job cwd; None: the process starts in it.
    pub cwd_flag: Option<String>,
    pub prompt: Prompt,
    /// None: the harness takes no effort setting.
    pub effort: Option<Flag>,
    /// A level missing from this map is refused for this harness.
    pub access: BTreeMap<Access, AccessSpec>,
    /// Fixed args placed right after the access args (omp's --approval-mode yolo --no-title).
    pub after_access: Vec<String>,
    pub session: Session,
    /// Longest stdout line in bytes; u64::MAX means no limit.
    pub line_cap: u64,
    /// Kill the rest of the process group after the leader exits on its own.
    pub reap_group: bool,
}

impl Run {
    /// The levels of `access`, in Access order (readonly, exec, write).
    pub fn accepted_access(&self) -> Vec<Access> {
        self.access.keys().copied().collect()
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Prompt {
    pub transport: Transport,
    /// Argv: flag placed before the prompt. File: flag that takes the file path.
    pub flag: Option<String>,
    /// File only: (flag, text) placed after the file path.
    pub argv_text: Option<(String, String)>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Transport {
    Argv,
    Stdin,
    File,
    Rpc,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Flag {
    /// Argument template, "{value}" replaced; empty: nothing is passed (ZCode's "default").
    pub args: Vec<String>,
    /// Accepted values; empty means any value is passed through.
    pub values: Vec<String>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct AccessSpec {
    pub args: Vec<String>,
    pub env: Vec<(String, String)>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Session {
    pub owner: SessionOwner,
    /// Args for a new session under a caller key; "{id}" is replaced.
    pub new: Vec<String>,
    /// Args to resume; empty: cannot resume. "{id}" is replaced.
    pub resume: Vec<String>,
    /// Alternatives meaning "session not found" on stderr. An alternative matches when all its parts appear
    /// in order, ASCII case-insensitive.
    pub missing: Vec<Vec<String>>,
}

impl Session {
    pub fn is_missing(&self, stderr: &str) -> bool {
        let hay = stderr.to_ascii_lowercase();
        self.missing
            .iter()
            .any(|parts| !parts.is_empty() && parts_appear_in_order(&hay, parts))
    }
}

fn parts_appear_in_order(hay: &str, parts: &[String]) -> bool {
    // `to_ascii_lowercase` keeps byte offsets and char boundaries intact.
    let mut cursor = 0;
    for part in parts {
        let needle = part.to_ascii_lowercase();
        let Some(at) = hay[cursor..].find(&needle) else {
            return false;
        };
        cursor += at + needle.len();
    }
    true
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SessionOwner {
    Caller,
    Engine,
    None,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Wire {
    Text,
    JsonDoc,
    Jsonl,
    StreamJson,
    OmpRpc { handshake: Duration },
    PiRpc { handshake: Duration },
}

#[derive(Clone, Debug, PartialEq)]
pub struct Caps {
    pub report: ReportKind,
    /// Lines /huddle:setup prints for this entry.
    pub notes: Vec<String>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum ReportKind {
    /// JSON report checked, with up to two correction turns (omp, pi).
    Checked,
    /// JSON report read out of the final text, no correction (claude, opencode, zcode).
    Extracted,
    /// Stdout is the answer (every text-wire entry, every user entry).
    FreeText,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Brief {
    /// The task text; the runner appends the report contract.
    pub prompt: String,
    pub access: Access,
    pub effort: Option<String>,
    pub session: SessionRef,
}

#[derive(Clone, Debug, PartialEq)]
pub enum SessionRef {
    /// A new session; `key` is used only for caller-owned sessions and must be a UUID v4 (claude requires one).
    New { key: String },
    /// Resume a known id. Refused for entries with SessionOwner::None.
    Resume { id: String },
}

/// How a run ended. `forced` is "timeout" or "stopped".
#[derive(Clone, Debug, PartialEq)]
pub struct ExitInfo {
    pub code: Option<i32>,
    pub signal: Option<String>,
    pub stderr_tail: String,
    pub forced: Option<String>,
    pub session_missing: bool,
}

impl ExitInfo {
    /// A process that exited on its own with this code: no signal, empty tail, not forced.
    pub fn exited(code: i32) -> ExitInfo {
        ExitInfo {
            code: Some(code),
            signal: None,
            stderr_tail: String::new(),
            forced: None,
            session_missing: false,
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum RunEvent {
    Session(String),
    TextDelta(String),
    AssistantText(String),
    ToolUse {
        name: String,
        summary: String,
    },
    ToolResult {
        is_error: bool,
    },
    ApiRetry {
        attempt: u64,
        max_attempts: u64,
        status: Option<u16>,
    },
    Error {
        message: String,
        status: Option<u16>,
    },
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct RunStats {
    pub turns: u64,
    pub model: Option<String>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cache_read_tokens: u64,
    pub cache_write_tokens: u64,
    pub cost_usd: f64,
    pub duration_ms: u64,
    pub api_error_status: Option<u16>,
}

/// What a wire reader hands back at the end of a run.
#[derive(Clone, Debug, PartialEq)]
pub struct WireOutput {
    pub text: String,
    pub session_id: Option<String>,
    /// Why the run failed, in one line; None when it did not.
    pub failure: Option<String>,
    pub stats: RunStats,
}

/// A wire reader for a harness that runs once and exits: lines in, one output at the end.
pub trait OneShotReader {
    fn feed_line(&mut self, line: &str, events: &mut Vec<RunEvent>);
    fn finish(self: Box<Self>, exit: &ExitInfo) -> WireOutput;
    /// True once the reader wants the run stopped because its output is over budget.
    fn over_budget(&self) -> bool {
        false
    }
}

/// The first string value among an object's members, in the order the JSON wrote them. Reads `serde_json::Value` in
/// its stored key order (the workspace enables `preserve_order`).
pub fn first_string_value(value: Option<&serde_json::Value>) -> Option<String> {
    let members = value?.as_object()?;
    members
        .iter()
        .find_map(|(_, member)| member.as_str().map(str::to_string))
}
