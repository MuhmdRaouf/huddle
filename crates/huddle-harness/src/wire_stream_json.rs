//! huddle-harness: wire_stream_json.

use crate::table::{ExitInfo, OneShotReader, RunEvent, RunStats, WireOutput, first_string_value};
use serde_json::{Map, Value};

/// Reader for `claude -p --output-format stream-json --verbose`: one JSON object per line, the
/// final `result` line as the report, `system/init` as the session. Partial-message streaming is
/// not requested, so `stream_event` lines never arrive; unknown line types are ignored, and a
/// line that is not a JSON object is skipped.
pub struct StreamJsonReader {
    session_id: Option<String>,
    model: Option<String>,
    result: Option<ResultLine>,
}

/// The final `result` line, kept as read; a later line replaces an earlier one.
struct ResultLine {
    is_error: bool,
    text: String,
    failure_text: String,
    stats: RunStats,
}

impl StreamJsonReader {
    pub fn new() -> StreamJsonReader {
        StreamJsonReader {
            session_id: None,
            model: None,
            result: None,
        }
    }

    fn feed_system(&mut self, fields: &Map<String, Value>, events: &mut Vec<RunEvent>) {
        match string_field(fields, "subtype") {
            Some("init") => {
                let Some(session_id) = string_field(fields, "session_id") else {
                    return;
                };
                if let Some(model) = string_field(fields, "model") {
                    self.model = Some(model.to_string());
                }
                self.session_id = Some(session_id.to_string());
                events.push(RunEvent::Session(session_id.to_string()));
            }
            Some("api_retry") => {
                let (Some(attempt), Some(max_retries)) =
                    (count(fields, "attempt"), count(fields, "max_retries"))
                else {
                    return;
                };
                let status = match fields.get("error_status") {
                    None | Some(Value::Null) => None,
                    Some(value) => match value.as_u64() {
                        Some(status) => u16::try_from(status).ok(),
                        // a status that is not a number fails the wire shape: the line is ignored
                        None => return,
                    },
                };
                events.push(RunEvent::ApiRetry {
                    attempt,
                    max_attempts: max_retries,
                    status,
                });
            }
            _ => {}
        }
    }

    fn feed_result(&mut self, fields: &Map<String, Value>) {
        let Some(is_error) = fields.get("is_error").and_then(Value::as_bool) else {
            return;
        };
        let text = string_field(fields, "result")
            .unwrap_or_default()
            .to_string();
        let failure_text = match (is_error, string_field(fields, "result")) {
            // an error result carries Claude Code's own error text; without one there is only
            // the fact that it failed
            (true, Some(text)) => text.to_string(),
            (true, None) => "claude reported an error".to_string(),
            (false, _) => String::new(),
        };
        let usage = fields.get("usage").and_then(Value::as_object);
        let usage_count = |key: &str| -> u64 {
            usage
                .and_then(|counts| count(counts, key))
                .unwrap_or_default()
        };
        let stats = RunStats {
            turns: count(fields, "num_turns").unwrap_or_default(),
            duration_ms: count(fields, "duration_ms").unwrap_or_default(),
            cost_usd: fields
                .get("total_cost_usd")
                .and_then(Value::as_f64)
                .unwrap_or_default(),
            input_tokens: usage_count("input_tokens"),
            output_tokens: usage_count("output_tokens"),
            cache_read_tokens: usage_count("cache_read_input_tokens"),
            cache_write_tokens: usage_count("cache_creation_input_tokens"),
            api_error_status: nullish_status(fields, "api_error_status"),
            model: None,
        };
        // the result line names the session again; it wins over the init id
        if let Some(session_id) = string_field(fields, "session_id") {
            self.session_id = Some(session_id.to_string());
        }
        self.result = Some(ResultLine {
            is_error,
            text,
            failure_text,
            stats,
        });
    }
}

impl Default for StreamJsonReader {
    fn default() -> StreamJsonReader {
        StreamJsonReader::new()
    }
}

impl OneShotReader for StreamJsonReader {
    fn feed_line(&mut self, line: &str, events: &mut Vec<RunEvent>) {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            return;
        }
        let Ok(Value::Object(fields)) = serde_json::from_str(trimmed) else {
            return;
        };
        match string_field(&fields, "type") {
            Some("system") => self.feed_system(&fields, events),
            Some("assistant") => feed_assistant(&fields, events),
            Some("user") => feed_user(&fields, events),
            Some("result") => self.feed_result(&fields),
            _ => {}
        }
    }

    fn finish(self: Box<Self>, exit: &ExitInfo) -> WireOutput {
        let StreamJsonReader {
            session_id,
            model,
            result,
        } = *self;
        let mut stats = match &result {
            Some(result) => result.stats.clone(),
            None => RunStats::default(),
        };
        stats.model = model;
        let failure = if let Some(forced) = &exit.forced {
            Some(format!("claude was stopped ({forced})"))
        } else {
            match &result {
                None => {
                    let how = match exit.code {
                        Some(code) => format!("exit {code}"),
                        None => exit.signal.clone().unwrap_or_else(|| "unknown".to_string()),
                    };
                    Some(format!("claude ended without a result ({how})"))
                }
                Some(result) if result.is_error => Some(result.failure_text.clone()),
                Some(_) => None,
            }
        };
        WireOutput {
            text: result.map(|result| result.text).unwrap_or_default(),
            session_id,
            failure,
            stats,
        }
    }
}

fn feed_assistant(fields: &Map<String, Value>, events: &mut Vec<RunEvent>) {
    let Some(blocks) = content_blocks(fields) else {
        return;
    };
    let mut text = String::new();
    for block in blocks {
        let Some(block) = block.as_object() else {
            continue;
        };
        match block.get("type").and_then(Value::as_str) {
            Some("text") => {
                if let Some(piece) = block.get("text").and_then(Value::as_str) {
                    // adjacent text blocks are one piece of prose split by the API
                    text.push_str(piece);
                }
            }
            Some("tool_use") => {
                let Some(name) = string_field(block, "name") else {
                    continue;
                };
                flush_text(&mut text, events);
                let summary = first_string_value(block.get("input")).unwrap_or_default();
                events.push(RunEvent::ToolUse {
                    name: name.to_string(),
                    summary,
                });
            }
            _ => {}
        }
    }
    flush_text(&mut text, events);
}

fn feed_user(fields: &Map<String, Value>, events: &mut Vec<RunEvent>) {
    let Some(blocks) = content_blocks(fields) else {
        return;
    };
    let mut found = false;
    let mut is_error = false;
    for block in blocks {
        let Some(block) = block.as_object() else {
            continue;
        };
        if block.get("type").and_then(Value::as_str) != Some("tool_result") {
            continue;
        }
        found = true;
        if matches!(block.get("is_error"), Some(Value::Bool(true))) {
            is_error = true;
        }
    }
    if found {
        events.push(RunEvent::ToolResult { is_error });
    }
}

fn flush_text(text: &mut String, events: &mut Vec<RunEvent>) {
    if !text.is_empty() {
        events.push(RunEvent::AssistantText(std::mem::take(text)));
    }
}

fn content_blocks(fields: &Map<String, Value>) -> Option<&Vec<Value>> {
    fields
        .get("message")
        .and_then(Value::as_object)
        .and_then(|message| message.get("content"))
        .and_then(Value::as_array)
}

fn string_field<'a>(fields: &'a Map<String, Value>, key: &str) -> Option<&'a str> {
    fields.get(key).and_then(Value::as_str)
}

/// A count the wire schema types as a number: anything but an integer reads as absent, so a
/// missing or mistyped value never costs the line its other fields.
fn count(fields: &Map<String, Value>, key: &str) -> Option<u64> {
    fields.get(key).and_then(Value::as_u64)
}

fn nullish_status(fields: &Map<String, Value>, key: &str) -> Option<u16> {
    match fields.get(key) {
        None | Some(Value::Null) => None,
        Some(value) => value.as_u64().and_then(|status| u16::try_from(status).ok()),
    }
}
