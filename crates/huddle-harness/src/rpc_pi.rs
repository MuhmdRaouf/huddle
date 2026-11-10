//! pi's RPC reader: the handshake (one `get_state` that must name a session id, and the benign
//! session-create stderr line), then the turn run: prompts, the frame fold, the usage tally and
//! the run's output. Every failure but an exit leaves pi dead: a peer the handshake gave up on
//! must not outlive the attempt.

use crate::rpc_common::{
    Frame, Incoming, PlainDecoder, Response, RpcConn, RpcError, RpcErrorKind, as_response,
    count_of, describe_exit, reported_model, text_of,
};
use crate::table::{RunEvent, RunStats, WireOutput, first_string_value};
use huddle_procs::group::{group_alive, signal_group};
use huddle_procs::stream::ProcExit;
use serde_json::Value;
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

/// How long the handshake waits for the killed group to empty, signalling again while a member
/// lasts: the kill's group scan can miss a child forked in the middle of it.
const KILL_SETTLE: Duration = Duration::from_millis(2000);

/// How often the settle loop probes the group.
const POLL_INTERVAL: Duration = Duration::from_millis(25);

/// A completed handshake: the session id get_state named, and get_state's data.
#[derive(Debug)]
pub struct Handshake {
    pub session_id: String,
    /// get_state's data.
    pub state: Value,
}

/// What pi prints on stderr while creating a session for a `--session-id` it has not seen (it
/// resumes otherwise); matched lines stay out of the exit's stderr tail.
pub fn is_benign_stderr(line: &str) -> bool {
    const PREFIX: &str = "Warning: No project session found with id '";
    const SUFFIX: &str = "'; creating a new session with that id.";
    let Some(after) = line.strip_prefix(PREFIX) else {
        return false;
    };
    match after.strip_suffix(SUFFIX) {
        Some(id) => !id.is_empty(),
        None => false,
    }
}

/// Sends `get_state` and waits for its answer within `timeout`. pi is killed (stdin closed and
/// the group SIGKILLed) on every failure but an exit: a peer the handshake gave up on must not
/// outlive the attempt.
pub fn handshake(
    conn: &mut RpcConn<PlainDecoder>,
    timeout: Duration,
) -> Result<Handshake, RpcError> {
    let id = conn.next_id();
    let deadline = Instant::now() + timeout;
    let result = match conn.send(&id, r#"{"type":"get_state"}"#) {
        Ok(()) => conn
            .await_responses(&[id], Some(deadline), &mut |_| {})
            .and_then(session_of),
        Err(error) => Err(error),
    };
    if let Err(error) = &result
        && error.kind != RpcErrorKind::Exited
    {
        conn.end_input();
        conn.stream().stop_handle().kill();
        settle_killed_group(conn.pid());
    }
    result.map_err(|error| match error.kind {
        RpcErrorKind::Timeout => RpcError {
            kind: RpcErrorKind::Timeout,
            message: format!(
                "pi did not complete the RPC handshake within {} ms",
                timeout.as_millis()
            ),
            command: None,
        },
        _ => error,
    })
}

/// The handshake's answer: `data.sessionId` must be a string.
fn session_of(mut answers: Vec<Response>) -> Result<Handshake, RpcError> {
    let Some(response) = answers.pop() else {
        return Err(no_session_id());
    };
    let data = response.data.unwrap_or(Value::Null);
    let Some(session_id) = data.get("sessionId").and_then(Value::as_str) else {
        return Err(no_session_id());
    };
    Ok(Handshake {
        session_id: session_id.to_string(),
        state: data,
    })
}

fn no_session_id() -> RpcError {
    RpcError {
        kind: RpcErrorKind::Protocol,
        message: "pi's get_state answer carries no sessionId".to_string(),
        command: None,
    }
}

/// SIGKILLs the group until it is empty or the settle budget runs out. One group scan can miss a
/// child forked while it runs: the scan kills the leader, the child joins the group after it and
/// lives on in a leaderless group no later `kill` reaches (a reaped leader makes it a no-op).
fn settle_killed_group(pid: u32) {
    let deadline = Instant::now() + KILL_SETTLE;
    while group_alive(pid) && Instant::now() < deadline {
        signal_group(pid, libc::SIGKILL);
        thread::sleep(POLL_INTERVAL);
    }
}

/// When one turn ran: epoch ms when the prompt was written, and when the turn was settled.
#[derive(Clone, Debug, PartialEq)]
pub struct PiTurn {
    pub sent_at_ms: u64,
    pub ended_at_ms: u64,
}

/// The usage a message_update carried for the message still streaming: pi reports its usage
/// cumulatively on every update.
#[derive(Clone, Debug, Default, PartialEq)]
struct PendingUsage {
    input: f64,
    output: f64,
    cache_read: f64,
    cache_write: f64,
    cost_usd: f64,
}

/// Sums over every assistant message_end so far. A message that carries no count for a value
/// falls back to the pending update's count, because pi's own number already rode along.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Tally {
    pub final_text: String,
    pub last_stop_reason: String,
    pub last_error: String,
    pub turns: u64,
    pub model: Option<String>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cache_read_tokens: u64,
    pub cache_write_tokens: u64,
    pub cost_usd: f64,
    pending: PendingUsage,
}

impl Tally {
    /// The tally's half of one frame; every other frame is ignored, and a missing or mistyped
    /// count counts 0.
    pub fn apply(&mut self, frame: &Frame) {
        match frame.kind.as_str() {
            "message_update" => {
                let usage = frame.member("usage");
                self.pending.input = count_of(usage.and_then(|usage| usage.get("input")));
                self.pending.output = count_of(usage.and_then(|usage| usage.get("output")));
                self.pending.cache_read = count_of(usage.and_then(|usage| usage.get("cacheRead")));
                self.pending.cache_write =
                    count_of(usage.and_then(|usage| usage.get("cacheWrite")));
                self.pending.cost_usd = count_of(
                    usage
                        .and_then(|usage| usage.get("cost"))
                        .and_then(|cost| cost.get("total")),
                );
            }
            "message_end" => {
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
                if self.last_stop_reason == "error" {
                    self.last_error = message
                        .get("errorMessage")
                        .and_then(Value::as_str)
                        .unwrap_or("pi ended an assistant message in error")
                        .to_string();
                }
                let usage = message.get("usage");
                self.input_tokens += pick(
                    usage.and_then(|usage| usage.get("input")),
                    self.pending.input,
                ) as u64;
                self.output_tokens += pick(
                    usage.and_then(|usage| usage.get("output")),
                    self.pending.output,
                ) as u64;
                self.cache_read_tokens += pick(
                    usage.and_then(|usage| usage.get("cacheRead")),
                    self.pending.cache_read,
                ) as u64;
                self.cache_write_tokens += pick(
                    usage.and_then(|usage| usage.get("cacheWrite")),
                    self.pending.cache_write,
                ) as u64;
                self.cost_usd += pick(
                    usage
                        .and_then(|usage| usage.get("cost"))
                        .and_then(|cost| cost.get("total")),
                    self.pending.cost_usd,
                );
                self.pending = PendingUsage::default();
            }
            _ => {}
        }
    }
}

/// The value when it is a finite number, else the fallback.
fn pick(value: Option<&Value>, fallback: f64) -> f64 {
    match value {
        Some(Value::Number(number)) => number
            .as_f64()
            .filter(|count| count.is_finite())
            .unwrap_or(fallback),
        _ => fallback,
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
            .unwrap_or_else(|| format!("pi refused {}", response.command));
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

/// Assistant message ends: the joined text, and an error event when pi ended the message in
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
            .unwrap_or("pi ended an assistant message in error");
        events.push(RunEvent::Error {
            message: reason.to_string(),
            status: None,
        });
    }
    events
}

/// Tool starts: a name, then a summary from the first string argument, or none.
fn fold_tool_start(frame: &Frame) -> Vec<RunEvent> {
    let Some(name) = frame.str_member("toolName") else {
        return Vec::new();
    };
    vec![RunEvent::ToolUse {
        name: name.to_string(),
        summary: first_string_value(frame.member("args")).unwrap_or_default(),
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

/// One live pi run: the connection, the running tally, and the session the handshake read.
pub struct PiRun {
    conn: RpcConn<PlainDecoder>,
    tally: Tally,
    session_id: String,
}

impl PiRun {
    /// Takes the connection over from the handshake; the session id becomes a session event
    /// first.
    pub fn new(
        conn: RpcConn<PlainDecoder>,
        handshake: Handshake,
        on_event: &mut dyn FnMut(&RunEvent),
    ) -> PiRun {
        let Handshake {
            session_id,
            state: _,
        } = handshake;
        on_event(&RunEvent::Session(session_id.clone()));
        PiRun {
            conn,
            tally: Tally::default(),
            session_id,
        }
    }

    /// The session id the handshake read.
    pub fn session_id(&self) -> &str {
        &self.session_id
    }

    /// The text of the last assistant message so far.
    pub fn final_text(&self) -> &str {
        &self.tally.final_text
    }

    /// The tally over every assistant message so far.
    pub fn tally(&self) -> &Tally {
        &self.tally
    }

    /// Says a turn ended in an error no correction can fix: pi reports engine failures on the
    /// message stream, and there is nothing to correct after one.
    pub fn failed_stop(&self) -> bool {
        self.tally.last_stop_reason == "error"
    }

    /// Sends the prompt and blocks until the agent_settled that ends the turn, folding every
    /// frame through `on_event`. `success:true` is admission only; the failure it refuses with
    /// is the turn's error. A crash mid-turn fails the turn: the exit is the story.
    pub fn prompt(
        &mut self,
        text: &str,
        on_event: &mut dyn FnMut(&RunEvent),
    ) -> Result<PiTurn, RpcError> {
        let id = self.conn.next_id();
        let sent_at_ms = now_ms();
        // serde_json cannot fail on a string; the fallback never runs.
        let escaped = serde_json::to_string(text).unwrap_or_else(|_| format!("{text:?}"));
        self.conn
            .send(&id, &format!(r#"{{"type":"prompt","message":{escaped}}}"#))?;
        let mut acked = false;
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
                                    .unwrap_or_else(|| "pi refused prompt".to_string()),
                                command: Some("prompt".into()),
                            });
                        }
                    }
                    if frame.kind == "extension_ui_request"
                        && let Some(reply) = unattended_reply(&frame)
                    {
                        // Not a command, so no id rides first; false once stdin is closed.
                        let _ = self.conn.stream().write_line(&reply);
                    }
                    if frame.kind == "agent_settled" {
                        break;
                    }
                }
                // `next_frame(None)` never times out.
                Incoming::Timeout => {}
                Incoming::Eof(exit) => {
                    return Err(RpcError {
                        kind: RpcErrorKind::Exited,
                        message: describe_exit("pi", &exit),
                        command: None,
                    });
                }
            }
        }
        self.recover_final_text(on_event);
        Ok(PiTurn {
            sent_at_ms,
            ended_at_ms: now_ms(),
        })
    }

    /// Closes stdin; pi saves its session and drains, and every remaining frame is folded before
    /// the exit comes back. pi answers an abort after agent_settled, so a reply nobody waits on
    /// is read here.
    pub fn close(&mut self, on_event: &mut dyn FnMut(&RunEvent)) -> ProcExit {
        self.conn.end_input();
        loop {
            match self.conn.next_frame(None) {
                Ok(Incoming::Frame(frame)) => {
                    self.tally.apply(&frame);
                    for event in fold_frame(&frame) {
                        on_event(&event);
                    }
                    if frame.kind == "extension_ui_request"
                        && let Some(reply) = unattended_reply(&frame)
                    {
                        let _ = self.conn.stream().write_line(&reply);
                    }
                }
                // `next_frame(None)` never times out; a broken stream leaves the exit.
                Ok(Incoming::Timeout) => {}
                Ok(Incoming::Eof(exit)) => return exit,
                Err(_) => return self.conn.stream().wait(),
            }
        }
    }

    /// Asks pi for the session's last assistant text when the final message ended `stop` with no
    /// text: pi's last assistant message can end with no text parts although the session holds
    /// one. Any failure leaves the empty text standing.
    fn recover_final_text(&mut self, on_event: &mut dyn FnMut(&RunEvent)) {
        if self.tally.last_stop_reason != "stop" || !self.tally.final_text.is_empty() {
            return;
        }
        let id = self.conn.next_id();
        if self
            .conn
            .send(&id, r#"{"type":"get_last_assistant_text"}"#)
            .is_err()
        {
            return;
        }
        let tally = &mut self.tally;
        let mut drain = |frame: &Frame| {
            tally.apply(frame);
            for event in fold_frame(frame) {
                on_event(&event);
            }
        };
        let Ok(answers) = self.conn.await_responses(&[id], None, &mut drain) else {
            return;
        };
        let Some(text) = answers
            .first()
            .and_then(|response| response.data.as_ref())
            .and_then(|data| data.get("text"))
            .and_then(Value::as_str)
        else {
            return;
        };
        if !text.is_empty() {
            self.tally.final_text = text.to_string();
        }
    }
}

/// The automatic answer to an extension_ui_request: RPC has no human UI. Confirmations are
/// approved; choices needing input return cancellation to the extension. A request with no
/// string id or another method goes unanswered.
fn unattended_reply(frame: &Frame) -> Option<String> {
    let id = frame.str_member("id")?;
    let confirmed = match frame.str_member("method") {
        Some("confirm") => true,
        Some("select" | "input" | "editor") => false,
        _ => return None,
    };
    // serde_json cannot fail on a string; the fallback never runs.
    let escaped = serde_json::to_string(id).unwrap_or_else(|_| format!("{id:?}"));
    Some(match confirmed {
        true => format!(r#"{{"type":"extension_ui_response","id":{escaped},"confirmed":true}}"#),
        false => format!(r#"{{"type":"extension_ui_response","id":{escaped},"cancelled":true}}"#),
    })
}

/// The run's output from the tally: the final text, the session, the summed usage, and the
/// failure the last stop reason tells.
pub fn output(session_id: &str, tally: &Tally, span_ms: u64) -> WireOutput {
    let failure = if tally.last_stop_reason == "error" {
        Some(tally.last_error.clone())
    } else if tally.last_stop_reason != "stop" {
        // No assistant message ended at all leaves the empty stop reason here too.
        Some(format!(
            "worker stopped without finishing (stopReason {})",
            tally.last_stop_reason
        ))
    } else {
        None
    };
    WireOutput {
        text: tally.final_text.clone(),
        session_id: Some(session_id.to_string()),
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
            api_error_status: leading_status(&tally.last_error),
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
