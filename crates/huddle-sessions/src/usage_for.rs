//! Usage and attribution for named request ids, read from the session logs modified since a
//! time. Like the scan, it decodes only the fields `log::Summary` names: message text and
//! tool data are never held.

use std::fs;
use std::path::{Path, PathBuf};

use crate::log::Summary;
use crate::model::Usage;
use crate::scan::{base_name, modified_ms};

/// One request id found in a log, with where it ran.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LogUsage {
    pub request_id: String,
    pub session: String,
    /// `main`, or the subagent's id.
    pub agent_id: String,
    /// The last folder name of the log's working directory.
    pub project: Option<String>,
    pub usage: Usage,
    pub stop: Option<String>,
}

/// The requests in `ids` that the session and subagent logs under `<claude_dir>/projects`
/// name, reading only logs modified at or after `since_ms`.
pub fn usage_for(claude_dir: &Path, ids: &[String], since_ms: u64) -> Vec<LogUsage> {
    if ids.is_empty() {
        return Vec::new();
    }
    let mut found = Vec::new();
    for log in logs_since(&claude_dir.join("projects"), since_ms) {
        let Ok(text) = fs::read_to_string(&log.path) else {
            continue;
        };
        let summary = Summary::parse(&text);
        let project = summary.cwd.as_deref().and_then(base_name);
        for tag in summary.tags(&log.agent_id) {
            if ids.contains(&tag.request_id) {
                found.push(LogUsage {
                    request_id: tag.request_id,
                    session: log.session.clone(),
                    agent_id: tag.agent_id,
                    project: project.clone(),
                    usage: tag.usage,
                    stop: tag.stop,
                });
            }
        }
    }
    found
}

struct LogFile {
    path: PathBuf,
    session: String,
    agent_id: String,
}

/// Every session log and every subagent log under `projects` changed at or after `since_ms`.
fn logs_since(projects: &Path, since_ms: u64) -> Vec<LogFile> {
    let mut logs = Vec::new();
    let Ok(folders) = fs::read_dir(projects) else {
        return logs;
    };
    for folder in folders.flatten() {
        let Ok(files) = fs::read_dir(folder.path()) else {
            continue;
        };
        for file in files.flatten() {
            let path = file.path();
            let Some(session) = path
                .file_name()
                .and_then(|n| n.to_str())
                .and_then(|n| n.strip_suffix(".jsonl"))
                .map(str::to_string)
            else {
                continue;
            };
            if modified_ms(&path).is_some_and(|at| at >= since_ms) {
                logs.push(LogFile {
                    path: path.clone(),
                    session: session.clone(),
                    agent_id: "main".to_string(),
                });
            }
            subagent_logs(
                &path.with_extension("").join("subagents"),
                &session,
                since_ms,
                &mut logs,
            );
        }
    }
    logs
}

fn subagent_logs(dir: &Path, session: &str, since_ms: u64, logs: &mut Vec<LogFile>) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        let is_log = path.extension().is_some_and(|x| x == "jsonl");
        let Some(agent_id) = path
            .file_stem()
            .and_then(|n| n.to_str())
            .map(str::to_string)
        else {
            continue;
        };
        if is_log && modified_ms(&path).is_some_and(|at| at >= since_ms) {
            logs.push(LogFile {
                path,
                session: session.to_string(),
                agent_id,
            });
        }
    }
}
