//! The opencode JSONL reader: one JSON event per stdout line, folded to events and one report.

use crate::table::{ExitInfo, OneShotReader, RunEvent, RunStats, WireOutput, first_string_value};
use serde_json::{Map, Value};

/// Reader for opencode's wire. Every stdout line is one JSON event; blank, non-JSON and
/// non-object lines are ignored. The run is good only when a `step_finish` arrived and no
/// `error` event did; the last non-empty `text` part is the report.
pub struct JsonlReader {
    final_text: String,
    session_id: Option<String>,
    error_text: Option<String>,
    finished: bool,
    turns: u64,
    input_tokens: u64,
    output_tokens: u64,
    cache_read_tokens: u64,
    cache_write_tokens: u64,
    cost_usd: f64,
    started_at: Option<u64>,
    ended_at: Option<u64>,
}

impl JsonlReader {
    pub fn new() -> JsonlReader {
        JsonlReader {
            final_text: String::new(),
            session_id: None,
            error_text: None,
            finished: false,
            turns: 0,
            input_tokens: 0,
            output_tokens: 0,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            cost_usd: 0.0,
            started_at: None,
            ended_at: None,
        }
    }

    /// Epoch milliseconds when the field is a finite number, else nothing.
    fn stamp(event: &Map<String, Value>) -> Option<u64> {
        event
            .get("timestamp")
            .and_then(Value::as_f64)
            .filter(|stamp| stamp.is_finite())
            .map(|stamp| stamp as u64)
    }

    /// A JSON number as a count: non-numbers count 0, negatives floor at 0.
    fn count(value: Option<&Value>) -> u64 {
        value
            .and_then(Value::as_f64)
            .filter(|count| count.is_finite())
            .unwrap_or(0.0) as u64
    }

    fn fold_text(event: &Map<String, Value>, events: &mut Vec<RunEvent>) -> Option<String> {
        let text = event
            .get("part")?
            .get("text")?
            .as_str()
            .filter(|text| !text.is_empty())?;
        events.push(RunEvent::AssistantText(text.to_string()));
        Some(text.to_string())
    }

    fn fold_session(event: &Map<String, Value>, events: &mut Vec<RunEvent>) -> Option<String> {
        let id = event.get("sessionID")?.as_str()?;
        events.push(RunEvent::Session(id.to_string()));
        Some(id.to_string())
    }

    /// The error's nested data message, its name, then a fixed line. Every error event becomes a
    /// `RunEvent::Error`; only the first one also sets the failure.
    fn fold_error(event: &Map<String, Value>, events: &mut Vec<RunEvent>) -> String {
        let error = event.get("error").and_then(Value::as_object);
        let message = error
            .and_then(|error| error.get("data")?.get("message"))
            .and_then(Value::as_str)
            .or_else(|| {
                error
                    .and_then(|error| error.get("name"))
                    .and_then(Value::as_str)
            })
            .unwrap_or("opencode reported an error");
        events.push(RunEvent::Error {
            message: message.to_string(),
            status: None,
        });
        message.to_string()
    }
}

impl Default for JsonlReader {
    fn default() -> Self {
        Self::new()
    }
}

impl OneShotReader for JsonlReader {
    fn feed_line(&mut self, line: &str, events: &mut Vec<RunEvent>) {
        let Ok(value) = serde_json::from_str::<Value>(line.trim()) else {
            return;
        };
        let Some(event) = value.as_object() else {
            return;
        };
        if let Some(stamp) = Self::stamp(event) {
            self.started_at.get_or_insert(stamp);
            self.ended_at = Some(stamp);
        }
        match event.get("type").and_then(Value::as_str) {
            Some("text") => {
                if let Some(text) = Self::fold_text(event, events) {
                    self.final_text = text;
                }
            }
            Some("tool_use") => self.fold_tool_use(event, events),
            Some("error") => {
                let message = Self::fold_error(event, events);
                if self.error_text.is_none() {
                    self.error_text = Some(message);
                }
            }
            Some("step_finish") => self.tally_step_finish(event),
            Some("step_start") => {
                if let Some(id) = Self::fold_session(event, events) {
                    self.session_id = Some(id);
                }
            }
            _ => {}
        }
    }

    fn finish(self: Box<Self>, exit: &ExitInfo) -> WireOutput {
        let reader = *self;
        let failure = reader.failure(exit);
        let duration_ms = match (reader.started_at, reader.ended_at) {
            (Some(started), Some(ended)) => ended.saturating_sub(started),
            _ => 0,
        };
        WireOutput {
            text: reader.final_text,
            session_id: reader.session_id,
            failure,
            stats: RunStats {
                turns: reader.turns,
                input_tokens: reader.input_tokens,
                output_tokens: reader.output_tokens,
                cache_read_tokens: reader.cache_read_tokens,
                cache_write_tokens: reader.cache_write_tokens,
                cost_usd: reader.cost_usd,
                duration_ms,
                ..RunStats::default()
            },
        }
    }
}

impl JsonlReader {
    /// A forced stop is a failure that keeps the text; a reported error beats a missing finish;
    /// a run whose last step never finished failed however the process ended.
    fn failure(&self, exit: &ExitInfo) -> Option<String> {
        if let Some(forced) = &exit.forced {
            return Some(format!("opencode was stopped ({forced})"));
        }
        if let Some(text) = &self.error_text {
            return Some(text.clone());
        }
        if !self.finished {
            let how = match (exit.code.as_ref(), exit.signal.as_ref()) {
                (_, Some(signal)) => signal.clone(),
                (Some(code), _) => format!("exit {code}"),
                (None, None) => "unknown".to_string(),
            };
            return Some(format!("opencode exited without a finished step ({how})"));
        }
        None
    }

    /// A tool part folds to its use and, once the state is done, its result. The summary is the
    /// state's title, else the first string in the input object's own key order, else empty.
    fn fold_tool_use(&mut self, event: &Map<String, Value>, events: &mut Vec<RunEvent>) {
        let Some(part) = event.get("part").and_then(Value::as_object) else {
            return;
        };
        let Some(name) = part.get("tool").and_then(Value::as_str) else {
            return;
        };
        let state = part.get("state").and_then(Value::as_object);
        let summary = state
            .and_then(|state| state.get("title"))
            .and_then(Value::as_str)
            .map(str::to_string)
            .or_else(|| first_string_value(state.and_then(|state| state.get("input"))))
            .unwrap_or_default();
        events.push(RunEvent::ToolUse {
            name: name.to_string(),
            summary,
        });
        let status = state
            .and_then(|state| state.get("status"))
            .and_then(Value::as_str);
        if matches!(status, Some("completed") | Some("error")) {
            events.push(RunEvent::ToolResult {
                is_error: status == Some("error"),
            });
        }
    }

    /// A finished step: one turn, the step's token counts and cost added to the run.
    fn tally_step_finish(&mut self, event: &Map<String, Value>) {
        self.finished = true;
        self.turns += 1;
        let part = event.get("part").and_then(Value::as_object);
        let tokens = part
            .and_then(|part| part.get("tokens"))
            .and_then(Value::as_object);
        self.input_tokens += Self::count(tokens.and_then(|tokens| tokens.get("input")));
        self.output_tokens += Self::count(tokens.and_then(|tokens| tokens.get("output")));
        let cache = tokens
            .and_then(|tokens| tokens.get("cache"))
            .and_then(Value::as_object);
        self.cache_read_tokens += Self::count(cache.and_then(|cache| cache.get("read")));
        self.cache_write_tokens += Self::count(cache.and_then(|cache| cache.get("write")));
        self.cost_usd += part
            .and_then(|part| part.get("cost"))
            .and_then(Value::as_f64)
            .filter(|cost| cost.is_finite())
            .unwrap_or(0.0);
    }
}
