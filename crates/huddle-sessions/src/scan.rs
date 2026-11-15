//! Finds the recent session logs under `<claude_dir>/projects` and assembles each `Session`.

use std::collections::HashMap;
use std::fs;
#[cfg(test)]
use std::fs::File;
use std::path::{Path, PathBuf};
use std::time::UNIX_EPOCH;

use serde::Deserialize;

use crate::log::Summary;
use crate::model::{Agent, AgentState, Limits, RequestTag, Session};
use crate::repo::Repos;
use crate::state::{agent_state, standing};
use crate::tail::{feed_lines, first_ms};

const ONE_HOUR_MS: u64 = 3_600_000;
const FIVE_MINUTES_MS: u64 = 300_000;
/// Subagents whose log has not changed for this long are left out.
const AGENT_MAX_AGE_MS: u64 = 2 * ONE_HOUR_MS;
const MAX_AGENTS: usize = 50;

/// The newest `limits.max_sessions` logs modified within `limits.max_age_ms` of `now_ms`,
/// newest first. Each log is read from its last `limits.tail_bytes` bytes.
pub fn scan(claude_dir: &Path, now_ms: u64, limits: Limits) -> Vec<Session> {
    SessionScanner::default().scan(claude_dir, now_ms, limits)
}

/// `scan` that keeps a cursor per log between calls. A log whose modified time and length are
/// unchanged is not read again, a grown log is read from where the last call stopped, and a log
/// that became shorter is read afresh from its last `tail_bytes`. A log is parsed from its tail
/// at first sight and every line after it, so its summary is the parse of all those lines.
#[derive(Default)]
pub struct SessionScanner {
    cursors: HashMap<PathBuf, Cursor>,
    bytes_read: u64,
}

/// How far one log was read, and what its lines said.
struct Cursor {
    modified_ms: u64,
    len: u64,
    /// The end of the last whole line read.
    offset: u64,
    /// The first line read starts before `offset`, cut by the tail cap, and is still unread.
    cut: bool,
    /// When the log starts: from the summary when it was read from byte 0, else from its head.
    head_ms: Option<u64>,
    from_start: bool,
    summary: Summary,
}

impl Cursor {
    fn started_ms(&self) -> Option<u64> {
        if self.from_start {
            self.summary.first_ms
        } else {
            self.head_ms
        }
    }
}

/// Request entries a cursor's summary keeps, newest last: the router joins only recent ones.
const MAX_REQUESTS: usize = 500;

impl SessionScanner {
    /// Every byte read from a log so far, counting a partial last line each time it is read.
    pub fn bytes_read(&self) -> u64 {
        self.bytes_read
    }

    /// The sessions `scan` lists, reading only the bytes that changed since the last call.
    pub fn scan(&mut self, claude_dir: &Path, now_ms: u64, limits: Limits) -> Vec<Session> {
        let mut repos = Repos::default();
        let mut logs = recent_logs(&claude_dir.join("projects"), now_ms, limits.max_age_ms);
        logs.sort_by(|a, b| {
            b.modified_ms
                .cmp(&a.modified_ms)
                .then_with(|| a.id.cmp(&b.id))
        });
        logs.truncate(limits.max_sessions);
        let mut earlier = std::mem::take(&mut self.cursors);
        logs.iter()
            .filter_map(|log| {
                self.session(&mut earlier, log, now_ms, limits.tail_bytes, &mut repos)
            })
            .collect()
    }

    /// The log's cursor brought up to date. Cursors not asked for in a scan are dropped with
    /// `earlier`.
    fn read(
        &mut self,
        earlier: &mut HashMap<PathBuf, Cursor>,
        path: &Path,
        modified_ms: u64,
        tail_bytes: u64,
    ) -> Option<&Cursor> {
        let len = fs::metadata(path).ok()?.len();
        let mut cursor = match earlier.remove(path) {
            Some(cursor) if cursor.len <= len => cursor,
            _ => self.first_sight(path, len, tail_bytes),
        };
        if cursor.len != len || cursor.modified_ms != modified_ms {
            let fed = feed_lines(path, cursor.offset, &mut cursor.cut, &mut cursor.summary)?;
            self.bytes_read += fed.bytes;
            cursor.offset = fed.end;
            cursor.len = len;
            cursor.modified_ms = modified_ms;
            let requests = &mut cursor.summary.requests;
            requests.drain(..requests.len().saturating_sub(MAX_REQUESTS));
        }
        Some(
            self.cursors
                .entry(path.to_path_buf())
                .insert_entry(cursor)
                .into_mut(),
        )
    }

    /// A cursor at the start of the log's last `tail_bytes`, not yet read. A log longer than
    /// that has its start time read from its head now, once.
    fn first_sight(&mut self, path: &Path, len: u64, tail_bytes: u64) -> Cursor {
        let offset = len.saturating_sub(tail_bytes);
        let head_ms = if offset > 0 {
            let (at, bytes) = first_ms(path);
            self.bytes_read += bytes;
            at
        } else {
            None
        };
        Cursor {
            modified_ms: 0,
            len: 0,
            offset,
            cut: offset > 0,
            head_ms,
            from_start: offset == 0,
            summary: Summary::default(),
        }
    }

    fn session(
        &mut self,
        earlier: &mut HashMap<PathBuf, Cursor>,
        log: &Log,
        now_ms: u64,
        tail_bytes: u64,
        repos: &mut Repos,
    ) -> Option<Session> {
        let subagents = self.subagents(
            earlier,
            &log.path.with_extension("").join("subagents"),
            now_ms,
            tail_bytes,
        );
        let cursor = self.read(earlier, &log.path, log.modified_ms, tail_bytes)?;
        let main = &cursor.summary;
        let started_ms = cursor.started_ms();
        let repo = main.cwd.as_deref().and_then(|cwd| repos.of(cwd));
        let standing = standing(main, false, now_ms);
        let state = standing.state;
        let model = main.model.clone();
        let context_tokens = main
            .last_usage
            .map(|u| u.input + u.cache_read + u.cache_write);
        let one_million = [&main.model, &main.requested_model]
            .into_iter()
            .flatten()
            .any(|m| m.ends_with("[1m]"));
        // The 1M window is not always named in the model id; a context past 200k proves it.
        let context_limit = if one_million || context_tokens.is_some_and(|t| t > 200_000) {
            1_000_000
        } else {
            200_000
        };
        let mut request_ids = main.tags("main");
        let mut agents = vec![Agent {
            id: "main".to_string(),
            kind: "main".to_string(),
            description: String::new(),
            model: model.clone(),
            state: agent_state(state),
            started_ms,
            ended_ms: None,
            worktree: None,
        }];
        for (agent, tags) in subagents {
            agents.push(agent);
            request_ids.extend(tags);
        }
        Some(Session {
            id: log.id.clone(),
            title: main
                .custom_title
                .clone()
                .or_else(|| main.ai_title.clone())
                .unwrap_or_default(),
            project: repo.as_ref().map(|r| r.name.clone()),
            repo: repo.as_ref().map(|r| r.key.clone()),
            worktree: repo.map(|r| r.worktree),
            cwd: main.cwd.clone(),
            branch: main.branch.clone(),
            model,
            state,
            started_ms,
            last_ms: main.last_ms.or(Some(log.modified_ms)),
            state_since_ms: standing.since_ms,
            open_tasks: standing.open_tasks,
            question_tool: standing.question_tool,
            context_tokens,
            context_limit,
            cache_ttl_ms: if main.one_hour_cache {
                ONE_HOUR_MS
            } else {
                FIVE_MINUTES_MS
            },
            last_request_ms: main.last_assistant_ms,
            agents,
            request_ids,
        })
    }

    /// The newest `MAX_AGENTS` subagents of one session changed within `AGENT_MAX_AGE_MS`,
    /// ordered by file name, each with its request tags.
    fn subagents(
        &mut self,
        earlier: &mut HashMap<PathBuf, Cursor>,
        dir: &Path,
        now_ms: u64,
        tail_bytes: u64,
    ) -> Vec<(Agent, Vec<RequestTag>)> {
        let Ok(entries) = fs::read_dir(dir) else {
            return Vec::new();
        };
        let mut logs: Vec<(u64, PathBuf)> = entries
            .flatten()
            .map(|e| e.path())
            .filter(|p| p.extension().is_some_and(|x| x == "jsonl"))
            .filter_map(|p| Some((modified_ms(&p)?, p)))
            .filter(|(at, _)| now_ms.saturating_sub(*at) <= AGENT_MAX_AGE_MS)
            .collect();
        logs.sort_by(|a, b| b.0.cmp(&a.0).then_with(|| a.1.cmp(&b.1)));
        logs.truncate(MAX_AGENTS);
        logs.sort_by(|a, b| a.1.cmp(&b.1));
        logs.iter()
            .filter_map(|(at, path)| self.subagent(earlier, path, *at, now_ms, tail_bytes))
            .collect()
    }

    fn subagent(
        &mut self,
        earlier: &mut HashMap<PathBuf, Cursor>,
        path: &Path,
        modified_ms: u64,
        now_ms: u64,
        tail_bytes: u64,
    ) -> Option<(Agent, Vec<RequestTag>)> {
        let id = path.file_stem()?.to_str()?.to_string();
        let meta: Meta = fs::read_to_string(path.with_extension("meta.json"))
            .ok()
            .and_then(|text| serde_json::from_str(&text).ok())
            .unwrap_or_default();
        let cursor = self.read(earlier, path, modified_ms, tail_bytes)?;
        let tail = &cursor.summary;
        let state = standing(tail, true, now_ms).state;
        let (state, ended_ms) = if meta.finished {
            (AgentState::Done, tail.last_ms)
        } else {
            (agent_state(state), None)
        };
        let tags = tail.tags(&id);
        let agent = Agent {
            kind: meta.agent_type.unwrap_or_default(),
            description: meta.description.unwrap_or_default(),
            model: tail.model.clone().or(meta.model),
            id,
            state,
            started_ms: cursor.started_ms(),
            ended_ms,
            worktree: None,
        };
        Some((agent, tags))
    }
}

struct Log {
    id: String,
    path: PathBuf,
    modified_ms: u64,
}

fn recent_logs(projects: &Path, now_ms: u64, max_age_ms: u64) -> Vec<Log> {
    let mut logs = Vec::new();
    let Ok(projects) = fs::read_dir(projects) else {
        return logs;
    };
    for project in projects.flatten() {
        let Ok(files) = fs::read_dir(project.path()) else {
            continue;
        };
        for file in files.flatten() {
            let path = file.path();
            let Some(id) = path
                .file_name()
                .and_then(|n| n.to_str())
                .and_then(|n| n.strip_suffix(".jsonl"))
            else {
                continue;
            };
            let Some(modified_ms) = modified_ms(&path) else {
                continue;
            };
            if now_ms.saturating_sub(modified_ms) <= max_age_ms {
                logs.push(Log {
                    id: id.to_string(),
                    path,
                    modified_ms,
                });
            }
        }
    }
    logs
}

pub(crate) fn modified_ms(path: &Path) -> Option<u64> {
    let modified = fs::metadata(path).ok()?.modified().ok()?;
    let since_epoch = modified.duration_since(UNIX_EPOCH).ok()?;
    u64::try_from(since_epoch.as_millis()).ok()
}

#[derive(Deserialize, Default)]
struct Meta {
    #[serde(rename = "agentType")]
    agent_type: Option<String>,
    description: Option<String>,
    model: Option<String>,
    #[serde(default)]
    finished: bool,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    fn line(at: &str, request: &str) -> String {
        format!(
            r#"{{"type":"assistant","timestamp":"{at}","cwd":"/x","requestId":"{request}","message":{{"model":"m","stop_reason":"tool_use","usage":{{"input_tokens":1}}}}}}"#
        ) + "\n"
    }

    #[test]
    fn l23_incremental_summary_equals_a_full_parse() {
        let home = huddle_testkit::temp_home::TempHome::new();
        let path = home.root().join("s.jsonl");
        fs::write(&path, line("2026-11-11T11:00:00.000Z", "r1")).expect("write");
        let mut scanner = SessionScanner::default();
        let mut earlier = HashMap::new();
        scanner.read(&mut earlier, &path, 1, 512 * 1024);
        let mut file = File::options().append(true).open(&path).expect("open");
        file.write_all(line("2026-11-11T11:01:00.000Z", "r2").as_bytes())
            .expect("append");
        file.write_all(b"{\"type\":\"user\",\"interruptedMessageId\":\"x\"}\n")
            .expect("append");
        let mut earlier = std::mem::take(&mut scanner.cursors);
        let cursor = scanner
            .read(&mut earlier, &path, 2, 512 * 1024)
            .expect("read");
        let whole = fs::read_to_string(&path).expect("read whole");
        assert_eq!(
            format!("{:?}", cursor.summary),
            format!("{:?}", Summary::parse(&whole))
        );
    }
}
