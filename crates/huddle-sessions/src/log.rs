//! Parses the text of a session log into a `Summary`. Only the fields named in `Line` are
//! decoded. Message content is read for tool-call ids, tool names and task ids only; text,
//! tool input and tool output are never kept in the `Summary`.

use serde::Deserialize;
use serde::de::IgnoredAny;

use crate::model::{RequestTag, SharedStr, Usage};
use crate::time::parse_iso_ms;

#[derive(Deserialize)]
struct Line {
    #[serde(rename = "type")]
    kind: Option<String>,
    timestamp: Option<String>,
    cwd: Option<String>,
    #[serde(rename = "gitBranch")]
    git_branch: Option<String>,
    #[serde(rename = "requestId")]
    request_id: Option<String>,
    #[serde(rename = "requestedModel")]
    requested_model: Option<String>,
    #[serde(rename = "customTitle")]
    custom_title: Option<String>,
    #[serde(rename = "aiTitle")]
    ai_title: Option<String>,
    #[serde(rename = "interruptedMessageId")]
    interrupted_message_id: Option<String>,
    entrypoint: Option<String>,
    #[serde(rename = "promptSource")]
    prompt_source: Option<String>,
    origin: Option<Origin>,
    #[serde(rename = "toolUseResult")]
    tool_use_result: Option<ToolUseResult>,
    message: Option<Message>,
}

#[derive(Deserialize)]
struct Origin {
    kind: Option<String>,
}

/// `toolUseResult` is an object for launches and a plain string for some errors.
#[derive(Deserialize)]
#[serde(untagged)]
enum ToolUseResult {
    Fields(ToolFields),
    Other(IgnoredAny),
}

#[derive(Deserialize)]
struct ToolFields {
    #[serde(rename = "backgroundTaskId")]
    background_task_id: Option<String>,
    #[serde(rename = "agentId")]
    agent_id: Option<String>,
    status: Option<String>,
}

/// A message's content is a string or a list of blocks.
#[derive(Deserialize)]
#[serde(untagged)]
enum Content {
    Text(String),
    Blocks(Vec<Block>),
}

#[derive(Deserialize)]
struct Block {
    #[serde(rename = "type")]
    kind: Option<String>,
    id: Option<String>,
    name: Option<String>,
    tool_use_id: Option<String>,
    input: Option<ToolInput>,
    text: Option<String>,
}

#[derive(Deserialize)]
struct ToolInput {
    run_in_background: Option<bool>,
}

#[derive(Deserialize)]
struct Message {
    model: Option<String>,
    stop_reason: Option<String>,
    usage: Option<RawUsage>,
    content: Option<Content>,
}

#[derive(Deserialize)]
struct RawUsage {
    input_tokens: Option<u64>,
    cache_creation_input_tokens: Option<u64>,
    cache_read_input_tokens: Option<u64>,
    output_tokens: Option<u64>,
    cache_creation: Option<CacheCreation>,
}

#[derive(Deserialize)]
struct CacheCreation {
    ephemeral_1h_input_tokens: Option<u64>,
}

/// Where a turn stands after its last event.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Phase {
    /// A request was sent and no answer has been written yet.
    Asked,
    /// The model stopped to run a tool, or is still streaming.
    Tool,
    /// The model ended the turn, or the user interrupted it.
    Ended,
}

/// The last turn event of a log and when it was written.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Turn {
    pub at_ms: Option<u64>,
    pub phase: Phase,
}

#[derive(Debug, Default)]
pub(crate) struct Summary {
    pub first_ms: Option<u64>,
    pub last_ms: Option<u64>,
    pub cwd: Option<String>,
    pub branch: Option<String>,
    pub custom_title: Option<String>,
    pub ai_title: Option<String>,
    pub model: Option<String>,
    pub requested_model: Option<String>,
    pub last_turn: Option<Turn>,
    pub last_assistant_ms: Option<u64>,
    pub last_usage: Option<Usage>,
    pub one_hour_cache: bool,
    /// One entry per request id, holding the last usage and stop reason seen for it, in order
    /// of first sight. The strings are shared with every tag made from them.
    pub requests: Vec<(SharedStr, Usage, Option<SharedStr>)>,
    /// A line carried a driven marker: `entrypoint` `sdk-*` or `promptSource` `sdk`.
    pub driven: bool,
    /// Questions asked and not yet answered: tool-call id, tool name, when.
    pub questions: Vec<Question>,
    /// Background tasks launched and not yet reported finished.
    pub tasks: Vec<Task>,
    /// Ids of background `Bash` calls whose result has not arrived.
    launching: Vec<String>,
    /// One string per distinct stop reason the requests carry.
    stops: Vec<SharedStr>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Question {
    pub call_id: String,
    pub tool: String,
    pub at_ms: Option<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Task {
    pub id: String,
    pub at_ms: Option<u64>,
}

const QUESTION_TOOLS: [&str; 2] = ["AskUserQuestion", "ExitPlanMode"];
/// Stop reasons past this many distinct ones are not shared.
const MAX_STOPS: usize = 8;

impl Summary {
    /// Reads every line; a line that is not a JSON object of the expected shape is skipped.
    #[cfg(test)]
    pub(crate) fn parse(text: &str) -> Summary {
        let mut summary = Summary::default();
        for line in text.lines() {
            summary.feed(line);
        }
        summary
    }

    /// Reads one line; a line that is not a JSON object of the expected shape is skipped.
    pub(crate) fn feed(&mut self, line: &str) {
        if let Ok(line) = serde_json::from_str::<Line>(line) {
            self.add(line);
        }
    }

    fn add(&mut self, mut line: Line) {
        let at_ms = line.timestamp.as_deref().and_then(parse_iso_ms);
        if let Some(at) = at_ms {
            self.first_ms.get_or_insert(at);
            self.last_ms = Some(at);
        }
        replace_some(&mut self.cwd, line.cwd);
        replace_some(&mut self.branch, line.git_branch);
        replace_some(&mut self.custom_title, line.custom_title);
        replace_some(&mut self.ai_title, line.ai_title);
        if line
            .entrypoint
            .as_deref()
            .is_some_and(|e| e.starts_with("sdk"))
            || line.prompt_source.as_deref() == Some("sdk")
        {
            self.driven = true;
        }
        match line.kind.as_deref() {
            Some("assistant") => {
                let blocks = line.message.as_mut().and_then(|m| m.content.take());
                self.add_blocks(at_ms, blocks);
                self.add_assistant(at_ms, line.request_id, line.requested_model, line.message)
            }
            Some("api-request") => self.turn(at_ms, Phase::Asked),
            Some("user") => {
                let notified = line
                    .origin
                    .as_ref()
                    .is_some_and(|o| o.kind.as_deref() == Some("task-notification"));
                let blocks = line.message.and_then(|m| m.content);
                self.add_user(at_ms, notified, blocks, line.tool_use_result);
                if line.interrupted_message_id.is_some() {
                    self.turn(at_ms, Phase::Ended);
                }
            }
            _ => {}
        }
    }

    /// Tool calls of an assistant line: questions that wait for an answer, and background
    /// `Bash` calls whose task id arrives with the result.
    fn add_blocks(&mut self, at_ms: Option<u64>, content: Option<Content>) {
        let Some(Content::Blocks(blocks)) = content else {
            return;
        };
        for block in blocks {
            if block.kind.as_deref() != Some("tool_use") {
                continue;
            }
            let (Some(id), Some(name)) = (block.id, block.name) else {
                continue;
            };
            if QUESTION_TOOLS.contains(&name.as_str()) {
                self.questions.push(Question {
                    call_id: id,
                    tool: name,
                    at_ms,
                });
            } else if name == "Bash" && block.input.and_then(|i| i.run_in_background) == Some(true)
            {
                self.launching.push(id);
            }
        }
    }

    /// Tool results answer questions and launch tasks; a task notification closes one.
    fn add_user(
        &mut self,
        at_ms: Option<u64>,
        notified: bool,
        content: Option<Content>,
        result: Option<ToolUseResult>,
    ) {
        let fields = match result {
            Some(ToolUseResult::Fields(fields)) => Some(fields),
            _ => None,
        };
        match content {
            Some(Content::Blocks(blocks)) => {
                for block in blocks {
                    if block.kind.as_deref() == Some("tool_result")
                        && let Some(call) = block.tool_use_id
                    {
                        self.answer(&call, at_ms, fields.as_ref());
                    } else if notified && let Some(text) = block.text {
                        self.finish_task(&text);
                    }
                }
            }
            Some(Content::Text(text)) if notified => self.finish_task(&text),
            _ => {}
        }
    }

    fn answer(&mut self, call: &str, at_ms: Option<u64>, fields: Option<&ToolFields>) {
        self.questions.retain(|q| q.call_id != call);
        let Some(fields) = fields else { return };
        let launched = match (&fields.background_task_id, &fields.agent_id) {
            (Some(task), _) if self.launching.iter().any(|id| id == call) => Some(task),
            (_, Some(agent)) if fields.status.as_deref() == Some("async_launched") => Some(agent),
            _ => None,
        };
        self.launching.retain(|id| id != call);
        if let Some(id) = launched {
            self.tasks.push(Task {
                id: id.clone(),
                at_ms,
            });
        }
    }

    /// Drops the task named by the `<task-id>` tag of a notification. Only the id is read.
    fn finish_task(&mut self, text: &str) {
        let Some((_, rest)) = text.split_once("<task-id>") else {
            return;
        };
        let Some((id, _)) = rest.split_once("</task-id>") else {
            return;
        };
        self.tasks.retain(|task| task.id != id);
    }

    fn add_assistant(
        &mut self,
        at_ms: Option<u64>,
        request_id: Option<String>,
        requested_model: Option<String>,
        message: Option<Message>,
    ) {
        let Some(message) = message else { return };
        if message.model.as_deref() == Some("<synthetic>") {
            return;
        }
        replace_some(&mut self.model, message.model);
        replace_some(&mut self.requested_model, requested_model);
        self.last_assistant_ms = at_ms;
        let stop = message.stop_reason;
        let phase = match stop.as_deref() {
            Some("end_turn" | "stop_sequence" | "max_tokens" | "refusal") => Phase::Ended,
            _ => Phase::Tool,
        };
        self.turn(at_ms, phase);
        let Some(raw) = message.usage else { return };
        let usage = Usage {
            input: raw.input_tokens.unwrap_or(0),
            cache_read: raw.cache_read_input_tokens.unwrap_or(0),
            cache_write: raw.cache_creation_input_tokens.unwrap_or(0),
            output: raw.output_tokens.unwrap_or(0),
        };
        self.last_usage = Some(usage);
        self.one_hour_cache = raw
            .cache_creation
            .and_then(|c| c.ephemeral_1h_input_tokens)
            .is_some_and(|tokens| tokens > 0);
        if let Some(id) = request_id {
            let stop = stop.map(|stop| self.stop_reason(stop));
            match self
                .requests
                .iter_mut()
                .find(|(seen, _, _)| *seen == id.as_str())
            {
                Some(entry) => {
                    entry.1 = usage;
                    if stop.is_some() {
                        entry.2 = stop;
                    }
                }
                None => self.requests.push((SharedStr::from(id), usage, stop)),
            }
        }
    }

    /// The shared string for a stop reason: a log names only a few, thousands of times.
    fn stop_reason(&mut self, stop: String) -> SharedStr {
        if let Some(known) = self.stops.iter().find(|known| **known == *stop) {
            return known.clone();
        }
        let stop = SharedStr::from(stop);
        if self.stops.len() < MAX_STOPS {
            self.stops.push(stop.clone());
        }
        stop
    }

    fn turn(&mut self, at_ms: Option<u64>, phase: Phase) {
        self.last_turn = Some(Turn { at_ms, phase });
    }

    /// The request tags of this log's agent; each shares its strings with the summary.
    pub(crate) fn tags<'a>(
        &'a self,
        agent_id: &'a SharedStr,
    ) -> impl ExactSizeIterator<Item = RequestTag> + 'a {
        self.requests
            .iter()
            .map(|(request_id, usage, stop)| RequestTag {
                request_id: request_id.clone(),
                agent_id: agent_id.clone(),
                usage: *usage,
                stop: stop.clone(),
            })
    }
}

fn replace_some(slot: &mut Option<String>, value: Option<String>) {
    if value.is_some() {
        *slot = value;
    }
}
