//! huddle-harness: wire_jsondoc.
//!
//! The ZCode one-shot wire reader: stdout holds warning lines and then one final JSON document;
//! the document's `response` is the report text and `sessionId` the session.

use crate::table::{ExitInfo, OneShotReader, RunEvent, RunStats, WireOutput};
use serde_json::{Deserializer, Value};

/// The whole stdout a run may produce before the reader wants it stopped, in bytes.
pub const OUTPUT_BUDGET: u64 = 8_388_608;

/// Reads ZCode's stdout: warning lines and then one final JSON document, until the budget runs out.
pub struct JsonDocReader {
    output: String,
    bytes: u64,
    over: bool,
}

impl JsonDocReader {
    pub fn new() -> JsonDocReader {
        JsonDocReader {
            output: String::new(),
            bytes: 0,
            over: false,
        }
    }
}

impl Default for JsonDocReader {
    fn default() -> Self {
        Self::new()
    }
}

impl OneShotReader for JsonDocReader {
    fn feed_line(&mut self, line: &str, _events: &mut Vec<RunEvent>) {
        if self.over {
            return;
        }
        self.output.push_str(line);
        self.output.push('\n');
        self.bytes += (line.len() + 1) as u64;
        if self.bytes > OUTPUT_BUDGET {
            self.over = true;
        }
    }

    fn over_budget(&self) -> bool {
        self.over
    }

    fn finish(self: Box<Self>, exit: &ExitInfo) -> WireOutput {
        let reader = *self;
        let kept = if reader.over {
            Vec::new()
        } else {
            objects_from_line_starts(&reader.output)
        };
        let document = kept
            .iter()
            .find(|d| d.get("response").and_then(Value::as_str).is_some())
            .or_else(|| kept.first());
        let response = document
            .and_then(|d| d.get("response"))
            .and_then(Value::as_str)
            .map(str::to_string);
        let session = document
            .and_then(|d| d.get("sessionId"))
            .and_then(Value::as_str)
            .filter(|id| is_session_id(id))
            .map(str::to_string);
        let failure = if let Some(reason) = &exit.forced {
            Some(format!("ZCode was stopped ({reason})"))
        } else if exit.code != Some(0) {
            Some(format!(
                "ZCode exited {}: {}",
                exit_label(exit),
                exit.stderr_tail
            ))
        } else if response.is_none() || session.is_none() {
            Some("ZCode returned no valid JSON response and session id".to_string())
        } else if projection_failed(document) {
            Some("ZCode reported a failed turn".to_string())
        } else {
            None
        };
        let turns = u64::from(response.as_deref().is_some_and(|text| !text.is_empty()));
        WireOutput {
            text: response.unwrap_or_default(),
            session_id: if exit.forced.is_some() { None } else { session },
            failure,
            stats: RunStats {
                turns,
                input_tokens: usage_count(document, "inputTokens"),
                output_tokens: usage_count(document, "outputTokens"),
                cache_read_tokens: usage_count(document, "cacheReadTokens"),
                cache_write_tokens: usage_count(document, "cacheWriteTokens"),
                ..RunStats::default()
            },
        }
    }
}

/// Every JSON object on stdout that starts at a line start, left to right. A parse eats one whole
/// value and the scan resumes after it, so a warning line after the document cannot lose it.
fn objects_from_line_starts(output: &str) -> Vec<Value> {
    let bytes = output.as_bytes();
    let mut kept = Vec::new();
    let mut at = 0;
    while at < output.len() {
        if bytes[at] != b'{' || (at > 0 && bytes[at - 1] != b'\n') {
            at += 1;
            continue;
        }
        let mut stream = Deserializer::from_str(&output[at..]).into_iter::<Value>();
        match stream.next() {
            Some(Ok(value)) => {
                let end = at + stream.byte_offset();
                if value.is_object() {
                    kept.push(value);
                }
                at = end;
            }
            _ => at += 1,
        }
    }
    kept
}

/// How the run ended the way the failure message reads it: the signal, else the exit code, else
/// unknown when the run had neither.
fn exit_label(exit: &ExitInfo) -> String {
    if let Some(signal) = &exit.signal {
        return signal.clone();
    }
    match exit.code {
        Some(code) => code.to_string(),
        None => "unknown".to_string(),
    }
}

/// A ZCode session id: `sess_` and at least one more character, each ASCII alphanumeric or one of
/// `_`, `.`, `:`, `-`.
fn is_session_id(id: &str) -> bool {
    match id.strip_prefix("sess_") {
        Some(rest) => {
            !rest.is_empty()
                && rest
                    .chars()
                    .all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | ':' | '-'))
        }
        None => false,
    }
}

/// True when the document's projection says the turn failed.
fn projection_failed(document: Option<&Value>) -> bool {
    matches!(
        document
            .and_then(|d| d.get("projection"))
            .and_then(|p| p.get("status"))
            .and_then(Value::as_str),
        Some("error") | Some("failed")
    )
}

/// One count from the document's usage record; a missing or non-number value reads 0.
fn usage_count(document: Option<&Value>, key: &str) -> u64 {
    document
        .and_then(|d| d.get("usage"))
        .and_then(|u| u.get(key))
        .and_then(Value::as_u64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_session_id_rejects_every_wrong_shape() {
        assert!(is_session_id("sess_a"));
        assert!(is_session_id("sess_1.2:x-y_z"));
        assert!(!is_session_id("sess_"));
        assert!(!is_session_id("other_x"));
        assert!(!is_session_id("sess_é"));
    }
}
