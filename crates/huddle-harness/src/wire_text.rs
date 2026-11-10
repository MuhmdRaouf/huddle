//! huddle-harness: wire_text.

use crate::table::{ExitInfo, OneShotReader, RunEvent, RunStats, WireOutput};

/// Reader for a harness whose stdout is the answer: every line is report text, with no session
/// and no usage. Failure comes from the exit code, the signal, the forcing, or the silence.
pub struct TextReader {
    lines: String,
}

impl TextReader {
    pub fn new() -> TextReader {
        TextReader {
            lines: String::new(),
        }
    }
}

impl Default for TextReader {
    fn default() -> TextReader {
        TextReader::new()
    }
}
impl OneShotReader for TextReader {
    fn feed_line(&mut self, line: &str, _events: &mut Vec<RunEvent>) {
        self.lines.push_str(line);
        self.lines.push('\n');
    }

    fn finish(self: Box<Self>, exit: &ExitInfo) -> WireOutput {
        let text = self.lines.trim_end().to_string();
        let failure = if let Some(forced) = &exit.forced {
            Some(format!("the harness was stopped ({forced})"))
        } else if exit.code != Some(0) {
            let how = match exit.code {
                Some(code) => code.to_string(),
                None => exit.signal.clone().unwrap_or_else(|| "unknown".to_string()),
            };
            let tail = exit.stderr_tail.trim();
            Some(if tail.is_empty() {
                format!("the harness exited {how}")
            } else {
                format!("the harness exited {how}: {tail}")
            })
        } else if text.is_empty() {
            Some("the harness printed nothing".to_string())
        } else {
            None
        };
        WireOutput {
            text,
            session_id: None,
            failure,
            stats: RunStats::default(),
        }
    }
}
