//! omp's RPC reader: the frame decoder with v2 `rpc_chunk` reassembly, and the handshake that
//! negotiates protocol 2, sets the event filter and reads the session state.

use serde_json::{Map, Value};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use huddle_procs::stream::ProcExit;

use crate::rpc_common::{
    Decoded, Frame, FrameDecoder, Incoming, RpcConn, RpcError, RpcErrorKind, as_response, count_of,
    describe_exit, parse_frame, reported_model, text_of,
};
use crate::table::{Access, Entry, RunEvent, RunStats, WireOutput, first_string_value};

/// What omp refuses to reassemble beyond.
pub const MAX_REASSEMBLED_BYTES: u64 = 67_108_864;

/// omp's slice size for one `rpc_chunk`'s data.
pub const CHUNK_SLICE_BYTES: usize = 262_144;

/// The chunk threshold before the handshake agrees on one.
const DEFAULT_THRESHOLD: u64 = 1_048_576;

/// The largest integer a JS number holds exactly, the bound on every count the protocol carries.
const MAX_SAFE_INTEGER: f64 = 9_007_199_254_740_991.0;

/// omp's own decoder limits: chunk ids and chunk counts it refuses outright.
const MAX_CHUNK_ID_UNITS: usize = 128;
const MAX_CHUNKS: u64 = 256;

/// omp's decoder: one frame per line, reassembling `rpc_chunk` runs once protocol 2 is agreed.
pub struct OmpDecoder {
    sequence: Option<RpcSequence>,
    chunked: bool,
    threshold: u64,
}

impl Default for OmpDecoder {
    fn default() -> Self {
        Self::new()
    }
}

impl OmpDecoder {
    /// Chunking off, threshold 1 MiB.
    pub fn new() -> OmpDecoder {
        OmpDecoder {
            sequence: None,
            chunked: false,
            threshold: DEFAULT_THRESHOLD,
        }
    }

    /// Marks protocol 2 as agreed: `rpc_chunk` lines become legal, and `max_frame_bytes` (from
    /// the peer's ready frame) sets the size a frame must exceed to arrive chunked. A peer that
    /// does not send one keeps the default.
    pub fn negotiated(&mut self, max_frame_bytes: Option<f64>) {
        self.chunked = true;
        if let Some(value) = max_frame_bytes
            && value == value.trunc()
            && value.abs() <= MAX_SAFE_INTEGER
            && value > 0.0
        {
            self.threshold = value as u64;
        }
    }

    /// Folds one `rpc_chunk` line into the sequence under way; the last chunk returns the frame.
    fn chunk(&mut self, frame: Frame) -> Decoded {
        if !self.chunked {
            return self.fail("rpc chunk before protocol negotiation");
        }
        let meta = match self.chunk_meta(&frame) {
            Ok(meta) => meta,
            Err(reason) => return self.fail(reason),
        };
        if self.sequence.is_none() && meta.index != 0 {
            return self.fail("rpc chunk sequence must start at index 0");
        }
        let mut sequence = self.sequence.take().unwrap_or(RpcSequence {
            chunk_id: meta.chunk_id.clone(),
            count: meta.count,
            byte_length: meta.byte_length,
            chunks: Vec::new(),
            received: 0,
        });
        if sequence.chunk_id != meta.chunk_id
            || sequence.count != meta.count
            || sequence.byte_length != meta.byte_length
            || sequence.chunks.len() as u64 != meta.index
        {
            return self.fail("rpc chunk sequence mismatch");
        }
        let data_len = meta.data.len();
        sequence.chunks.push(meta.data);
        sequence.received += data_len as u64;
        if sequence.received > sequence.byte_length {
            return self.fail("rpc chunk sequence exceeds declared length");
        }
        if (sequence.chunks.len() as u64) < sequence.count {
            self.sequence = Some(sequence);
            return Decoded::Partial;
        }
        self.finish(sequence)
    }

    /// Decodes a sequence that just received its last chunk; the bytes must add up to the
    /// declared length.
    fn finish(&mut self, sequence: RpcSequence) -> Decoded {
        if sequence.received != sequence.byte_length {
            return self.fail("rpc chunk sequence length mismatch");
        }
        let text = match String::from_utf8(sequence.chunks.concat()) {
            Ok(text) => text,
            Err(_) => return self.fail("rpc chunk data is not UTF-8"),
        };
        match parse_frame(&text) {
            Ok(frame) => Decoded::Frame(frame),
            Err(_) => self.fail("rpc frame must be an object"),
        }
    }

    /// Ends the stream with a protocol error; the sequence, if any, is dropped.
    fn fail(&mut self, reason: &str) -> Decoded {
        self.sequence = None;
        Decoded::Error {
            reason: reason.to_string(),
        }
    }

    /// Checks one `rpc_chunk`'s metadata and data against the negotiated limits.
    fn chunk_meta(&self, frame: &Frame) -> Result<ChunkMeta, &'static str> {
        let chunk_id = frame
            .str_member("chunkId")
            .filter(|id| !id.is_empty() && id.encode_utf16().count() <= MAX_CHUNK_ID_UNITS);
        let index = member_count(frame, "index");
        let count = member_count(frame, "count");
        let byte_length = member_count(frame, "byteLength");
        let within_limits = count.is_some_and(|count| (2..=MAX_CHUNKS).contains(&count))
            && index.is_some_and(|index| count.is_some_and(|count| index < count))
            && byte_length
                .is_some_and(|length| (self.threshold..=MAX_REASSEMBLED_BYTES).contains(&length));
        let (Some(chunk_id), Some(index), Some(count), Some(byte_length), true) =
            (chunk_id, index, count, byte_length, within_limits)
        else {
            return Err("invalid rpc chunk metadata");
        };
        if frame.member("data").is_none() {
            return Err("invalid rpc chunk metadata");
        }
        let encoded = match frame.str_member("data") {
            Some(encoded) => encoded,
            None => return Err("invalid rpc chunk data"),
        };
        let data = match b64_decode(encoded) {
            Some(data) => data,
            None => return Err("invalid rpc chunk data"),
        };
        if data.len() > CHUNK_SLICE_BYTES {
            return Err("rpc chunk data exceeds the slice size");
        }
        Ok(ChunkMeta {
            chunk_id: chunk_id.to_string(),
            index,
            count,
            byte_length,
            data,
        })
    }
}

impl FrameDecoder for OmpDecoder {
    fn push(&mut self, raw: &str) -> Decoded {
        let line = raw.strip_suffix('\r').unwrap_or(raw);
        if line.is_empty() {
            return Decoded::Partial;
        }
        let frame = match parse_frame(line) {
            Ok(frame) => frame,
            Err(reason) => {
                return Decoded::Malformed {
                    line: line.to_string(),
                    reason: reason.to_string(),
                };
            }
        };
        if frame.kind == "rpc_chunk" {
            return self.chunk(frame);
        }
        if self.sequence.is_some() {
            return self.fail("rpc chunk sequence interrupted");
        }
        Decoded::Frame(frame)
    }
}

/// One `rpc_chunk`'s checked metadata and data.
struct ChunkMeta {
    chunk_id: String,
    index: u64,
    count: u64,
    byte_length: u64,
    data: Vec<u8>,
}

/// A frame still being reassembled.
struct RpcSequence {
    chunk_id: String,
    count: u64,
    byte_length: u64,
    chunks: Vec<Vec<u8>>,
    received: u64,
}

/// A member as a non-negative safe integer, the counts the protocol carries.
fn member_count(frame: &Frame, key: &str) -> Option<u64> {
    let number = frame.member(key)?.as_f64()?;
    if number != number.trunc() || !(0.0..=MAX_SAFE_INTEGER).contains(&number) {
        return None;
    }
    Some(number as u64)
}

/// Standard-alphabet base64, padding required; canonical means the re-encoding equals the input.
fn b64_decode(text: &str) -> Option<Vec<u8>> {
    if text.is_empty() || !text.len().is_multiple_of(4) {
        return None;
    }
    fn value(byte: u8) -> Option<u32> {
        match byte {
            b'A'..=b'Z' => Some(u32::from(byte - b'A')),
            b'a'..=b'z' => Some(u32::from(byte - b'a') + 26),
            b'0'..=b'9' => Some(u32::from(byte - b'0') + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    }
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(text.len() / 4 * 3);
    for (position, group) in bytes.as_chunks::<4>().0.iter().enumerate() {
        let last = position == bytes.len() / 4 - 1;
        let pads = group[2] == b'=' || group[3] == b'=';
        if group[0] == b'=' || group[1] == b'=' || (pads && !last) {
            return None;
        }
        if group[2] == b'=' && group[3] != b'=' {
            return None;
        }
        let high = (value(group[0])? << 18) | (value(group[1])? << 12);
        out.push((high >> 16) as u8);
        if group[2] != b'=' {
            let mid = high | (value(group[2])? << 6);
            out.push((mid >> 8) as u8);
            if group[3] != b'=' {
                out.push((mid | value(group[3])?) as u8);
            }
        }
    }
    // Non-canonical input (a wrong pad bit, a stray non-zero leftover) decodes here but
    // re-encodes differently.
    if b64_encode(&out) != text {
        return None;
    }
    Some(out)
}

/// Standard-alphabet base64 with padding.
fn b64_encode(bytes: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for group in bytes.chunks(3) {
        let mut buf = [0u8; 3];
        buf[..group.len()].copy_from_slice(group);
        let packed = (u32::from(buf[0]) << 16) | (u32::from(buf[1]) << 8) | u32::from(buf[2]);
        out.push(ALPHABET[(packed >> 18) as usize & 63] as char);
        out.push(ALPHABET[(packed >> 12) as usize & 63] as char);
        out.push(if group.len() > 1 {
            ALPHABET[(packed >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if group.len() > 2 {
            ALPHABET[packed as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

/// A completed handshake: the session id and `get_state`'s data.
#[derive(Debug)]
pub struct Handshake {
    pub session_id: Option<String>,
    /// `get_state`'s data, empty object when absent.
    pub state: Value,
}

/// The ready frame's members the handshake needs, guarded.
struct ReadyFrame {
    supported_protocol_versions: Vec<f64>,
    max_frame_bytes: Option<f64>,
}

/// Guards and reads a ready frame: a numeric `protocolVersion`, an array of numbers in
/// `supportedProtocolVersions`, an optional numeric `maxFrameBytes`.
fn ready_frame(frame: &Frame) -> Option<ReadyFrame> {
    if frame.kind != "ready" {
        return None;
    }
    frame.member("protocolVersion").and_then(Value::as_f64)?;
    let versions = frame
        .member("supportedProtocolVersions")?
        .as_array()?
        .iter()
        .map(|version| version.as_f64())
        .collect::<Option<Vec<f64>>>()?;
    Some(ReadyFrame {
        supported_protocol_versions: versions,
        max_frame_bytes: frame.member("maxFrameBytes").and_then(Value::as_f64),
    })
}

/// The event filter command body, without an id.
pub fn event_filter_json() -> String {
    r#"{"type":"set_event_filter","events":["message_update","message_end","tool_execution_start","tool_execution_end","auto_retry_start","auto_retry_end","retry_fallback_applied","auto_compaction_start","auto_compaction_end","notice"],"messageUpdates":"delta"}"#
        .to_string()
}

/// The `--tools` list of the access level's args: the value after `--tools` split on `,`.
pub fn expected_tools(entry: &Entry, access: Access) -> Vec<String> {
    let Some(spec) = entry.run.access.get(&access) else {
        return Vec::new();
    };
    let Some(position) = spec.args.iter().position(|arg| arg == "--tools") else {
        return Vec::new();
    };
    match spec.args.get(position + 1) {
        Some(list) => list.split(',').map(str::to_string).collect(),
        None => Vec::new(),
    }
}

/// Runs the handshake on a started omp. `timeout` covers ready plus every response. On any
/// failure but an exit, omp is killed and its stdin closed: a peer the handshake gave up on
/// must not outlive the attempt.
pub fn handshake(
    conn: &mut RpcConn<OmpDecoder>,
    expected: &[String],
    timeout: Duration,
) -> Result<Handshake, RpcError> {
    let result = handshake_steps(conn, expected, timeout);
    if let Err(error) = &result
        && error.kind != RpcErrorKind::Exited
    {
        // The kill goes first: a peer that drains on stdin EOF would otherwise race its own
        // clean exit past the kill the failed handshake owes it.
        conn.stream().stop_handle().kill();
        conn.end_input();
    }
    result
}

/// The handshake's own exchange, without the kill.
fn handshake_steps(
    conn: &mut RpcConn<OmpDecoder>,
    expected: &[String],
    timeout: Duration,
) -> Result<Handshake, RpcError> {
    let deadline = Instant::now() + timeout;
    let ready = wait_for_ready(conn, deadline, timeout)?;
    if !ready.supported_protocol_versions.contains(&2.0) {
        let versions: Vec<String> = ready
            .supported_protocol_versions
            .iter()
            .map(f64::to_string)
            .collect();
        return Err(RpcError {
            kind: RpcErrorKind::Protocol,
            message: format!(
                "omp offers RPC protocol {}; 2 is required",
                versions.join(", ")
            ),
            command: None,
        });
    }
    conn.decoder_mut().negotiated(ready.max_frame_bytes);
    // All the commands go out before any answer is awaited: a replaying peer answers them only
    // once it has taken every one.
    let ids = [conn.next_id(), conn.next_id(), conn.next_id()];
    conn.send(
        &ids[0],
        r#"{"type":"negotiate_protocol","protocolVersion":2}"#,
    )?;
    conn.send(&ids[1], &event_filter_json())?;
    conn.send(&ids[2], r#"{"type":"get_state"}"#)?;
    let responses = match conn.await_responses(&ids, Some(deadline), &mut |_| {}) {
        Ok(responses) => responses,
        Err(error) => {
            return match error.kind {
                RpcErrorKind::Timeout => Err(timeout_error(timeout)),
                _ => Err(error),
            };
        }
    };
    let state = match responses.last().and_then(|response| response.data.clone()) {
        Some(state @ Value::Object(_)) => state,
        _ => Value::Object(Map::new()),
    };
    check_tools(&state, expected)?;
    Ok(Handshake {
        session_id: state
            .get("sessionId")
            .and_then(Value::as_str)
            .map(str::to_string),
        state,
    })
}

/// Reads frames until a valid `ready` comes; every other frame is ignored, and a `ready` that
/// fails the guard is treated as any other frame.
fn wait_for_ready(
    conn: &mut RpcConn<OmpDecoder>,
    deadline: Instant,
    timeout: Duration,
) -> Result<ReadyFrame, RpcError> {
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Err(timeout_error(timeout));
        }
        match conn.next_frame(Some(remaining))? {
            Incoming::Frame(frame) => {
                if let Some(ready) = ready_frame(&frame) {
                    return Ok(ready);
                }
            }
            Incoming::Timeout => return Err(timeout_error(timeout)),
            Incoming::Eof(exit) => {
                return Err(RpcError {
                    kind: RpcErrorKind::Exited,
                    message: describe_exit("omp", &exit),
                    command: None,
                });
            }
        }
    }
}

fn timeout_error(timeout: Duration) -> RpcError {
    RpcError {
        kind: RpcErrorKind::Timeout,
        message: format!(
            "omp did not complete the RPC handshake within {} ms",
            timeout.as_millis()
        ),
        command: None,
    }
}

/// Refuses a peer whose `dumpTools` lacks any expected tool. Tools beyond the expected set are
/// fine: newer omp adds its own registry tools, and the session's `--tools` allowlist governs
/// what it can call.
fn check_tools(state: &Value, expected: &[String]) -> Result<(), RpcError> {
    let names: Vec<&str> = state
        .get("dumpTools")
        .and_then(Value::as_array)
        .map(|tools| {
            tools
                .iter()
                .filter_map(|tool| tool.get("name").and_then(Value::as_str))
                .collect()
        })
        .unwrap_or_default();
    let missing: Vec<&str> = expected
        .iter()
        .filter(|want| !names.contains(&want.as_str()))
        .map(|want| want.as_str())
        .collect();
    if missing.is_empty() {
        return Ok(());
    }
    Err(RpcError {
        kind: RpcErrorKind::Protocol,
        message: format!(
            "omp lacks the expected tools [{}]; it exposes [{}]",
            missing.join(", "),
            names.join(", ")
        ),
        command: None,
    })
}

/// Names startup stderr that is not trouble: a line starting `Still starting after `, then
/// digits, then `s`, then a word boundary.
pub fn is_benign_stderr(line: &str) -> bool {
    let Some(rest) = line.strip_prefix("Still starting after ") else {
        return false;
    };
    let bytes = rest.as_bytes();
    let mut index = 0;
    while index < bytes.len() && bytes[index].is_ascii_digit() {
        index += 1;
    }
    if index == 0 || index >= bytes.len() || bytes[index] != b's' {
        return false;
    }
    index += 1;
    match bytes.get(index) {
        None => true,
        Some(byte) => !(byte.is_ascii_alphanumeric() || *byte == b'_'),
    }
}

/// Why a prompt failed, as the prompt_result carried it.
#[derive(Clone, Debug, PartialEq)]
pub struct PromptError {
    pub message: String,
    pub http_status: Option<u16>,
}

/// One prompt's answer: how the turn ended, its error, and when it ran.
#[derive(Clone, Debug, PartialEq)]
pub struct OmpTurn {
    /// completed, aborted or error.
    pub status: String,
    pub error: Option<PromptError>,
    pub sent_at_ms: u64,
    pub ended_at_ms: u64,
}

/// Sums over every assistant message_end so far.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Tally {
    pub final_text: String,
    pub last_stop_reason: String,
    pub turns: u64,
    pub model: Option<String>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cache_read_tokens: u64,
    pub cache_write_tokens: u64,
    pub cost_usd: f64,
}

impl Tally {
    /// Folds one assistant message_end into the sums; every other frame is ignored, and a
    /// missing or mistyped count counts 0.
    pub fn apply(&mut self, frame: &Frame) {
        if frame.kind != "message_end" {
            return;
        }
        let Some(message) = frame.member("message") else {
            return;
        };
        if message.get("role").and_then(Value::as_str) != Some("assistant") {
            return;
        }
        self.turns += 1;
        if let Some(model) = reported_model(message) {
            self.model = Some(model);
        }
        self.final_text = text_of(message.get("content"));
        self.last_stop_reason = message
            .get("stopReason")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        let usage = message.get("usage");
        self.input_tokens += count_of(usage.and_then(|usage| usage.get("input"))) as u64;
        self.output_tokens += count_of(usage.and_then(|usage| usage.get("output"))) as u64;
        self.cache_read_tokens += count_of(usage.and_then(|usage| usage.get("cacheRead"))) as u64;
        self.cache_write_tokens += count_of(usage.and_then(|usage| usage.get("cacheWrite"))) as u64;
        self.cost_usd += count_of(
            usage
                .and_then(|usage| usage.get("cost"))
                .and_then(|cost| cost.get("total")),
        );
    }
}

/// One frame's events: what the run's listeners see. Frame types the fold does not track fold
/// to nothing; they stay in the log.
pub fn fold_frame(frame: &Frame) -> Vec<RunEvent> {
    match frame.kind.as_str() {
        "response" => fold_response(frame),
        "message_update" => fold_update(frame),
        "message_end" => fold_message_end(frame),
        "tool_execution_start" => fold_tool_start(frame),
        "tool_execution_end" => vec![RunEvent::ToolResult {
            is_error: frame.bool_member("isError") == Some(true),
        }],
        "auto_retry_start" => fold_retry(frame),
        "prompt_result" => fold_prompt_result(frame),
        _ => Vec::new(),
    }
}

/// Responses: refusals as errors, the session id get_state reports, quiet successes as nothing.
fn fold_response(frame: &Frame) -> Vec<RunEvent> {
    let Some(response) = as_response(frame) else {
        return Vec::new();
    };
    if !response.success {
        let message = response
            .error
            .clone()
            .unwrap_or_else(|| format!("omp refused {}", response.command));
        return vec![RunEvent::Error {
            message,
            status: None,
        }];
    }
    if response.command != "get_state" {
        return Vec::new();
    }
    let Some(session) = response
        .data
        .as_ref()
        .and_then(|data| data.get("sessionId"))
        .and_then(Value::as_str)
    else {
        return Vec::new();
    };
    vec![RunEvent::Session(session.to_string())]
}

/// Message updates: only string text deltas.
fn fold_update(frame: &Frame) -> Vec<RunEvent> {
    let Some(event) = frame.member("assistantMessageEvent") else {
        return Vec::new();
    };
    if event.get("type").and_then(Value::as_str) != Some("text_delta") {
        return Vec::new();
    }
    let Some(delta) = event.get("delta").and_then(Value::as_str) else {
        return Vec::new();
    };
    vec![RunEvent::TextDelta(delta.to_string())]
}

/// Assistant message ends: the joined text, and an error event when omp ended the message in
/// error.
fn fold_message_end(frame: &Frame) -> Vec<RunEvent> {
    let Some(message) = frame.member("message").and_then(Value::as_object) else {
        return Vec::new();
    };
    if message.get("role").and_then(Value::as_str) != Some("assistant") {
        return Vec::new();
    }
    let mut events = Vec::new();
    let text = text_of(message.get("content"));
    if !text.is_empty() {
        events.push(RunEvent::AssistantText(text));
    }
    if message.get("stopReason").and_then(Value::as_str) == Some("error") {
        let reason = message
            .get("errorMessage")
            .and_then(Value::as_str)
            .unwrap_or("omp ended an assistant message in error");
        events.push(RunEvent::Error {
            message: reason.to_string(),
            status: None,
        });
    }
    events
}

/// Tool starts: a name, then a summary from args (the first string value), the intent, or none.
fn fold_tool_start(frame: &Frame) -> Vec<RunEvent> {
    let Some(name) = frame.str_member("toolName") else {
        return Vec::new();
    };
    let summary = first_string_value(frame.member("args"))
        .or_else(|| frame.str_member("intent").map(str::to_string))
        .unwrap_or_default();
    vec![RunEvent::ToolUse {
        name: name.to_string(),
        summary,
    }]
}

/// Retries: a leading 3-digit status, and counts that may be missing.
fn fold_retry(frame: &Frame) -> Vec<RunEvent> {
    vec![RunEvent::ApiRetry {
        attempt: frame.count_member("attempt") as u64,
        max_attempts: frame.count_member("maxAttempts") as u64,
        status: frame.str_member("errorMessage").and_then(leading_status),
    }]
}

/// The leading 3 digits of an error message, when they spell a status.
fn leading_status(message: &str) -> Option<u16> {
    let bytes = message.as_bytes();
    if bytes.len() < 3 || !bytes[..3].iter().all(u8::is_ascii_digit) {
        return None;
    }
    let status =
        (bytes[0] - b'0') as u16 * 100 + (bytes[1] - b'0') as u16 * 10 + (bytes[2] - b'0') as u16;
    Some(status)
}

/// Prompt results: only errors, with their http status when given.
fn fold_prompt_result(frame: &Frame) -> Vec<RunEvent> {
    let Some(result) = as_prompt_result(frame) else {
        return Vec::new();
    };
    if result.status != "error" {
        return Vec::new();
    }
    let Some(error) = result.error else {
        return Vec::new();
    };
    vec![RunEvent::Error {
        message: error.message,
        status: error.http_status,
    }]
}

/// One valid prompt_result as the turn logic reads it. Valid means a string id, booleans
/// `agentInvoked` and `sessionSettled`, and a status of completed, aborted or error; anything
/// else is no prompt_result at all.
struct PromptResultFrame {
    id: String,
    agent_invoked: bool,
    session_settled: bool,
    status: String,
    error: Option<PromptError>,
}

fn as_prompt_result(frame: &Frame) -> Option<PromptResultFrame> {
    if frame.kind != "prompt_result" {
        return None;
    }
    let status = frame.str_member("status")?;
    if !matches!(status, "completed" | "aborted" | "error") {
        return None;
    }
    Some(PromptResultFrame {
        id: frame.str_member("id")?.to_string(),
        agent_invoked: frame.bool_member("agentInvoked")?,
        session_settled: frame.bool_member("sessionSettled")?,
        status: status.to_string(),
        error: frame.member("error").and_then(prompt_error),
    })
}

/// The error object of a prompt_result: a non-object error is none.
fn prompt_error(error: &Value) -> Option<PromptError> {
    let object = error.as_object()?;
    Some(PromptError {
        message: object
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string(),
        http_status: status_of(object.get("httpStatus")),
    })
}

/// A number as an http status; anything else is none.
fn status_of(value: Option<&Value>) -> Option<u16> {
    let number = value?.as_f64()?;
    if number.is_finite() && (0.0..=f64::from(u16::MAX)).contains(&number) {
        Some(number as u16)
    } else {
        None
    }
}

/// One live omp run: the connection, the running tally, and the session the handshake read.
pub struct OmpRun {
    conn: RpcConn<OmpDecoder>,
    tally: Tally,
    session_id: Option<String>,
}

impl OmpRun {
    /// Takes the connection over from the handshake; the handshake's session id, when present,
    /// becomes a session event first.
    pub fn new(
        conn: RpcConn<OmpDecoder>,
        handshake: Handshake,
        on_event: &mut dyn FnMut(&RunEvent),
    ) -> OmpRun {
        let Handshake {
            session_id,
            state: _,
        } = handshake;
        if let Some(session) = &session_id {
            on_event(&RunEvent::Session(session.clone()));
        }
        OmpRun {
            conn,
            tally: Tally::default(),
            session_id,
        }
    }

    /// The session id the handshake read.
    pub fn session_id(&self) -> Option<&str> {
        self.session_id.as_deref()
    }

    /// The text of the last assistant message so far.
    pub fn final_text(&self) -> &str {
        &self.tally.final_text
    }

    /// The tally over every assistant message so far.
    pub fn tally(&self) -> &Tally {
        &self.tally
    }

    /// Sends the prompt and blocks until the turn is settled, folding every frame through
    /// `on_event`. A crash mid-turn fails the turn: the exit is the story.
    pub fn prompt(
        &mut self,
        text: &str,
        on_event: &mut dyn FnMut(&RunEvent),
    ) -> Result<OmpTurn, RpcError> {
        let id = self.conn.next_id();
        let sent_at_ms = now_ms();
        // serde_json cannot fail on a string; the fallback never runs.
        let escaped = serde_json::to_string(text).unwrap_or_else(|_| format!("{text:?}"));
        self.conn
            .send(&id, &format!(r#"{{"type":"prompt","message":{escaped}}}"#))?;
        let mut acked = false;
        let mut pending: Option<OmpTurn> = None;
        loop {
            match self.conn.next_frame(None)? {
                Incoming::Frame(frame) => {
                    self.tally.apply(&frame);
                    for event in fold_frame(&frame) {
                        on_event(&event);
                    }
                    if let Some(response) = as_response(&frame)
                        && response.id.as_deref() == Some(id.as_str())
                    {
                        if response.success {
                            acked = true;
                        } else if !acked {
                            return Err(RpcError {
                                kind: RpcErrorKind::Rejected,
                                message: response
                                    .error
                                    .clone()
                                    .unwrap_or_else(|| "omp refused prompt".to_string()),
                                command: Some("prompt".into()),
                            });
                        }
                    }
                    if let Some(result) = as_prompt_result(&frame)
                        && result.id == id
                    {
                        let turn = OmpTurn {
                            status: result.status,
                            error: result.error,
                            sent_at_ms,
                            ended_at_ms: now_ms(),
                        };
                        if result.session_settled || !result.agent_invoked {
                            return Ok(turn);
                        }
                        pending = Some(turn);
                    }
                    if frame.kind == "session_settled"
                        && let Some(turn) = pending.take()
                    {
                        return Ok(turn);
                    }
                }
                // `next_frame(None)` never times out.
                Incoming::Timeout => {
                    return Err(RpcError {
                        kind: RpcErrorKind::Timeout,
                        message: "omp frame read timed out".to_string(),
                        command: None,
                    });
                }
                Incoming::Eof(exit) => {
                    return Err(RpcError {
                        kind: RpcErrorKind::Exited,
                        message: describe_exit("omp", &exit),
                        command: None,
                    });
                }
            }
        }
    }

    /// Closes stdin; omp drains and exits, and every remaining frame is folded before the exit
    /// comes back.
    pub fn close(&mut self, on_event: &mut dyn FnMut(&RunEvent)) -> ProcExit {
        self.conn.end_input();
        loop {
            match self.conn.next_frame(None) {
                Ok(Incoming::Frame(frame)) => {
                    self.tally.apply(&frame);
                    for event in fold_frame(&frame) {
                        on_event(&event);
                    }
                }
                // `next_frame(None)` never times out; a broken stream leaves the exit.
                Ok(Incoming::Timeout) => {}
                Ok(Incoming::Eof(exit)) => return exit,
                Err(_) => return self.conn.stream().wait(),
            }
        }
    }
}

/// The run's output from its last turn: the turn's own failure, or "unfinished".
pub fn output(session_id: Option<&str>, tally: &Tally, last: &OmpTurn, span_ms: u64) -> WireOutput {
    let failure = match last.status.as_str() {
        "error" => Some(last.error.as_ref().map_or(
            "omp ended the turn in error without saying why".to_string(),
            |error| error.message.clone(),
        )),
        "completed" if tally.last_stop_reason != "stop" => Some(format!(
            "worker stopped without finishing (stopReason {})",
            tally.last_stop_reason
        )),
        _ => None,
    };
    let api_error_status = match last.status.as_str() {
        "error" => last.error.as_ref().and_then(|error| error.http_status),
        _ => None,
    };
    WireOutput {
        text: tally.final_text.clone(),
        session_id: session_id.map(str::to_string),
        failure,
        stats: RunStats {
            turns: tally.turns,
            model: tally.model.clone(),
            input_tokens: tally.input_tokens,
            output_tokens: tally.output_tokens,
            cache_read_tokens: tally.cache_read_tokens,
            cache_write_tokens: tally.cache_write_tokens,
            cost_usd: tally.cost_usd,
            duration_ms: span_ms,
            api_error_status,
        },
    }
}

/// Epoch milliseconds now; a clock before the epoch reads as 0.
fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_millis() as u64)
        .unwrap_or(0)
}
