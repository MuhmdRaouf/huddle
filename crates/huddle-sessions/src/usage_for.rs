//! Usage and attribution for named request ids, read from the session logs modified since a
//! time. Like the scan, it decodes only the fields `log::Summary` names: message text and
//! tool data are never held.

use std::collections::{HashMap, HashSet};
use std::fs::{self, File};
use std::io::{BufRead, BufReader, Seek, SeekFrom};
use std::path::{Path, PathBuf};

use crate::log::Summary;
use crate::model::Usage;
use crate::repo::Repos;
use crate::scan::modified_ms;

/// One request id found in a log, with where it ran.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LogUsage {
    pub request_id: String,
    pub session: String,
    /// `main`, or the subagent's id.
    pub agent_id: String,
    /// The name of the git repository around the log's working directory.
    pub project: Option<String>,
    pub usage: Usage,
    pub stop: Option<String>,
}

/// The requests in `ids` that the session and subagent logs under `<claude_dir>/projects`
/// name, reading only logs modified at or after `since_ms`. One full read of every such log.
pub fn usage_for(claude_dir: &Path, ids: &[String], since_ms: u64) -> Vec<LogUsage> {
    UsageReader::default().read(claude_dir, ids, since_ms)
}

/// `usage_for` that remembers how far it read each log, so a later call reads only the bytes
/// appended since. A log whose modified time and length are unchanged is skipped, and a log that
/// became shorter is read from its start. Only a requested id found in the newly read bytes is
/// credited: a line read before its id was asked for is not read again.
#[derive(Default)]
pub struct UsageReader {
    cursors: HashMap<PathBuf, Cursor>,
    bytes_read: u64,
}

/// How far one log was read, and what its earlier lines said (the working directory).
#[derive(Default)]
struct Cursor {
    modified_ms: u64,
    len: u64,
    /// The end of the last whole line read.
    offset: u64,
    summary: Summary,
}

impl UsageReader {
    /// Every byte read from a log so far, counting a partial last line each time it is read.
    pub fn bytes_read(&self) -> u64 {
        self.bytes_read
    }

    /// The requests in `ids` named by lines appended to the logs since the last call.
    pub fn read(&mut self, claude_dir: &Path, ids: &[String], since_ms: u64) -> Vec<LogUsage> {
        if ids.is_empty() {
            return Vec::new();
        }
        let wanted: HashSet<&str> = ids.iter().map(String::as_str).collect();
        let logs = logs_since(&claude_dir.join("projects"), since_ms);
        let live: HashSet<&Path> = logs.iter().map(|log| log.path.as_path()).collect();
        self.cursors.retain(|path, _| live.contains(path.as_path()));
        let mut found = Vec::new();
        let mut repos = Repos::default();
        for log in &logs {
            let Ok(len) = fs::metadata(&log.path).map(|meta| meta.len()) else {
                continue;
            };
            let cursor = self.cursors.entry(log.path.clone()).or_default();
            if cursor.len == len && cursor.modified_ms == log.modified_ms {
                continue;
            }
            if len < cursor.len {
                *cursor = Cursor::default();
            }
            cursor.summary.requests.clear();
            self.bytes_read += read_appended(&log.path, cursor, &wanted);
            cursor.len = len;
            cursor.modified_ms = log.modified_ms;
            let project = cursor
                .summary
                .cwd
                .as_deref()
                .and_then(|cwd| repos.of(cwd))
                .map(|repo| repo.name);
            for (request_id, usage, stop) in &cursor.summary.requests {
                found.push(LogUsage {
                    request_id: request_id.to_string(),
                    session: log.session.clone(),
                    agent_id: log.agent_id.clone(),
                    project: project.clone(),
                    usage: *usage,
                    stop: stop.as_deref().map(str::to_string),
                });
            }
        }
        found
    }
}

/// Feeds the whole lines after the cursor's offset into its summary, one line at a time, and
/// keeps only the requests in `wanted`, so memory holds a line and the wanted ids, never the
/// file. A partial last line stays unread for the next call. Returns the bytes read; a log that
/// cannot be opened or a line that cannot be read ends the read.
fn read_appended(path: &Path, cursor: &mut Cursor, wanted: &HashSet<&str>) -> u64 {
    let Ok(mut file) = File::open(path) else {
        return 0;
    };
    if file.seek(SeekFrom::Start(cursor.offset)).is_err() {
        return 0;
    }
    let mut reader = BufReader::new(file);
    let mut line = Vec::new();
    let mut read = 0;
    loop {
        line.clear();
        let Ok(n) = reader.read_until(b'\n', &mut line) else {
            break;
        };
        read += n as u64;
        if n == 0 || line.last() != Some(&b'\n') {
            break;
        }
        cursor.offset += n as u64;
        if let Ok(text) = std::str::from_utf8(&line) {
            cursor.summary.feed(text);
            cursor
                .summary
                .requests
                .retain(|(id, ..)| wanted.contains(id.as_str()));
        }
    }
    read
}

struct LogFile {
    path: PathBuf,
    modified_ms: u64,
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
            if let Some(at) = modified_ms(&path).filter(|at| *at >= since_ms) {
                logs.push(LogFile {
                    path: path.clone(),
                    modified_ms: at,
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
        let modified = modified_ms(&path).filter(|at| *at >= since_ms);
        if let (true, Some(at)) = (is_log, modified) {
            logs.push(LogFile {
                path,
                modified_ms: at,
                session: session.to_string(),
                agent_id,
            });
        }
    }
}
