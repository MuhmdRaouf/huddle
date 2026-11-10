//! The RPC layer the omp and pi readers share: one JSON object per line as a `Frame`, the
//! response shape, the command-id counter, the abort soft stop, the attempt logs, and the
//! single-threaded connection that writes commands, decodes stdout and matches responses by id.

use huddle_procs::group::group_alive;
use huddle_procs::stream::{InputHandle, NextLine, ProcExit, SoftStop, Streaming};
use serde_json::Value;
use std::fs::{DirBuilder, File, OpenOptions};
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::thread;
use std::time::{Duration, Instant};

/// One decoded frame: an object with a string `type`, otherwise opaque here.
#[derive(Clone, Debug, PartialEq)]
pub struct Frame {
    /// The frame's `type`.
    pub kind: String,
    /// The exact JSON text of the frame (the attempt log and the fold read this).
    pub text: String,
    pub value: Value,
}

impl Frame {
    /// The member `key`, any JSON value.
    pub fn member(&self, key: &str) -> Option<&Value> {
        self.value.get(key)
    }

    /// The member `key` as a string.
    pub fn str_member(&self, key: &str) -> Option<&str> {
        self.member(key).and_then(Value::as_str)
    }

    /// The member `key` as a boolean.
    pub fn bool_member(&self, key: &str) -> Option<bool> {
        self.member(key).and_then(Value::as_bool)
    }

    /// A finite number, 0 for a missing or mistyped member.
    pub fn count_member(&self, key: &str) -> f64 {
        count_of(self.member(key))
    }
}

/// The text of an array's object parts: the `text` member of each, joined.
pub fn text_of(content: Option<&Value>) -> String {
    let Some(Value::Array(parts)) = content else {
        return String::new();
    };
    parts
        .iter()
        .filter_map(|part| {
            part.as_object()
                .and_then(|object| object.get("text"))
                .and_then(Value::as_str)
        })
        .collect()
}

/// A finite number, 0 for a missing or mistyped value.
pub fn count_of(value: Option<&Value>) -> f64 {
    match value {
        Some(Value::Number(number)) => number
            .as_f64()
            .filter(|count| count.is_finite())
            .unwrap_or(0.0),
        _ => 0.0,
    }
}

/// What one pushed line decoded to.
#[derive(Debug)]
pub enum Decoded {
    Frame(Frame),
    /// A blank line, or a chunk of a frame still reassembling.
    Partial,
    /// A line that is not a frame: logged raw and skipped by the caller.
    Malformed {
        line: String,
        reason: String,
    },
    /// A broken chunk sequence: the stream can no longer be trusted.
    Error {
        reason: String,
    },
}

/// Decodes one stream's lines.
pub trait FrameDecoder {
    fn push(&mut self, raw: &str) -> Decoded;
}

/// pi's decoder: strip one trailing "\r", blank is Partial, any object with a string type is a
/// Frame, everything else is Malformed.
pub struct PlainDecoder;

impl FrameDecoder for PlainDecoder {
    fn push(&mut self, raw: &str) -> Decoded {
        let line = raw.strip_suffix('\r').unwrap_or(raw);
        if line.is_empty() {
            return Decoded::Partial;
        }
        match parse_frame(line) {
            Ok(frame) => Decoded::Frame(frame),
            Err(reason) => Decoded::Malformed {
                line: line.to_string(),
                reason: reason.to_string(),
            },
        }
    }
}

/// Decodes `text` as a frame, or says why it is not one.
pub fn parse_frame(text: &str) -> Result<Frame, &'static str> {
    let value: Value = serde_json::from_str(text).map_err(|_| "not JSON")?;
    let object = value.as_object().ok_or("not an RPC frame")?;
    let kind = object
        .get("type")
        .and_then(Value::as_str)
        .ok_or("not an RPC frame")?;
    Ok(Frame {
        kind: kind.to_string(),
        text: text.to_string(),
        value,
    })
}

/// The ways an RPC exchange fails.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RpcErrorKind {
    Start,
    Exited,
    Timeout,
    Protocol,
    Rejected,
    Closed,
}

/// The error of one RPC exchange.
#[derive(Clone, Debug, PartialEq)]
pub struct RpcError {
    pub kind: RpcErrorKind,
    pub message: String,
    /// For Rejected: what the peer refused.
    pub command: Option<String>,
}

impl std::fmt::Display for RpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.message)
    }
}

impl std::error::Error for RpcError {}

/// One response as the wire carries it.
#[derive(Clone, Debug, PartialEq)]
pub struct Response {
    pub id: Option<String>,
    pub command: String,
    pub success: bool,
    pub data: Option<Value>,
    pub error: Option<String>,
}

/// The frame as a response: type "response" with a string command and a bool success.
pub fn as_response(frame: &Frame) -> Option<Response> {
    if frame.kind != "response" {
        return None;
    }
    Some(Response {
        id: frame.str_member("id").map(str::to_string),
        command: frame.str_member("command")?.to_string(),
        success: frame.bool_member("success")?,
        data: frame.member("data").cloned(),
        error: frame.str_member("error").map(str::to_string),
    })
}

/// `{"id":"c1",` + the body's members: the id rides first so the peer sees it before anything
/// else. `body` must be a non-empty JSON object text.
pub fn with_id(id: &str, body: &str) -> String {
    let id_json = serde_json::to_string(id).unwrap_or_else(|_| format!("{id:?}"));
    match body.strip_prefix('{') {
        Some(rest) => format!("{{\"id\":{id_json},{rest}"),
        None => format!("{{\"id\":{id_json},{body}"),
    }
}

/// How a process ended, for the exited error's message: "<name> ended (exit code 1): <stderr
/// tail>" or "<name> ended (signal SIGTERM)"; no ": tail" when the tail is empty.
pub fn describe_exit(name: &str, exit: &ProcExit) -> String {
    let how = match exit.code {
        Some(code) => format!("exit code {code}"),
        None => format!("signal {}", exit.signal.as_deref().unwrap_or("")),
    };
    let tail = exit.stderr_tail.trim();
    if tail.is_empty() {
        format!("{name} ended ({how})")
    } else {
        format!("{name} ended ({how}): {tail}")
    }
}

/// The model a tool says it ran on: "provider/model" from a message's `model` and `provider`
/// members, no double prefix. None when `model` is missing or empty.
pub fn reported_model(message: &Value) -> Option<String> {
    let model = message.get("model")?.as_str()?;
    if model.is_empty() {
        return None;
    }
    match message.get("provider").and_then(Value::as_str) {
        Some(provider) if !provider.is_empty() => {
            let prefix = format!("{provider}/");
            if model.starts_with(&prefix) {
                Some(model.to_string())
            } else {
                Some(format!("{prefix}{model}"))
            }
        }
        _ => Some(model.to_string()),
    }
}

/// A fresh command-id counter, starting at 0 (next_id yields "c1" first).
pub fn shared_counter() -> Arc<AtomicU64> {
    Arc::new(AtomicU64::new(0))
}

/// The soft stop both RPC harnesses use: `{"id":"cN","type":"abort"}`, then stdin closed, then
/// wait for the group to end (the caller's grace cuts this off). N comes from `counter`.
pub fn abort_soft_stop(counter: Arc<AtomicU64>) -> SoftStop {
    Box::new(move |input: &InputHandle| {
        let id = format!("c{}", counter.fetch_add(1, Ordering::SeqCst) + 1);
        let _ = input.write_line(&with_id(&id, r#"{"type":"abort"}"#));
        input.end_input();
        while group_alive(input.pid()) {
            thread::sleep(Duration::from_millis(25));
        }
    })
}

/// The byte length a tool result field may reach before the log cuts it.
pub const LOG_FIELD_LIMIT: usize = 64 * 1024;

/// How many bytes of the head and of the tail a cut field keeps.
pub const LOG_KEEP: usize = 16 * 1024;

/// Stands in for a prompt's text in the outbound log; the text itself lives in job.json.
pub const PROMPT_REF: &str = "job.json#prompt";

/// The log line for an inbound frame: `frame.text`, unless a tool result field is too big, when
/// that field is cut to its byte count plus its first and last `LOG_KEEP` bytes. Inbound frames
/// carry the full pre-elision tool output (a tool_execution_end's `result.details`, a toolResult
/// message_end's `content` and `details`), which the fold never reads.
pub fn log_text(frame: &Frame) -> String {
    match frame.kind.as_str() {
        "tool_execution_end" => cut_members(frame, "result", &["details"]),
        "message_end" => cut_tool_result_message(frame),
        _ => frame.text.clone(),
    }
}

/// The log line for an outbound command, as sent, except a prompt's `message` becomes
/// `{"$ref":"job.json#prompt"}`: the prompt text is not for the log.
pub fn outbound_log_text(command: &str) -> String {
    let Ok(value) = serde_json::from_str::<Value>(command) else {
        return command.to_string();
    };
    if value.get("type").and_then(Value::as_str) != Some("prompt") {
        return command.to_string();
    }
    let mut value = value;
    if let Some(members) = value.as_object_mut() {
        members.insert(
            "message".to_string(),
            serde_json::json!({ "$ref": PROMPT_REF }),
        );
    }
    serde_json::to_string(&value).unwrap_or_else(|_| command.to_string())
}

/// Serializes `frame` again with the named members of `container` (an object) cut; `frame.text` when
/// nothing needed cutting.
fn cut_members(frame: &Frame, container: &str, keys: &[&str]) -> String {
    if !frame.member(container).is_some_and(Value::is_object) {
        return frame.text.clone();
    }
    let mut value = frame.value.clone();
    let Some(object) = value.get_mut(container) else {
        return frame.text.clone();
    };
    if !keys.iter().any(|key| cut_member(object, key)) {
        return frame.text.clone();
    }
    serde_json::to_string(&value).unwrap_or_else(|_| frame.text.clone())
}

/// Serializes again a message_end frame whose message is a toolResult, cutting its `content` and
/// `details`; `frame.text` for any other message and when nothing needed cutting.
fn cut_tool_result_message(frame: &Frame) -> String {
    let is_tool_result = frame
        .member("message")
        .and_then(|message| message.get("role"))
        .and_then(Value::as_str)
        == Some("toolResult");
    if !is_tool_result {
        return frame.text.clone();
    }
    cut_members(frame, "message", &["content", "details"])
}

/// Replaces object member `key` with its cut stand-in when its JSON runs over `LOG_FIELD_LIMIT`;
/// says whether it did.
fn cut_member(object: &mut Value, key: &str) -> bool {
    let Some(members) = object.as_object_mut() else {
        return false;
    };
    let Some(field) = members.get_mut(key) else {
        return false;
    };
    let Ok(compact) = serde_json::to_string(field) else {
        return false;
    };
    if compact.len() <= LOG_FIELD_LIMIT {
        return false;
    }
    *field = serde_json::json!({
        "$truncated": compact.len(),
        "head": head_of(&compact, LOG_KEEP),
        "tail": tail_of(&compact, LOG_KEEP),
    });
    true
}

/// The first `keep` bytes of `text`, walked back to a char boundary so no character is halved.
fn head_of(text: &str, keep: usize) -> &str {
    let mut end = keep.min(text.len());
    while end > 0 && !text.is_char_boundary(end) {
        end -= 1;
    }
    &text[..end]
}

/// The last `keep` bytes of `text`, walked forward to a char boundary so no character is halved.
fn tail_of(text: &str, keep: usize) -> &str {
    let mut start = text.len().saturating_sub(keep);
    while start < text.len() && !text.is_char_boundary(start) {
        start += 1;
    }
    &text[start..]
}

/// One attempt's two logs: inbound frames and outbound commands, one line each. Writes stop
/// after the first failure, which `failure` reports.
pub struct RpcLog {
    frames: File,
    commands: File,
    failed: Option<String>,
}

impl RpcLog {
    /// Opens both for append (mode 0600), creating the parent dir (0700). A caller that wants no
    /// log attaches no log.
    pub fn open(frames: &Path, commands: &Path) -> Result<RpcLog, String> {
        let frames_file = open_append(frames)?;
        let commands_file = open_append(commands)?;
        Ok(RpcLog {
            frames: frames_file,
            commands: commands_file,
            failed: None,
        })
    }

    /// Appends the log line for one inbound frame.
    pub fn inbound(&mut self, frame: &Frame) {
        write_line(&mut self.frames, &log_text(frame), &mut self.failed);
    }

    /// Appends one raw stdout line, as received, for lines the decoder rejected.
    pub fn inbound_raw(&mut self, line: &str) {
        write_line(&mut self.frames, line, &mut self.failed);
    }

    /// Appends the log line for one outbound command, as sent.
    pub fn outbound(&mut self, command: &str) {
        write_line(
            &mut self.commands,
            &outbound_log_text(command),
            &mut self.failed,
        );
    }

    /// The first write failure; logging stops after it.
    pub fn failure(&self) -> Option<&str> {
        self.failed.as_deref()
    }
}

/// Appends line plus newline; after a failure every later write is a no-op.
fn write_line(file: &mut File, line: &str, failed: &mut Option<String>) {
    if failed.is_some() {
        return;
    }
    if let Err(err) = writeln!(file, "{line}") {
        *failed = Some(err.to_string());
    }
}

/// Opens path for appending (0600), creating its parent dir (0700).
fn open_append(path: &Path) -> Result<File, String> {
    let parent = match path.parent() {
        Some(parent) if !parent.as_os_str().is_empty() => parent,
        _ => Path::new("."),
    };
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(parent)
        .map_err(|err| format!("cannot open log {}: {err}", path.display()))?;
    OpenOptions::new()
        .append(true)
        .create(true)
        .mode(0o600)
        .open(path)
        .map_err(|err| format!("cannot open log {}: {err}", path.display()))
}

/// One answer from the frame reader.
#[derive(Debug)]
pub enum Incoming {
    Frame(Frame),
    Timeout,
    /// stdout ended; carries the process exit (stdout drained, process reaped).
    Eof(ProcExit),
}

/// The single-threaded RPC connection over one streamed child: one reader (the run's own
/// thread) walks stdout through the decoder, commands go out with ids from the shared counter,
/// and `await_responses` matches each answer by id. Frames that are not the response are handed
/// to the caller.
pub struct RpcConn<D: FrameDecoder> {
    stream: Streaming,
    decoder: D,
    name: &'static str,
    counter: Arc<AtomicU64>,
    input_closed: bool,
    log: Option<RpcLog>,
}

impl<D: FrameDecoder> RpcConn<D> {
    /// `counter` is the shared command-id counter (`shared_counter()`), so the soft stop can
    /// take ids too.
    pub fn new(
        stream: Streaming,
        decoder: D,
        name: &'static str,
        counter: Arc<AtomicU64>,
    ) -> RpcConn<D> {
        RpcConn {
            stream,
            decoder,
            name,
            counter,
            input_closed: false,
            log: None,
        }
    }

    /// Attaches the attempt logs: `send` and `next_frame` write them as frames flow.
    pub fn attach_log(&mut self, log: RpcLog) {
        self.log = Some(log);
    }

    /// The process-group leader's pid.
    pub fn pid(&self) -> u32 {
        self.stream.pid()
    }

    /// "c1", "c2", ... one counter for every command and prompt. A soft stop may also need ids;
    /// it reads the same counter.
    pub fn next_id(&self) -> String {
        format!("c{}", self.counter.fetch_add(1, Ordering::SeqCst) + 1)
    }

    /// Writes `with_id(id, body)` plus newline. When the write fails (the child closed its
    /// stdin) this returns Ok: the failure surfaces on the next read as Eof, whose exit tells
    /// the story.
    pub fn send(&mut self, id: &str, body: &str) -> Result<(), RpcError> {
        if self.input_closed {
            return Err(RpcError {
                kind: RpcErrorKind::Closed,
                message: format!(
                    "{}'s stdin is closed; no further commands can be sent",
                    self.name
                ),
                command: None,
            });
        }
        let command = with_id(id, body);
        if let Some(log) = self.log.as_mut() {
            log.outbound(&command);
        }
        let _ = self.stream.write_line(&command);
        Ok(())
    }

    /// Reads, decodes and returns the next real frame, skipping Malformed lines and Partial
    /// results. A broken chunk sequence is a Protocol error.
    pub fn next_frame(&mut self, timeout: Option<Duration>) -> Result<Incoming, RpcError> {
        loop {
            match self.stream.next_line(timeout) {
                NextLine::Line(raw) => match self.decoder.push(&raw) {
                    Decoded::Frame(frame) => {
                        if let Some(log) = self.log.as_mut() {
                            log.inbound(&frame);
                        }
                        return Ok(Incoming::Frame(frame));
                    }
                    Decoded::Partial => continue,
                    Decoded::Malformed { line, .. } => {
                        if let Some(log) = self.log.as_mut() {
                            log.inbound_raw(&line);
                        }
                        continue;
                    }
                    Decoded::Error { reason } => {
                        return Err(RpcError {
                            kind: RpcErrorKind::Protocol,
                            message: format!("{} RPC stream: {reason}", self.name),
                            command: None,
                        });
                    }
                },
                NextLine::Timeout => return Ok(Incoming::Timeout),
                NextLine::Eof => {
                    let exit = self.stream.wait();
                    return Ok(Incoming::Eof(exit));
                }
            }
        }
    }

    /// Closes stdin; every later `send` fails with Closed.
    pub fn end_input(&mut self) {
        self.input_closed = true;
        self.stream.end_input();
    }

    /// Reads frames (handed to `on_frame`) until each id has its response; returns them in the
    /// order of `ids`. success:false becomes Rejected with the peer's error or "<name> refused
    /// <command>". A response with no id, or for a different id, is skipped and handed to
    /// `on_frame`. stdout's end becomes Exited; `deadline` is checked per read and becomes
    /// Timeout past it.
    pub fn await_responses(
        &mut self,
        ids: &[String],
        deadline: Option<Instant>,
        on_frame: &mut dyn FnMut(&Frame),
    ) -> Result<Vec<Response>, RpcError> {
        let mut answers: Vec<Option<Response>> = (0..ids.len()).map(|_| None).collect();
        loop {
            if answers.iter().all(Option::is_some) {
                return Ok(answers.into_iter().flatten().collect());
            }
            if let Some(limit) = deadline
                && Instant::now() >= limit
            {
                return Err(self.timeout_error());
            }
            let read_timeout =
                deadline.map(|limit| limit.saturating_duration_since(Instant::now()));
            match self.next_frame(read_timeout)? {
                Incoming::Frame(frame) => {
                    let Some(response) = as_response(&frame) else {
                        on_frame(&frame);
                        continue;
                    };
                    match response
                        .id
                        .as_deref()
                        .and_then(|id| ids.iter().position(|wanted| wanted == id))
                    {
                        Some(index) if answers[index].is_none() => {
                            if !response.success {
                                return Err(self.refused_error(&response));
                            }
                            answers[index] = Some(response);
                        }
                        _ => on_frame(&frame),
                    }
                }
                Incoming::Timeout => return Err(self.timeout_error()),
                Incoming::Eof(exit) => {
                    return Err(RpcError {
                        kind: RpcErrorKind::Exited,
                        message: describe_exit(self.name, &exit),
                        command: None,
                    });
                }
            }
        }
    }

    /// The decoder, for negotiation that switches decoding mid-stream (omp's protocol 2).
    pub fn decoder_mut(&mut self) -> &mut D {
        &mut self.decoder
    }

    /// The stream, for the handles the run loop needs.
    pub fn stream(&self) -> &Streaming {
        &self.stream
    }

    /// The stream, for the caller that finishes the run itself.
    pub fn into_stream(self) -> Streaming {
        self.stream
    }

    fn timeout_error(&self) -> RpcError {
        RpcError {
            kind: RpcErrorKind::Timeout,
            message: format!("{} did not answer in time", self.name),
            command: None,
        }
    }

    fn refused_error(&self, response: &Response) -> RpcError {
        let message = response
            .error
            .clone()
            .unwrap_or_else(|| format!("{} refused {}", self.name, response.command));
        RpcError {
            kind: RpcErrorKind::Rejected,
            message,
            command: Some(response.command.clone()),
        }
    }
}
