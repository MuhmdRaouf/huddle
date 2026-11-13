//! huddle-jobs: render. Plain text for the slash commands: no ANSI, no colour, and no renderer
//! adds a trailing newline. Facts on one line are joined by " · ".

use crate::accept::AcceptOutcome;
use crate::git::ChangeSet;
use crate::id::parse_iso_ms;
use crate::model::{Access, Attempt, AttemptKind, AttemptOutcome, Job, JobState};

/// Chars of a task title the status table shows before an ellipsis.
const TITLE_CHARS: usize = 60;
/// Chars of a crashed attempt's stderr the outcome line quotes.
const STDERR_CHARS: usize = 160;
/// Change lines a result packet lists before "and N more".
const CHANGES_LISTED: usize = 50;
/// Log lines a job detail shows.
const LOG_LINES: usize = 4;
/// Chars of a diff a result packet shows before the truncation note.
pub const DIFF_CAP_CHARS: usize = 60_000;

/// "just now" under 60 s or in the future; "<n>m ago" under 1 h; "<n>h ago" under 24 h; "<n>d ago".
/// An unparseable timestamp reads as the oldest age.
pub fn ago(now_ms: u64, iso: &str) -> String {
    let from = parse_iso_ms(iso).unwrap_or(0);
    let elapsed = now_ms.saturating_sub(from) / 1000;
    if elapsed < 60 {
        "just now".to_string()
    } else if elapsed < 3600 {
        format!("{}m ago", elapsed / 60)
    } else if elapsed < 86_400 {
        format!("{}h ago", elapsed / 3600)
    } else {
        format!("{}d ago", elapsed / 86_400)
    }
}

/// Under 10 000 ms: "<seconds>.<tenths>s", truncated ("9.9s" for 9999, "0.0s" for 0). Under 60 s:
/// "<s>s". Under 1 h: "<m>m<ss>s", seconds two digits. Else "<h>h<mm>m". Whole seconds are rounded
/// from ms.
pub fn duration_text(ms: u64) -> String {
    if ms < 10_000 {
        return format!("{}.{}s", ms / 1000, (ms % 1000) / 100);
    }
    let secs = (ms + 500) / 1000;
    if secs < 60 {
        format!("{secs}s")
    } else if secs < 3600 {
        format!("{}m{:02}s", secs / 60, secs % 60)
    } else {
        format!("{}h{:02}m", secs / 3600, (secs % 3600) / 60)
    }
}

/// Whitespace runs collapse to one space, trimmed. Longer than `max` chars: the first max-1 chars
/// plus "…". max 0 gives "".
pub fn one_line(text: &str, max: usize) -> String {
    if max == 0 {
        return String::new();
    }
    let line = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if line.chars().count() <= max {
        return line;
    }
    let mut cut: String = line.chars().take(max - 1).collect();
    cut.push('…');
    cut
}

/// How an attempt ended, in words. An empty stderr tail leaves the colon and tail off.
pub fn outcome_text(outcome: &AttemptOutcome) -> String {
    let (head, tail) = match outcome {
        AttemptOutcome::Completed => return "completed".to_string(),
        AttemptOutcome::Crashed {
            exit_code,
            signal,
            stderr_tail,
        } => {
            let head = match (exit_code, signal) {
                (Some(code), _) => format!("crashed (exit {code})"),
                (None, Some(sig)) => format!("killed by {sig}"),
                (None, None) => "crashed".to_string(),
            };
            (head, one_line(stderr_tail, STDERR_CHARS))
        }
        AttemptOutcome::Error { message } => (format!("failed: {message}"), String::new()),
        AttemptOutcome::Timeout => return "timed out".to_string(),
        AttemptOutcome::Stopped => return "stopped".to_string(),
    };
    if tail.is_empty() {
        head
    } else {
        format!("{head}: {tail}")
    }
}

/// The attempt's kind, as the job file spells it.
fn kind_word(kind: AttemptKind) -> &'static str {
    match kind {
        AttemptKind::Initial => "initial",
        AttemptKind::ReviewFix => "review_fix",
    }
}

/// The job's access, as a word.
fn access_word(access: Access) -> &'static str {
    match access {
        Access::Readonly => "readonly",
        Access::Exec => "exec",
        Access::Write => "write",
    }
}

/// "#<n> (<kind>): <outcome_text or "running">".
pub fn attempt_summary(attempt: &Attempt) -> String {
    let ended = match &attempt.outcome {
        Some(outcome) => outcome_text(outcome),
        None => "running".to_string(),
    };
    format!("#{} ({}): {}", attempt.n, kind_word(attempt.kind), ended)
}

/// The state in words: "failed (<failure>)" when the job failed with a reason.
fn state_cell(job: &Job) -> String {
    match job.state {
        JobState::Failed => match &job.failure {
            Some(why) => format!("failed ({why})"),
            None => "failed".to_string(),
        },
        state => state.label().to_string(),
    }
}

/// "folder <path> (branch <name or "detached">, base <first 7 chars>)", plus a note when the
/// folder had uncommitted changes at start.
fn folder_line(job: &Job) -> String {
    let branch = job.base_branch.as_deref().unwrap_or("detached");
    let base: String = job.base_sha.chars().take(7).collect();
    let mut line = format!("folder {} (branch {branch}, base {base})", job.dir);
    if job.dirty_at_start {
        line.push_str("; it had uncommitted changes at start, so the diff includes them");
    }
    line
}

/// The status table, or the empty-repo line. Jobs come newest first, as the store gives them.
pub fn status_table(jobs: &[Job], now_ms: u64) -> String {
    if jobs.is_empty() {
        return "No huddle jobs in this repository yet. /huddle:run <harness> <task> starts one."
            .to_string();
    }
    let mut rows = vec![vec![
        "ID".to_string(),
        "HARNESS".to_string(),
        "STATE".to_string(),
        "ACCESS".to_string(),
        "AGE".to_string(),
        "TASK".to_string(),
    ]];
    for job in jobs {
        rows.push(vec![
            job.id.clone(),
            job.harness.clone(),
            state_cell(job),
            access_word(job.access).to_string(),
            ago(now_ms, &job.created_at),
            one_line(&job.title, TITLE_CHARS),
        ]);
    }
    table(rows)
}

/// Columns padded to the widest cell by char count, joined by two spaces; the last column is not
/// padded. Each row right-trimmed.
fn table(rows: Vec<Vec<String>>) -> String {
    let cols = rows.first().map(Vec::len).unwrap_or(0);
    let widths: Vec<usize> = (0..cols)
        .map(|col| {
            rows.iter()
                .filter_map(|row| row.get(col))
                .map(|cell| cell.chars().count())
                .max()
                .unwrap_or(0)
        })
        .collect();
    let mut lines = Vec::with_capacity(rows.len());
    for row in &rows {
        let mut line = String::new();
        for (i, cell) in row.iter().enumerate() {
            if i > 0 {
                line.push_str("  ");
            }
            line.push_str(cell);
            if i + 1 < row.len() {
                for _ in cell.chars().count()..widths[i] {
                    line.push(' ');
                }
            }
        }
        lines.push(line.trim_end().to_string());
    }
    lines.join("\n")
}

/// One job in full: state, folder, attempts, the log tail the caller already cut, and the one
/// command that applies. An active job is cancellable; a settled one has a result.
pub fn job_detail(job: &Job, log_tail: &[String], now_ms: u64) -> String {
    let mut lines = vec![
        format!("huddle job {}: {}", job.id, job.title),
        format!(
            "{} · {} · {} · created {} · updated {}",
            state_cell(job),
            job.harness,
            access_word(job.access),
            ago(now_ms, &job.created_at),
            ago(now_ms, &job.updated_at),
        ),
        folder_line(job),
    ];
    if job.attempts.is_empty() {
        lines.push("Attempts: none yet".to_string());
    } else {
        lines.push("Attempts:".to_string());
        for attempt in &job.attempts {
            lines.push(format!("  {}", attempt_summary(attempt)));
        }
    }
    if !log_tail.is_empty() {
        lines.push(format!("Log (last {LOG_LINES} lines):"));
        for line in log_tail {
            lines.push(format!("  {line}"));
        }
    }
    if job.state.is_active() {
        lines.push(format!("Cancel: /huddle:cancel {}", job.id));
    } else {
        lines.push(format!("Result: /huddle:result {}", job.id));
    }
    lines.join("\n")
}

/// What a reviewer sees: the changes, their stat and the diff itself.
pub struct ReviewView {
    pub changes: ChangeSet,
    pub diff_stat: Option<String>,
    pub diff: Option<String>,
}

/// The result packet: header, attempts when there were several, the harness's end when it was not
/// a clean completion, the report, the changes, the diff, and the next command.
pub fn result_packet(job: &Job, view: &ReviewView) -> String {
    let last = job.last_attempt();
    let mut lines = vec![
        format!("huddle job {}: {}", job.id, job.title),
        match last {
            Some(attempt) => format!(
                "{} · {} · {} · attempt {} ({})",
                state_cell(job),
                job.harness,
                access_word(job.access),
                attempt.n,
                kind_word(attempt.kind),
            ),
            None => format!(
                "{} · {} · {} · no attempt yet",
                state_cell(job),
                job.harness,
                access_word(job.access),
            ),
        },
        folder_line(job),
    ];
    if job.attempts.len() > 1 {
        lines.push(String::new());
        lines.push("Attempts".to_string());
        for attempt in &job.attempts {
            lines.push(format!("  {}", attempt_summary(attempt)));
        }
    }
    if let Some(outcome) = last.and_then(|attempt| attempt.outcome.as_ref())
        && !matches!(outcome, AttemptOutcome::Completed)
    {
        lines.push(String::new());
        lines.push(format!("Harness: {}", outcome_text(outcome)));
    }
    lines.push(String::new());
    lines.extend(report_lines(job));
    lines.push(String::new());
    lines.extend(change_lines(&view.changes));
    if let Some(stat) = view.diff_stat.as_deref().filter(|stat| !stat.is_empty()) {
        lines.push(String::new());
        lines.push("Diff stat".to_string());
        for line in stat.lines() {
            lines.push(format!("  {line}"));
        }
    }
    if let Some(diff) = view.diff.as_deref().filter(|diff| !diff.is_empty()) {
        let fence = fence_for(diff);
        lines.push(String::new());
        lines.push("Diff".to_string());
        lines.push(format!("{fence}diff"));
        lines.push(diff.trim_end_matches('\n').to_string());
        lines.push(fence);
    }
    lines.push(String::new());
    lines.push("Next".to_string());
    lines.push(format!("  {}", next_step(job)));
    lines.join("\n")
}

/// The last attempt's report: structured when it has one, else its free text, else "missing".
fn report_lines(job: &Job) -> Vec<String> {
    let last = job.last_attempt();
    let Some(report) = last.and_then(|attempt| attempt.report.as_ref()) else {
        return match last
            .and_then(|attempt| attempt.report_text.as_ref())
            .filter(|text| !text.trim().is_empty())
        {
            Some(text) => {
                let mut out = vec!["Report (free text)".to_string()];
                for line in text.lines() {
                    out.push(format!("  {line}"));
                }
                out
            }
            None => vec!["Report: missing (the harness ended without a report)".to_string()],
        };
    };
    let mut out = vec!["Report".to_string()];
    let summary = if report.summary.trim().is_empty() {
        "(no summary)"
    } else {
        report.summary.as_str()
    };
    out.push(format!("  {summary}"));
    if report.files.is_empty() {
        out.push("  Files: none".to_string());
    } else {
        out.push("  Files:".to_string());
        for file in &report.files {
            out.push(format!("  - {}: {}", file.path, file.why));
        }
    }
    if report.open_items.is_empty() {
        out.push("  Open items: none".to_string());
    } else {
        out.push("  Open items:".to_string());
        for item in &report.open_items {
            out.push(format!("  - {item}"));
        }
    }
    out
}

/// What the attempt touched: added, modified, deleted, untracked, capped, then "and N more".
fn change_lines(changes: &ChangeSet) -> Vec<String> {
    let mut rows: Vec<(char, &String)> = Vec::new();
    for path in &changes.added {
        rows.push(('A', path));
    }
    for path in &changes.modified {
        rows.push(('M', path));
    }
    for path in &changes.deleted {
        rows.push(('D', path));
    }
    for path in &changes.untracked {
        rows.push(('?', path));
    }
    if rows.is_empty() {
        return vec!["Changes: none".to_string()];
    }
    let total = rows.len();
    let mut out = vec![format!("Changes ({total}):")];
    for (letter, path) in rows.iter().take(CHANGES_LISTED) {
        out.push(format!("  {letter} {path}"));
    }
    if total > CHANGES_LISTED {
        out.push(format!("  and {} more", total - CHANGES_LISTED));
    }
    out
}

/// The one command the job's state calls for. The failed branch never tells the user to stop a
/// job that is not running.
pub fn next_step(job: &Job) -> String {
    let id = &job.id;
    match job.state {
        JobState::Accepted => "nothing left to do: the job was accepted.".to_string(),
        JobState::Cancelled => "nothing left to do: the job was cancelled.".to_string(),
        JobState::Completed => {
            let article = if job.access == Access::Exec {
                "an"
            } else {
                "a"
            };
            format!(
                "nothing left to do: the job finished ({article} {} job: nothing to merge).",
                access_word(job.access)
            )
        }
        JobState::Failed => {
            let why = job.failure.as_deref().unwrap_or("unknown");
            if job.access == Access::Write {
                format!(
                    "the harness never finished ({why}): nothing to accept. /huddle:result {id} \
                     --return \"<feedback>\" runs it again in the same folder, or \
                     /huddle:cancel {id} drops it."
                )
            } else {
                format!(
                    "the harness never finished ({why}): nothing to accept. \
                     /huddle:cancel {id} drops it."
                )
            }
        }
        JobState::Queued | JobState::Running => format!(
            "it is {}; /huddle:status {id} shows it, /huddle:cancel {id} stops it.",
            job.state.label()
        ),
        JobState::AwaitingReview => format!(
            "/huddle:result {id} --diff reads the diff, then --accept lands it or \
             --return \"<feedback>\" sends it back."
        ),
    }
}

/// Cut a diff at `max` chars, appending "\n[diff truncated: <n> more characters]\n" when cut.
pub fn cap_diff(diff: &str, max: usize) -> String {
    let total = diff.chars().count();
    if total <= max {
        return diff.to_string();
    }
    let kept: String = diff.chars().take(max).collect();
    format!(
        "{kept}\n[diff truncated: {} more characters]\n",
        total - max
    )
}

/// A backtick run no backtick run inside `text` can close: one longer than the longest run there.
fn fence_for(text: &str) -> String {
    let mut longest = 2;
    let mut run = 0;
    for ch in text.chars() {
        if ch == '`' {
            run += 1;
            longest = longest.max(run);
        } else {
            run = 0;
        }
    }
    "`".repeat(longest + 1)
}

/// Where the accepted work landed.
pub fn accepted_line(outcome: &AcceptOutcome, no_commit: bool) -> String {
    let id = &outcome.job.id;
    let dir = &outcome.job.dir;
    if no_commit {
        return format!("huddle job {id} accepted: the change stays uncommitted in {dir}");
    }
    match outcome.commit.as_deref() {
        Some(sha) => {
            let short = sha.get(..12).unwrap_or(sha);
            format!("huddle job {id} accepted: commit {short} on the current branch of {dir}")
        }
        None => format!("huddle job {id} accepted: no changes to commit"),
    }
}

/// What a return tells the caller once the harness runs the feedback detached.
pub fn returned_line(job: &Job) -> String {
    format!(
        "huddle job {} returned with feedback: attempt {} runs detached. /huddle:result {} once \
         it awaits review again.",
        job.id,
        job.attempts.len(),
        job.id
    )
}
