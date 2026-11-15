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
/// Only `message_start` and `message_delta` are parsed in full; every other event is skipped
/// key by key, allocating nothing.
fn take(line: &[u8]) -> (Option<Usage>, bool) {
    let Some(payload) = line.strip_prefix(b"data:") else {
        return (None, false);
    };
    let Some(kind) = kind_of(payload) else {
        return (None, false);
    };
    match kind {
        EventKind::ContentBlockDelta => (None, true),
        EventKind::MessageStart | EventKind::MessageDelta => (usage_of_event(payload, kind), false),
        EventKind::Other => (None, false),
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum EventKind {
    MessageStart,
    MessageDelta,
    ContentBlockDelta,
    Other,
}

/// Reads the `type` of an event object: the keys it does not need are skipped without
/// allocation, and a truncated object keeps the type it read before the cut.
fn kind_of(payload: &[u8]) -> Option<EventKind> {
    use serde::de::{Deserializer as _, IgnoredAny, MapAccess, Visitor};

    /// A map key, read borrowed off the payload: true when the key is exactly `type`.
    struct IsType(bool);

    impl<'de> serde::Deserialize<'de> for IsType {
        fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
            struct Key;

            impl<'de> Visitor<'de> for Key {
                type Value = bool;

                fn expecting(&self, formatter: &mut std::fmt::Formatter) -> std::fmt::Result {
                    formatter.write_str("a key")
                }

                fn visit_borrowed_str<E: serde::de::Error>(
                    self,
                    value: &'de str,
                ) -> Result<bool, E> {
                    self.visit_str(value)
                }

                fn visit_str<E: serde::de::Error>(self, value: &str) -> Result<bool, E> {
                    Ok(value == "type")
                }
            }

            deserializer.deserialize_str(Key).map(IsType)
        }
    }

    /// An event type name, read borrowed off the payload.
    struct KindName(EventKind);

    impl<'de> serde::Deserialize<'de> for KindName {
        fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
            struct Name;

            impl<'de> Visitor<'de> for Name {
                type Value = EventKind;

                fn expecting(&self, formatter: &mut std::fmt::Formatter) -> std::fmt::Result {
                    formatter.write_str("an event type name")
                }

                fn visit_borrowed_str<E: serde::de::Error>(
                    self,
                    value: &'de str,
                ) -> Result<EventKind, E> {
                    self.visit_str(value)
                }

                fn visit_str<E: serde::de::Error>(self, value: &str) -> Result<EventKind, E> {
                    Ok(match value {
                        "message_start" => EventKind::MessageStart,
                        "message_delta" => EventKind::MessageDelta,
                        "content_block_delta" => EventKind::ContentBlockDelta,
                        _ => EventKind::Other,
                    })
                }
            }

            deserializer.deserialize_str(Name).map(KindName)
        }
    }

    struct TypeOf<'a>(&'a mut Option<EventKind>);

    impl<'de> Visitor<'de> for TypeOf<'_> {
        type Value = ();

        fn expecting(&self, formatter: &mut std::fmt::Formatter) -> std::fmt::Result {
            formatter.write_str("an event object")
        }

        fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<(), A::Error> {
            // The map is drained to its end: an early return would leave serde_json building
            // an error for the unread rest, one allocation a frame.
            while let Some(IsType(is_type)) = map.next_key::<IsType>()? {
                if is_type && self.0.is_none() {
                    *self.0 = Some(map.next_value::<KindName>()?.0);
                } else {
                    map.next_value::<IgnoredAny>()?;
                }
            }
            Ok(())
        }
    }

    let mut kind = None;
    let mut reader = serde_json::Deserializer::from_slice(payload);
    // A payload that is no object leaves the type unset; a truncated object keeps the type
    // it read before the cut.
    let _ = (&mut reader).deserialize_map(TypeOf(&mut kind));
    kind
}

/// The usage of a `message_start` or `message_delta` event, parsed in full.
fn usage_of_event(payload: &[u8], kind: EventKind) -> Option<Usage> {
    let event: serde_json::Value = serde_json::from_slice(payload).ok()?;
    let object = event.as_object()?;
    match kind {
        EventKind::MessageStart => object
            .get("message")
            .and_then(|message| message.as_object())
            .and_then(|message| message.get("usage"))
            .and_then(|usage| usage.as_object())
            .map(usage_from),
        EventKind::MessageDelta => {
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
    }
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
    fn l8_delta_events_are_not_parsed_into_a_value() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(
            b"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n",
        );
        // A delta whose body is not valid JSON: only a reader that stops at `type` sees it as one.
        for _ in 0..1000 {
            tracker.feed(b"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"x\n\n");
        }
        tracker.feed(b"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n");
        assert!(tracker.first_token_seen());
        assert_eq!(
            tracker.usage(),
            Some(&Usage {
                input_tokens: Some(7),
                output_tokens: Some(9),
                stop_reason: Some("end_turn".into()),
                ..Usage::default()
            })
        );
    }

    #[test]
    fn l8_usage_and_stop_reason_match_the_old_parse_on_the_fixtures() {
        let mut tracker = SseUsageTracker::new();
        let stream = concat!(
            "event: message_start\n",
            "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":3,\"output_tokens\":1}}}\n\n",
            "data: {\"message\":{},\"type\":\"ping\"}\n\n",
            "data: {\"index\":0,\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
            "data: {\"delta\":{\"stop_reason\":\"tool_use\"},\"type\":\"message_delta\",\"usage\":{\"output_tokens\":78}}\n\n",
            "data: {\"type\":\"message_stop\"}\n\n",
        );
        tracker.feed(stream.as_bytes());
        assert!(tracker.first_token_seen());
        assert_eq!(
            tracker.usage(),
            Some(&Usage {
                input_tokens: Some(12),
                output_tokens: Some(78),
                cache_read_input_tokens: Some(3),
                stop_reason: Some("tool_use".into()),
                ..Usage::default()
            })
        );
    }

    fn expected_usage() -> Usage {
        Usage {
            input_tokens: Some(12),
            output_tokens: Some(78),
            stop_reason: Some("end_turn".into()),
            ..Usage::default()
        }
    }

    #[test]
    fn l8_crlf_line_endings_still_count() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(b"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\r\n\r\n");
        tracker.feed(b"data: {\"type\":\"content_block_delta\"}\r\n\r\n");
        tracker.feed(b"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":78}}\r\n\r\n");
        assert!(tracker.first_token_seen());
        assert_eq!(tracker.usage(), Some(&expected_usage()));
    }

    #[test]
    fn l8_an_escaped_type_value_or_key_still_counts() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(b"data: {\"type\":\"message\\u005fstart\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\n");
        tracker.feed(b"data: {\"\\u0074ype\":\"content_block_delta\"}\n\n");
        tracker.feed(b"data: {\"a\\\"b\":1,\"\\u0074ype\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":78}}\n\n");
        assert!(tracker.first_token_seen());
        assert_eq!(tracker.usage(), Some(&expected_usage()));
    }

    #[test]
    fn l8_an_error_event_mid_stream_neither_fails_nor_adds_usage() {
        let mut tracker = SseUsageTracker::new();
        tracker.feed(
            b"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12}}}\n\n",
        );
        tracker.feed(b"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"},\"usage\":{\"output_tokens\":999}}\n\n");
        assert_eq!(tracker.usage().expect("usage").output_tokens, None);
        tracker.feed(b"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":78}}\n\n");
        assert_eq!(tracker.usage(), Some(&expected_usage()));
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
