//! Parses the text of a session log into a `Summary`. Only the fields named in `Line` are
//! decoded; message content, tool input and tool output are skipped by the deserializer and
//! never held in memory.

use serde::Deserialize;

use crate::model::{RequestTag, Usage};
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
    message: Option<Message>,
}

#[derive(Deserialize)]
struct Message {
    model: Option<String>,
    stop_reason: Option<String>,
    usage: Option<RawUsage>,
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

/// The last assistant line: when it was written and whether the turn ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct AssistantEnd {
    pub at_ms: Option<u64>,
    pub end_turn: bool,
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
    pub last_assistant: Option<AssistantEnd>,
    pub last_usage: Option<Usage>,
    pub one_hour_cache: bool,
    /// One entry per request id, holding the last usage seen for it, in order of first sight.
    pub requests: Vec<(String, Usage)>,
}

impl Summary {
    /// Reads every line; a line that is not a JSON object of the expected shape is skipped.
    pub(crate) fn parse(text: &str) -> Summary {
        let mut summary = Summary::default();
        for line in text.lines() {
            if let Ok(line) = serde_json::from_str::<Line>(line) {
                summary.add(line);
            }
        }
        summary
    }

    fn add(&mut self, line: Line) {
        let at_ms = line.timestamp.as_deref().and_then(parse_iso_ms);
        if let Some(at) = at_ms {
            self.first_ms.get_or_insert(at);
            self.last_ms = Some(at);
        }
        replace_some(&mut self.cwd, line.cwd);
        replace_some(&mut self.branch, line.git_branch);
        replace_some(&mut self.custom_title, line.custom_title);
        replace_some(&mut self.ai_title, line.ai_title);
        if line.kind.as_deref() == Some("assistant") {
            self.add_assistant(at_ms, line.request_id, line.requested_model, line.message);
        }
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
        self.last_assistant = Some(AssistantEnd {
            at_ms,
            end_turn: message.stop_reason.as_deref() == Some("end_turn"),
        });
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
            match self.requests.iter_mut().find(|(seen, _)| *seen == id) {
                Some(entry) => entry.1 = usage,
                None => self.requests.push((id, usage)),
            }
        }
    }

    pub(crate) fn tags(&self, agent_id: &str) -> Vec<RequestTag> {
        self.requests
            .iter()
            .map(|(request_id, usage)| RequestTag {
                request_id: request_id.clone(),
                agent_id: agent_id.to_string(),
                usage: *usage,
            })
            .collect()
    }
}

fn replace_some(slot: &mut Option<String>, value: Option<String>) {
    if value.is_some() {
        *slot = value;
    }
}
