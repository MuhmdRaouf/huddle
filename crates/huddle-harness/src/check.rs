//! The readiness report `/huddle:setup` prints: one object per harness, rendered as Markdown
//! or JSON.

use crate::env::harness_env;
use crate::resolve;
use crate::runner::{RunCtx, new_session_key, run_turn};
use crate::table::{Access, Brief, Entry, ReportKind, RunStats, SessionRef};
use huddle_config::env::Env;
use huddle_config::files::{DIR_MODE, create_dir};
use huddle_procs::stream::tool_version;
use std::path::PathBuf;
use std::time::Duration;

#[derive(Clone, Debug, PartialEq, serde::Serialize)]
pub struct CheckLine {
    pub name: String,
    pub ok: bool,
    pub detail: String,
}

#[derive(Clone, Debug, PartialEq, serde::Serialize)]
pub struct Report {
    pub id: String,
    pub label: String,
    pub ready: bool,
    pub checks: Vec<CheckLine>,
    pub flags_verified: bool,
    /// "checked", "extracted" or "free text".
    pub report_kind: String,
    pub resume: bool,
    /// "readonly", "exec", "write", in table order.
    pub access: Vec<String>,
    pub notes: Vec<String>,
    pub next_steps: Vec<String>,
}

/// One real, cheap model call.
pub trait SmokeRunner {
    fn smoke(&self, entry: &Entry) -> Result<String, String>;
}

/// The wall clock of one smoke run: a cold first start plus one short answer.
pub const SMOKE_TIMEOUT: Duration = Duration::from_millis(120_000);

/// The prompt the smoke run sends: the harness answers with the one word "ok".
pub const SMOKE_PROMPT: &str = "This is a check that you run. Use no tools. End at once with a report whose summary is the one word \"ok\", with no files and no open items.";

/// One smoke run in a throwaway directory, on the user's own login.
pub struct TurnSmoke<'a> {
    pub env: &'a Env,
    /// A throwaway directory: the cwd of the smoke run and where its logs go (`<dir>/<id>`).
    pub dir: PathBuf,
}

impl SmokeRunner for TurnSmoke<'_> {
    fn smoke(&self, entry: &Entry) -> Result<String, String> {
        let hint = |reason: String| {
            format!(
                "{reason}; run {} once and log in / configure it; huddle uses it as it is",
                entry.id
            )
        };
        let work = self.dir.join(&entry.id);
        create_dir(&work, DIR_MODE)
            .map_err(|err| hint(format!("cannot make {}: {err}", work.display())))?;
        let key = new_session_key().map_err(|err| hint(err.to_string()))?;
        let access = if entry.run.accepted_access().contains(&Access::Readonly) {
            Access::Readonly
        } else {
            Access::Write
        };
        let brief = Brief {
            prompt: SMOKE_PROMPT.to_string(),
            access,
            effort: None,
            session: SessionRef::New { key },
        };
        let log_dir = work.join("logs");
        let mut ctx = RunCtx::new(self.env, work, log_dir);
        ctx.timeout = Some(SMOKE_TIMEOUT);
        match run_turn(entry, &brief, &ctx) {
            Err(error) => Err(hint(error.to_string())),
            Ok(outcome) => match outcome.failure {
                Some(text) => Err(hint(text)),
                None => Ok(answered(entry, &outcome.stats)),
            },
        }
    }
}

/// The success line: which harness answered, with its own numbers when it reports any.
fn answered(entry: &Entry, stats: &RunStats) -> String {
    if stats.turns == 0 {
        return format!("ok: {} answered", entry.id);
    }
    let on = match &stats.model {
        Some(model) => format!(" on {model}"),
        None => String::new(),
    };
    let turns = if stats.turns == 1 { "turn" } else { "turns" };
    let cost = if stats.cost_usd < 1.0 {
        format!("{:.4}", stats.cost_usd)
    } else {
        format!("{:.2}", stats.cost_usd)
    };
    format!(
        "ok: {} answered{} ({}'s own numbers: {} {}, ${})",
        entry.id, on, entry.id, stats.turns, turns, cost
    )
}

/// The directory a version probe runs in: the env's HOME, or the OS temp directory when the env
/// names none. The probe starts the harness binary, and a harness must never be started inside
/// the user's repository before its worktree exists.
fn probe_cwd(env: &Env) -> PathBuf {
    match env.get("HOME") {
        Some(home) if !home.trim().is_empty() => PathBuf::from(home),
        _ => std::env::temp_dir(),
    }
}

/// Runs the cheap checks in order: binary, version, then the smoke run when one is wired.
/// The version probe starts with the harness env, never the caller's whole environment, and
/// runs in the env's HOME, never the caller's directory.
pub fn check_entry(entry: &Entry, env: &Env, smoke: Option<&dyn SmokeRunner>) -> Report {
    let (binary_line, resolved) = match resolve::resolve(entry, env) {
        Ok(found) => (
            CheckLine {
                name: "binary".into(),
                ok: true,
                detail: found.path.display().to_string(),
            },
            Some(found),
        ),
        Err(error) => (
            CheckLine {
                name: "binary".into(),
                ok: false,
                detail: error.to_string(),
            },
            None,
        ),
    };
    let version_line = match &resolved {
        Some(found) => match tool_version(
            &found.program,
            &found.prefix_args,
            &entry.check.version,
            &probe_cwd(env),
            Some(harness_env(env, &found.env)),
        ) {
            Ok(line) => CheckLine {
                name: "version".into(),
                ok: true,
                detail: line,
            },
            Err(error) => CheckLine {
                name: "version".into(),
                ok: false,
                detail: error,
            },
        },
        None => CheckLine {
            name: "version".into(),
            ok: false,
            detail: "not run: no binary".into(),
        },
    };
    let smoke_line = smoke.map(|runner| {
        if binary_line.ok && version_line.ok {
            match runner.smoke(entry) {
                Ok(text) => CheckLine {
                    name: "smoke".into(),
                    ok: true,
                    detail: text,
                },
                Err(text) => CheckLine {
                    name: "smoke".into(),
                    ok: false,
                    detail: text,
                },
            }
        } else {
            CheckLine {
                name: "smoke".into(),
                ok: false,
                detail: "not run: fix the lines above first".into(),
            }
        }
    });
    let smoke_ok = match &smoke_line {
        Some(line) => line.ok,
        None => true,
    };
    let ready = binary_line.ok && version_line.ok && smoke_ok;
    let next_steps = next_steps(entry, &binary_line, &version_line, smoke_line.as_ref());
    let mut checks = vec![binary_line, version_line];
    if let Some(line) = &smoke_line {
        checks.push(line.clone());
    }
    Report {
        id: entry.id.clone(),
        label: entry.label.clone(),
        ready,
        checks,
        flags_verified: entry.check.verified,
        report_kind: report_kind_word(&entry.caps.report).to_string(),
        resume: !entry.run.session.resume.is_empty(),
        access: entry
            .run
            .accepted_access()
            .into_iter()
            .map(|level| level.as_str().to_string())
            .collect(),
        notes: entry.caps.notes.clone(),
        next_steps,
    }
}

/// Every entry, no smoke run.
pub fn check_all(entries: &[Entry], env: &Env) -> Vec<Report> {
    entries
        .iter()
        .map(|entry| check_entry(entry, env, None))
        .collect()
}

/// Install and login hints for the failing checks, then the unverified-flags notice.
fn next_steps(
    entry: &Entry,
    binary: &CheckLine,
    version: &CheckLine,
    smoke: Option<&CheckLine>,
) -> Vec<String> {
    let mut steps = Vec::new();
    if !binary.ok {
        steps.push(entry.check.install_hint.clone());
    }
    if binary.ok && !version.ok && !steps.contains(&entry.check.login_hint) {
        steps.push(entry.check.login_hint.clone());
    }
    if let Some(line) = smoke
        && !line.ok
        && !steps.contains(&entry.check.login_hint)
    {
        steps.push(entry.check.login_hint.clone());
    }
    if !entry.check.verified {
        steps.push(format!(
            "Flags for {} come from a table of CLI options and are unverified: run /huddle:setup {} --smoke to prove them.",
            entry.label, entry.id
        ));
    }
    steps
}

fn report_kind_word(kind: &ReportKind) -> &'static str {
    match kind {
        ReportKind::Checked => "checked",
        ReportKind::Extracted => "extracted",
        ReportKind::FreeText => "free text",
    }
}

fn status_word(ready: bool) -> &'static str {
    if ready { "ready" } else { "needs attention" }
}

fn flags_word(verified: bool) -> &'static str {
    if verified { "checked" } else { "unverified" }
}

/// A check cell: the detail, or `FAILED:` before it when the check failed.
fn cell(line: &CheckLine) -> String {
    if line.ok {
        line.detail.clone()
    } else {
        format!("FAILED: {}", line.detail)
    }
}

pub fn render_markdown(report: &Report) -> String {
    let mut text = format!("# {} ({})\n", report.label, report.id);
    text.push_str(&format!("Status: {}\n", status_word(report.ready)));
    text.push_str("Checks:\n");
    for line in &report.checks {
        text.push_str(&format!("- {}: {}\n", line.name, cell(line)));
    }
    text.push_str(if report.flags_verified {
        "Flags: checked\n"
    } else {
        "Flags: unverified until a smoke run\n"
    });
    text.push_str(&format!("Report: {}\n", report.report_kind));
    text.push_str(if report.resume {
        "Resume: yes\n"
    } else {
        "Resume: no\n"
    });
    text.push_str(&format!("Access: {}\n", report.access.join(", ")));
    if !report.notes.is_empty() {
        text.push_str("Notes:\n");
        for note in &report.notes {
            text.push_str(&format!("- {note}\n"));
        }
    }
    if !report.next_steps.is_empty() {
        text.push_str("Next steps:\n");
        for step in &report.next_steps {
            text.push_str(&format!("- {step}\n"));
        }
    }
    text
}

/// One Markdown table row per report, in input order.
pub fn render_all_markdown(reports: &[Report]) -> String {
    let mut text = String::from(
        "| Harness | Status | Binary | Version | Flags | Report | Access |\n\
         |---|---|---|---|---|---|---|\n",
    );
    for report in reports {
        text.push_str(&format!(
            "| {} ({}) | {} | {} | {} | {} | {} | {} |\n",
            report.label,
            report.id,
            status_word(report.ready),
            cell(&report.checks[0]),
            cell(&report.checks[1]),
            flags_word(report.flags_verified),
            report.report_kind,
            report.access.join(", ")
        ));
    }
    text
}

/// The struct holds only strings, bools and vectors, so serialization cannot fail.
pub fn render_json(report: &Report) -> String {
    serde_json::to_string_pretty(report).unwrap_or_default()
}

pub fn render_all_json(reports: &[Report]) -> String {
    serde_json::to_string_pretty(reports).unwrap_or_default()
}
