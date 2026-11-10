//! Report correction turns for the RPC harnesses: the report is read out of the final message,
//! checked against the report shape, and a report that does not check draws up to two correction
//! prompts on the same session.

use crate::rpc_common::RpcError;
use crate::rpc_omp::{OmpRun, OmpTurn};
use crate::rpc_pi::PiRun;
use crate::table::RunEvent;
use serde_json::Value;

/// How many more turns a report that fails its check gets; the session is kept, so each fix
/// lands in context.
pub const MAX_CORRECTIONS: usize = 2;

/// The JSON object a final message carries: the whole text, a fenced block, or the outermost
/// braces. Arrays and scalars never count.
pub fn extract_report(text: &str) -> Option<Value> {
    if let Some(value) = as_object(text) {
        return Some(value);
    }
    if let Some(block) = fenced_block(text)
        && let Some(value) = as_object(&block)
    {
        return Some(value);
    }
    let start = text.find('{')?;
    let end = text.rfind('}')?;
    if end < start {
        return None;
    }
    as_object(&text[start..=end])
}

/// The candidate when the whole of it parses as a JSON object.
fn as_object(candidate: &str) -> Option<Value> {
    let value: Value = serde_json::from_str(candidate).ok()?;
    if value.is_object() { Some(value) } else { None }
}

/// The text of the first fenced block: the tag after the opening fence is dropped, the content is
/// everything up to the closing fence, trimmed of whitespace.
fn fenced_block(text: &str) -> Option<String> {
    let open = text.find("```")?;
    let after = &text[open + 3..];
    let tag = after.chars().take_while(|c| c.is_ascii_lowercase()).count();
    let body = &after[tag..];
    let close = body.find("```")?;
    let block = &body[..close];
    Some(
        block
            .trim_matches(|c: char| c.is_whitespace() || c == '\u{feff}')
            .to_string(),
    )
}

/// How a JSON value is named in a problem message; a missing member is `undefined`.
fn kind_of(value: Option<&Value>) -> &'static str {
    match value {
        None => "undefined",
        Some(Value::Null) => "null",
        Some(Value::Bool(_)) => "boolean",
        Some(Value::Number(_)) => "number",
        Some(Value::String(_)) => "string",
        Some(Value::Array(_)) => "array",
        Some(Value::Object(_)) => "object",
    }
}

/// The report shape's three keys, in declaration order.
const ROOT_KEYS: [&str; 3] = ["summary", "files", "open_items"];

/// A file entry's two keys, in declaration order.
const FILE_KEYS: [&str; 2] = ["path", "why"];

/// Empty when the report checks; the problems in declaration order otherwise.
pub fn problems(report: Option<&Value>) -> Vec<String> {
    let Some(value) = report.filter(|value| !value.is_null()) else {
        return vec!["no structured report".to_string()];
    };
    let Some(object) = value.as_object() else {
        return vec![format!(
            "(root): Invalid input: expected object, received {}",
            kind_of(Some(value))
        )];
    };
    let mut problems = Vec::new();
    match object.get("summary") {
        Some(Value::String(_)) => {}
        other => problems.push(format!(
            "summary: Invalid input: expected string, received {}",
            kind_of(other)
        )),
    }
    match object.get("files") {
        Some(Value::Array(files)) => {
            for (index, file) in files.iter().enumerate() {
                check_file(index, file, &mut problems);
            }
        }
        other => problems.push(format!(
            "files: Invalid input: expected array, received {}",
            kind_of(other)
        )),
    }
    match object.get("open_items") {
        Some(Value::Array(items)) => {
            for (index, item) in items.iter().enumerate() {
                if !item.is_string() {
                    problems.push(format!(
                        "open_items.{index}: Invalid input: expected string, received {}",
                        kind_of(Some(item))
                    ));
                }
            }
        }
        other => problems.push(format!(
            "open_items: Invalid input: expected array, received {}",
            kind_of(other)
        )),
    }
    let unknown: Vec<&String> = object
        .keys()
        .filter(|key| !ROOT_KEYS.contains(&key.as_str()))
        .collect();
    if let Some(line) = unrecognized_keys("(root)", &unknown) {
        problems.push(line);
    }
    problems
}

/// One `files` element: an object with string `path` and `why`, no other keys.
fn check_file(index: usize, file: &Value, problems: &mut Vec<String>) {
    let Some(object) = file.as_object() else {
        problems.push(format!(
            "files.{index}: Invalid input: expected object, received {}",
            kind_of(Some(file))
        ));
        return;
    };
    for key in FILE_KEYS {
        match object.get(key) {
            Some(Value::String(_)) => {}
            other => problems.push(format!(
                "files.{index}.{key}: Invalid input: expected string, received {}",
                kind_of(other)
            )),
        }
    }
    let unknown: Vec<&String> = object
        .keys()
        .filter(|key| !FILE_KEYS.contains(&key.as_str()))
        .collect();
    if let Some(line) = unrecognized_keys(&format!("files.{index}"), &unknown) {
        problems.push(line);
    }
}

/// One line naming the keys a report object must not carry, in document order.
fn unrecognized_keys(path: &str, keys: &[&String]) -> Option<String> {
    let quoted: Vec<String> = keys.iter().map(|key| format!("{key:?}")).collect();
    match quoted.as_slice() {
        [] => None,
        [one] => Some(format!("{path}: Unrecognized key: {one}")),
        many => Some(format!("{path}: Unrecognized keys: {}", many.join(", "))),
    }
}

/// Exactly the prompt that asks for a corrected report.
pub fn correction_prompt(problems: &[String]) -> String {
    format!(
        "Your report was invalid: {}. Reply with only the corrected JSON object.",
        problems.join("; ")
    )
}

/// A prompt's turn, as the run's driver reports it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TurnSpan {
    pub sent_at_ms: u64,
    pub ended_at_ms: u64,
}

/// The run whose prompts `run_report_turns` drives.
pub trait TurnDriver {
    /// Sends one prompt and waits for its turn to end.
    fn prompt(&mut self, text: &str) -> Result<TurnSpan, RpcError>;
    /// The final assistant text so far.
    fn final_text(&self) -> String;
    /// A turn the engine ended in a way no correction can fix.
    fn failed(&self) -> bool;
}

/// How one run's report turns went.
#[derive(Clone, Debug, PartialEq)]
pub struct ReportTurns {
    pub started_at_ms: u64,
    pub ended_at_ms: u64,
    /// The accepted report, or the last extracted value; None when nothing parsed.
    pub value: Option<Value>,
}

/// Runs the prompt, then up to `MAX_CORRECTIONS` correction turns while the extracted report does
/// not check. A driver error propagates: the run crashed, and no correction can fix that.
pub fn run_report_turns<D: TurnDriver>(
    driver: &mut D,
    prompt: &str,
) -> Result<ReportTurns, RpcError> {
    let first = driver.prompt(prompt)?;
    let started_at_ms = first.sent_at_ms;
    let mut ended_at_ms = first.ended_at_ms;
    let mut value = extract_report(&driver.final_text());
    for _ in 0..MAX_CORRECTIONS {
        if driver.failed() {
            break;
        }
        let found = problems(value.as_ref());
        if found.is_empty() {
            break;
        }
        let again = driver.prompt(&correction_prompt(&found))?;
        ended_at_ms = again.ended_at_ms;
        value = extract_report(&driver.final_text());
    }
    Ok(ReportTurns {
        started_at_ms,
        ended_at_ms,
        value,
    })
}

/// The omp run's turns: `failed` reads the last turn's own status.
pub struct OmpDriver<'a> {
    run: &'a mut OmpRun,
    on_event: &'a mut dyn FnMut(&RunEvent),
    last: Option<OmpTurn>,
}

impl<'a> OmpDriver<'a> {
    pub fn new(run: &'a mut OmpRun, on_event: &'a mut dyn FnMut(&RunEvent)) -> OmpDriver<'a> {
        OmpDriver {
            run,
            on_event,
            last: None,
        }
    }

    /// The last turn the run settled, if one did.
    pub fn last_turn(&self) -> Option<&OmpTurn> {
        self.last.as_ref()
    }
}

impl TurnDriver for OmpDriver<'_> {
    fn prompt(&mut self, text: &str) -> Result<TurnSpan, RpcError> {
        let turn = self.run.prompt(text, self.on_event)?;
        let span = TurnSpan {
            sent_at_ms: turn.sent_at_ms,
            ended_at_ms: turn.ended_at_ms,
        };
        self.last = Some(turn);
        Ok(span)
    }

    fn final_text(&self) -> String {
        self.run.final_text().to_string()
    }

    fn failed(&self) -> bool {
        self.last
            .as_ref()
            .is_some_and(|turn| turn.status != "completed")
    }
}

/// The pi run's turns: `failed` reads the stream's own error stop.
pub struct PiDriver<'a> {
    run: &'a mut PiRun,
    on_event: &'a mut dyn FnMut(&RunEvent),
}

impl<'a> PiDriver<'a> {
    pub fn new(run: &'a mut PiRun, on_event: &'a mut dyn FnMut(&RunEvent)) -> PiDriver<'a> {
        PiDriver { run, on_event }
    }
}

impl TurnDriver for PiDriver<'_> {
    fn prompt(&mut self, text: &str) -> Result<TurnSpan, RpcError> {
        let span = self.run.prompt(text, self.on_event)?;
        Ok(TurnSpan {
            sent_at_ms: span.sent_at_ms,
            ended_at_ms: span.ended_at_ms,
        })
    }

    fn final_text(&self) -> String {
        self.run.final_text().to_string()
    }

    fn failed(&self) -> bool {
        self.run.failed_stop()
    }
}
