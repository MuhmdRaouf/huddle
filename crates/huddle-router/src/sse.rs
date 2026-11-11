//! huddle-router: sse.

/// The most bytes a tracker holds before it gives up reading usage.
pub const USAGE_BUFFER: usize = 64 * 1024;

/// Token counts as the event records them. Later keys win.
#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize)]
pub struct Usage {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub input_tokens: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub output_tokens: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_creation_input_tokens: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_read_input_tokens: Option<i64>,
    /// Why the answer stopped; the dashboard's, never part of a route event.
    #[serde(skip)]
    pub stop_reason: Option<String>,
}

/// Reads usage off a passing stream. `feed` never delays or changes the caller's bytes.
#[derive(Debug, Default)]
pub struct SseUsageTracker {
    buffer: Vec<u8>,
    usage: Option<Usage>,
    first_token: bool,
    stopped: bool,
}

impl SseUsageTracker {
    pub fn new() -> SseUsageTracker {
        SseUsageTracker::default()
    }

    pub fn feed(&mut self, chunk: &[u8]) {
        if self.stopped {
            return;
        }
        self.buffer.extend_from_slice(chunk);
        let mut first_token = self.first_token;
        let mut usage = self.usage.take();
        let mut start = 0;
        while let Some(at) = self.buffer[start..].iter().position(|&b| b == b'\n') {
            let end = start + at;
            let (found, token) = take(&self.buffer[start..end]);
            first_token |= token;
            merge_into(&mut usage, found);
            start = end + 1;
        }
        self.first_token = first_token;
        self.usage = usage;
        self.buffer.drain(..start);
        if self.buffer.len() > USAGE_BUFFER {
            self.buffer.clear();
            self.stopped = true;
        }
    }

    pub fn usage(&self) -> Option<&Usage> {
        self.usage.as_ref()
    }

    pub fn first_token_seen(&self) -> bool {
        self.first_token
    }
}

/// Buffers a JSON answer up to 64 KiB and reads its top-level `usage` at the end.
#[derive(Debug, Default)]
pub struct JsonUsageTracker {
    buffer: Vec<u8>,
    total: usize,
    over: bool,
}

impl JsonUsageTracker {
    pub fn new() -> JsonUsageTracker {
        JsonUsageTracker::default()
    }

    pub fn feed(&mut self, chunk: &[u8]) {
        if self.over {
            return;
        }
        self.total += chunk.len();
        if self.total > USAGE_BUFFER {
            self.over = true;
            self.buffer.clear();
            return;
        }
        self.buffer.extend_from_slice(chunk);
    }

    pub fn finish(self) -> Option<Usage> {
        if self.over {
            return None;
        }
        let parsed: serde_json::Value = serde_json::from_slice(&self.buffer).ok()?;
        let object = parsed.as_object()?;
        let mut usage = usage_from(object.get("usage")?.as_object()?);
        usage.stop_reason = stop_reason_of(object);
        Some(usage)
    }
}

/// Reads one `data:` line: the event's usage object, and whether it marks the first token.
fn take(line: &[u8]) -> (Option<Usage>, bool) {
    let Some(payload) = line.strip_prefix(b"data:") else {
        return (None, false);
    };
    let Ok(event) = serde_json::from_slice::<serde_json::Value>(payload) else {
        return (None, false);
    };
    let Some(object) = event.as_object() else {
        return (None, false);
    };
    let kind = object.get("type").and_then(|value| value.as_str());
    let first_token = kind == Some("content_block_delta");
    let found = match kind {
        Some("message_start") => object
            .get("message")
            .and_then(|message| message.as_object())
            .and_then(|message| message.get("usage"))
            .and_then(|usage| usage.as_object())
            .map(usage_from),
        Some("message_delta") => {
            object
                .get("usage")
                .and_then(|usage| usage.as_object())
                .map(|usage| Usage {
                    stop_reason: object
                        .get("delta")
                        .and_then(|delta| delta.as_object())
                        .and_then(stop_reason_of),
                    ..usage_from(usage)
                })
        }
        _ => None,
    };
    (found, first_token)
}

/// Counts a key only when it is a JSON integer.
fn integer_of(object: &serde_json::Map<String, serde_json::Value>, key: &str) -> Option<i64> {
    object.get(key).and_then(|value| value.as_i64())
}

/// The `stop_reason` string of a message or a delta; null and other types count as absent.
fn stop_reason_of(object: &serde_json::Map<String, serde_json::Value>) -> Option<String> {
    object
        .get("stop_reason")
        .and_then(|value| value.as_str())
        .map(str::to_owned)
}

fn usage_from(object: &serde_json::Map<String, serde_json::Value>) -> Usage {
    Usage {
        input_tokens: integer_of(object, "input_tokens"),
        output_tokens: integer_of(object, "output_tokens"),
        cache_creation_input_tokens: integer_of(object, "cache_creation_input_tokens"),
        cache_read_input_tokens: integer_of(object, "cache_read_input_tokens"),
        stop_reason: None,
    }
}

/// Folds a found usage into the tracker's: per key, the later one wins.
fn merge_into(usage: &mut Option<Usage>, found: Option<Usage>) {
    let Some(found) = found else {
        return;
    };
    let usage = usage.get_or_insert_with(Usage::default);
    if found.input_tokens.is_some() {
        usage.input_tokens = found.input_tokens;
    }
    if found.output_tokens.is_some() {
        usage.output_tokens = found.output_tokens;
    }
    if found.cache_creation_input_tokens.is_some() {
        usage.cache_creation_input_tokens = found.cache_creation_input_tokens;
    }
    if found.cache_read_input_tokens.is_some() {
        usage.cache_read_input_tokens = found.cache_read_input_tokens;
    }
    if found.stop_reason.is_some() {
        usage.stop_reason = found.stop_reason;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stream_usage_merges_message_start_and_delta() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(b"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n");
        tracker.feed(b"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":78}}\n\n");
        assert_eq!(
            tracker.usage(),
            Some(&Usage {
                input_tokens: Some(12),
                output_tokens: Some(78),
                ..Usage::default()
            })
        );
    }

    #[test]
    fn a_stream_with_no_usage_answers_none() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(b"data: {\"type\":\"ping\"}\n\n");
        assert_eq!(tracker.usage(), None);
    }

    #[test]
    fn a_stream_marks_its_first_token() {
        let mut tracker = SseUsageTracker::new();
        assert!(!tracker.first_token_seen());
        tracker.feed(b"data: {\"type\":\"content_block_delta\"}\n\n");
        assert!(tracker.first_token_seen());
    }

    #[test]
    fn a_line_split_across_chunks_still_counts() {
        let mut tracker = SseUsageTracker::new();
        let line = b"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n";
        tracker.feed(&line[..20]);
        tracker.feed(&line[20..]);
        assert_eq!(tracker.usage().expect("usage").output_tokens, Some(5));
    }

    #[test]
    fn a_partial_line_past_the_buffer_stops_the_tracker_but_never_fails() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(&vec![b'x'; USAGE_BUFFER + 10]);
        tracker.feed(b"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n");
        assert_eq!(tracker.usage(), None);
    }

    #[test]
    fn a_json_answer_reads_its_top_level_usage() {
        let mut tracker = JsonUsageTracker::new();
        tracker.feed(b"{\"id\":\"m\",\"usage\":{\"input_tokens\":3,");
        tracker.feed(b"\"output_tokens\":4}}");
        assert_eq!(
            tracker.finish(),
            Some(Usage {
                input_tokens: Some(3),
                output_tokens: Some(4),
                ..Usage::default()
            })
        );
    }

    #[test]
    fn a_json_answer_past_the_cap_or_without_usage_answers_none() {
        let mut big = JsonUsageTracker::new();
        big.feed(&vec![b' '; USAGE_BUFFER + 1]);
        assert_eq!(big.finish(), None);
        let mut none = JsonUsageTracker::new();
        none.feed(b"not json");
        assert_eq!(none.finish(), None);
        let mut array = JsonUsageTracker::new();
        array.feed(b"[1,2]");
        assert_eq!(array.finish(), None);
    }

    #[test]
    fn usage_serializes_only_the_keys_it_has() {
        let usage = Usage {
            input_tokens: Some(1),
            ..Usage::default()
        };
        assert_eq!(
            serde_json::to_string(&usage).expect("json"),
            "{\"input_tokens\":1}"
        );
    }

    #[test]
    fn a_stream_reads_the_stop_reason_off_message_delta() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(
            b"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n",
        );
        assert_eq!(tracker.usage().expect("usage").stop_reason, None);
        tracker.feed(b"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n\n");
        let usage = tracker.usage().expect("usage");
        assert_eq!(usage.stop_reason.as_deref(), Some("tool_use"));
        assert_eq!(usage.input_tokens, Some(1));
    }

    #[test]
    fn a_json_answer_reads_its_top_level_stop_reason() {
        let mut tracker = JsonUsageTracker::new();
        tracker.feed(b"{\"stop_reason\":\"end_turn\",\"usage\":{\"output_tokens\":4}}");
        assert_eq!(
            tracker.finish().expect("usage").stop_reason.as_deref(),
            Some("end_turn")
        );
    }

    #[test]
    fn the_stop_reason_stays_out_of_the_serialized_usage() {
        let usage = Usage {
            stop_reason: Some("end_turn".into()),
            ..Usage::default()
        };
        assert_eq!(serde_json::to_string(&usage).expect("json"), "{}");
    }
}
