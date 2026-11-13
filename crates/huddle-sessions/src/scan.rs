//! Finds the recent session logs under `<claude_dir>/projects` and assembles each `Session`.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::UNIX_EPOCH;

use serde::Deserialize;

use crate::log::Summary;
use crate::model::{Agent, AgentState, Limits, RequestTag, Session};
use crate::repo::Repos;
use crate::state::{agent_state, standing};
use crate::tail::{read_head, read_tail};

const ONE_HOUR_MS: u64 = 3_600_000;
const FIVE_MINUTES_MS: u64 = 300_000;
/// Subagents whose log has not changed for this long are left out.
const AGENT_MAX_AGE_MS: u64 = 2 * ONE_HOUR_MS;
const MAX_AGENTS: usize = 50;

/// The newest `limits.max_sessions` logs modified within `limits.max_age_ms` of `now_ms`,
/// newest first. Each log is read from its last `limits.tail_bytes` bytes.
pub fn scan(claude_dir: &Path, now_ms: u64, limits: Limits) -> Vec<Session> {
    let mut repos = Repos::default();
    let mut logs = recent_logs(&claude_dir.join("projects"), now_ms, limits.max_age_ms);
    logs.sort_by(|a, b| {
        b.modified_ms
            .cmp(&a.modified_ms)
            .then_with(|| a.id.cmp(&b.id))
    });
    logs.truncate(limits.max_sessions);
    logs.iter()
        .filter_map(|log| session(log, now_ms, limits.tail_bytes, &mut repos))
        .collect()
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

fn session(log: &Log, now_ms: u64, tail_bytes: u64, repos: &mut Repos) -> Option<Session> {
    let main = Summary::parse(&read_tail(&log.path, tail_bytes)?);
    let repo = main.cwd.as_deref().and_then(|cwd| repos.of(cwd));
    let started_ms = read_head(&log.path).and_then(|head| Summary::parse(&head).first_ms);
    let standing = standing(&main, false, now_ms);
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
    let subagents = subagents(
        &log.path.with_extension("").join("subagents"),
        now_ms,
        tail_bytes,
    );
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
        title: main.custom_title.or(main.ai_title).unwrap_or_default(),
        project: repo.as_ref().map(|r| r.name.clone()),
        repo: repo.as_ref().map(|r| r.key.clone()),
        worktree: repo.map(|r| r.worktree),
        cwd: main.cwd,
        branch: main.branch,
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

#[derive(Deserialize, Default)]
struct Meta {
    #[serde(rename = "agentType")]
    agent_type: Option<String>,
    description: Option<String>,
    model: Option<String>,
    #[serde(default)]
    finished: bool,
}

/// The newest `MAX_AGENTS` subagents of one session changed within `AGENT_MAX_AGE_MS`,
/// ordered by file name, each with its request tags.
fn subagents(dir: &Path, now_ms: u64, tail_bytes: u64) -> Vec<(Agent, Vec<RequestTag>)> {
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
    let mut logs: Vec<PathBuf> = logs.into_iter().map(|(_, p)| p).collect();
    logs.sort();
    logs.iter()
        .filter_map(|path| subagent(path, now_ms, tail_bytes))
        .collect()
}

fn subagent(path: &Path, now_ms: u64, tail_bytes: u64) -> Option<(Agent, Vec<RequestTag>)> {
    let id = path.file_stem()?.to_str()?.to_string();
    let meta: Meta = fs::read_to_string(path.with_extension("meta.json"))
        .ok()
        .and_then(|text| serde_json::from_str(&text).ok())
        .unwrap_or_default();
    let tail = Summary::parse(&read_tail(path, tail_bytes)?);
    let started_ms = read_head(path).and_then(|head| Summary::parse(&head).first_ms);
    let state = standing(&tail, true, now_ms).state;
    let (state, ended_ms) = if meta.finished {
        (AgentState::Done, tail.last_ms)
    } else {
        (agent_state(state), None)
    };
    let tags = tail.tags(&id);
    let agent = Agent {
        kind: meta.agent_type.unwrap_or_default(),
        description: meta.description.unwrap_or_default(),
        model: tail.model.or(meta.model),
        id,
        state,
        started_ms,
        ended_ms,
        worktree: None,
    };
    Some((agent, tags))
}
