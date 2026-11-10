//! huddle-jobs: report.

use crate::model::{Access, Report};

/// The report inside a harness's final text: the whole text, then the first fenced block, then
/// the outermost braces, each tried in order; the first that parses as a `Report` wins. A
/// candidate parses only as exactly one JSON value, so trailing data is no match. The braces
/// scan runs only when prose precedes the braces: a text that starts with `{` is tried whole,
/// so `{report} junk` is not a report. None when no candidate parses.
pub fn extract_report(final_text: &str) -> Option<Report> {
    parse(final_text)
        .or_else(|| fenced_body(final_text).and_then(parse))
        .or_else(|| braces_candidate(final_text).and_then(parse))
}

/// The `Report` a candidate holds, when the whole of it is exactly one JSON value with the
/// report's keys and no others.
fn parse(candidate: &str) -> Option<Report> {
    serde_json::from_str(candidate).ok()
}

/// The body of the first fenced block: after "```" and its lowercase-letter language tag, the
/// text to the next "```", trimmed of surrounding whitespace. None when there is no complete fence.
fn fenced_body(text: &str) -> Option<&str> {
    let after_open = &text[text.find("```")? + 3..];
    let tag_len = after_open.len()
        - after_open
            .trim_start_matches(|c: char| c.is_ascii_lowercase())
            .len();
    let body = after_open[tag_len..].trim_start();
    let end = body.find("```")?;
    Some(body[..end].trim())
}

/// The text from the first `{` to the last `}`, inclusive, when prose precedes the braces. A
/// text that starts with `{` was already tried whole; its trailing data must refuse it.
fn braces_candidate(text: &str) -> Option<&str> {
    if text.trim_start().starts_with('{') {
        return None;
    }
    let start = text.find('{')?;
    let end = text.rfind('}')?;
    (end >= start).then(|| &text[start..=end])
}

const WRITE_LINES: &str = "You work in an isolated git worktree of the repository. Edit files only there.\n\
Leave your changes in the working tree. huddle reviews and commits them.\n\
Do not commit, stage, stash or switch branches.\n\
Never print, log or write secrets: API keys, tokens, passwords, private keys.";

const EXEC_LINES: &str = "You work in a throwaway checkout of the repository. Nothing you change here is kept.\n\
Run the commands the task needs. Do not create, edit or delete files in the repository.\n\
Do not commit, stage, stash or switch branches.\n\
Never print, log or write secrets: API keys, tokens, passwords, private keys.";

const READONLY_LINES: &str = "This is a read-only investigation in a throwaway checkout. Read and search; run only commands that change nothing.\n\
Do not create, edit or delete any file.\n\
Do not commit, stage, stash or switch branches.\n\
Never print, log or write secrets: API keys, tokens, passwords, private keys.";

/// The first prompt of a job: the task, then the footer. The runner appends the report
/// contract after it.
pub fn initial_prompt(task: &str, access: Access) -> String {
    let lines = match access {
        Access::Write => WRITE_LINES,
        Access::Exec => EXEC_LINES,
        Access::Readonly => READONLY_LINES,
    };
    format!("{}\n\n## huddle job\n\n{}", task.trim_end(), lines)
}

/// The prompt of a returned job. The feedback rides in a fenced block whose fence is longer
/// than any run of backticks inside it (at least three), so the fence survives feedback that
/// holds fenced text.
pub fn review_fix_prompt(feedback: &str) -> String {
    let fence = fence_for(feedback);
    format!(
        "The reviewer returned your work with this feedback:\n\n{fence}text\n{feedback}\n{fence}\n\n\
Address the feedback with the smallest change that satisfies it. Keep everything else as it is.\n\
End your run with the final report described in your first message."
    )
}

/// The backtick run that fences `text`: one more than the longest run inside it, at least three.
fn fence_for(text: &str) -> String {
    let mut longest = 0;
    let mut run = 0;
    for c in text.chars() {
        run = if c == '`' { run + 1 } else { 0 };
        longest = longest.max(run);
    }
    "`".repeat((longest + 1).max(3))
}
