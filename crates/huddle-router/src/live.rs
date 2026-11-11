//! huddle-router: live state. The dashboard's counters, request ring and in-flight set, held in memory.
//! The core takes every timestamp as a parameter and does no I/O.

use std::collections::{HashMap, VecDeque};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};

use huddle_sessions::{RequestTag, Session, Worktree};
use tokio::sync::watch;

use crate::answer_facts::PlanUsage;
use crate::sse::Usage;
use crate::stats::{self, Buckets, MAX_MODELS, ModelRow, ModelSeries, Sample, percentile};

/// Finished requests the ring keeps, newest first.
const RING_CAPACITY: usize = 200;
/// Time-to-first-byte samples kept per lane for the percentiles.
const TTFT_SAMPLES: usize = 500;
/// Seconds the requests-per-second series covers.
const SECONDS: usize = 60;
/// The window `errors_5m` counts over.
const ERROR_WINDOW_MS: u64 = 5 * 60 * 1000;

/// What the caller knows when a model request arrives.
#[derive(Debug, Clone)]
pub struct RequestStart {
    /// The `x-claude-code-session-id` header value, when the request has one.
    pub session: Option<String>,
    pub model: String,
    pub lane: String,
    pub now_ms: u64,
}

/// How a request ended.
#[derive(Debug, Clone, Default)]
pub struct Finish {
    pub status: u16,
    /// One line on why the request failed, when it did; decides `timeout` and `refused`.
    pub error: Option<String>,
    pub usage: Option<Usage>,
    /// The upstream's `request-id`, on the Anthropic lane.
    pub upstream_id: Option<String>,
    /// Plan usage the answer's headers carried.
    pub plan: Option<PlanUsage>,
    /// Milliseconds the router held the request before the first attempt left.
    pub router_ms: Option<u64>,
    pub now_ms: u64,
}

/// One lane as the caller describes it; the counters come from the live state.
#[derive(Debug, Clone)]
pub struct LaneMeta {
    pub id: String,
    pub name: String,
    pub enabled: bool,
    /// The model pattern the lane claims, such as `kimi-*`.
    pub models: String,
    /// The command that sets the lane up, shown for a lane that is off.
    pub setup: Option<String>,
    /// Whether the keystore holds a key; `None` for Anthropic.
    pub key: Option<bool>,
}

/// Whether Claude Code points at this router.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, serde::Serialize)]
pub struct Wiring {
    pub proxy: bool,
    pub ca: bool,
}

/// One delegated job, as the dashboard lists it.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct JobRow {
    pub id: String,
    pub harness: String,
    pub repo: String,
    pub title: String,
    pub state: String,
    pub started_ms: u64,
    pub ended_ms: Option<u64>,
    pub files: Option<u32>,
    pub add: Option<u64>,
    pub del: Option<u64>,
    pub why: Option<String>,
}

/// The wiring card's state: `Checking` until the first read lands, then `Ok` when Claude Code
/// points at the router with a CA that exists, else `Bad`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum WiringState {
    #[default]
    Checking,
    Ok,
    Bad,
}

/// The wiring card as the page reads it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
pub struct WiringCard {
    pub state: WiringState,
    pub proxy: bool,
    pub ca: bool,
}

/// The router process's own load, as `ps` reports it; `None` before the first sample.
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct ProcessUsage {
    pub cpu_pct: Option<f64>,
    pub rss_bytes: Option<u64>,
}

/// What the live state cannot know: the process, its wiring, the lane list, the jobs and what
/// the daemon read from the machine. The plugin and harness pages arrive as built JSON: the
/// daemon owns their shape and the router only carries them.
#[derive(Debug, Clone, Default)]
pub struct SnapshotInputs {
    pub version: String,
    pub pid: u32,
    pub wiring: Wiring,
    pub wiring_state: WiringState,
    pub lanes: Vec<LaneMeta>,
    pub jobs: Vec<JobRow>,
    /// Open connections to the router.
    pub connections: usize,
    pub process: ProcessUsage,
    pub sessions: Vec<Session>,
    pub worktrees: Vec<Worktree>,
    pub plugins: serde_json::Value,
    pub harnesses: serde_json::Value,
    pub hosts: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct LastError {
    pub at_ms: u64,
    pub lane: String,
    pub model: String,
    pub status: u16,
    pub reason: Option<String>,
}

#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct LaneRow {
    pub id: String,
    pub name: String,
    pub enabled: bool,
    pub models: String,
    pub setup: Option<String>,
    pub key: Option<bool>,
    pub requests: u64,
    pub errors: u64,
    pub ttft_p50_ms: Option<u64>,
    pub ttft_p95_ms: Option<u64>,
    /// The same value as `ttft_p50_ms`.
    pub ttft_p50: Option<u64>,
    pub last: Option<LaneLast>,
    pub tokens_in: u64,
    pub tokens_out: u64,
}

/// The status of a lane's latest answer and when it ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
pub struct LaneLast {
    pub status: u16,
    pub at_ms: u64,
}

/// The router card's counters.
#[derive(Debug, Clone, Copy, PartialEq, serde::Serialize)]
pub struct RouterStats {
    /// Dashboard live feeds open now.
    pub viewers: usize,
    pub connections: usize,
    pub cpu_pct: Option<f64>,
    pub rss_bytes: Option<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct RecentRow {
    pub id: u64,
    pub at_ms: u64,
    pub session: Option<String>,
    pub model: String,
    pub lane: String,
    pub status: u16,
    pub reason: Option<String>,
    pub ttft_ms: Option<u64>,
    pub tokens_in: Option<u64>,
    pub tokens_out: Option<u64>,
    /// The agent the request is credited to; filled from the session logs.
    pub agent: Option<String>,
    pub dur_ms: u64,
    pub router_ms: Option<u64>,
    pub cache_read: Option<u64>,
    pub cache_write: Option<u64>,
    pub stop: Option<String>,
    /// The upstream's `request-id`, the key that joins a request to a session log line.
    pub upstream_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct InFlightRow {
    pub id: u64,
    pub started_ms: u64,
    pub last_byte_ms: u64,
    pub session: Option<String>,
    pub model: String,
    pub lane: String,
    pub agent: Option<String>,
}

/// The dashboard's JSON: `GET /huddle-router/state` and each `snapshot` event.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Snapshot {
    pub version: String,
    pub pid: u32,
    pub started_ms: u64,
    pub now_ms: u64,
    pub wiring: WiringCard,
    pub router: RouterStats,
    /// Plan usage from the latest Anthropic answer that carried it.
    pub plan: Option<PlanUsage>,
    pub buckets: Buckets,
    /// Per-model rows over the last 15 minutes.
    pub models: Vec<ModelRow>,
    /// Requests started in each of the last 60 whole seconds, oldest first.
    pub per_second: Vec<u32>,
    pub errors_5m: u32,
    pub last_error: Option<LastError>,
    pub lanes: Vec<LaneRow>,
    pub recent: Vec<RecentRow>,
    pub in_flight: Vec<InFlightRow>,
    pub jobs: Vec<JobRow>,
    pub sessions: Vec<Session>,
    pub worktrees: Vec<Worktree>,
    pub plugins: serde_json::Value,
    pub harnesses: serde_json::Value,
    pub hosts: Vec<String>,
}

struct Active {
    started_ms: u64,
    first_byte_ms: Option<u64>,
    last_byte_ms: u64,
    session: Option<String>,
    model: String,
    lane: String,
}

#[derive(Default)]
struct LaneStats {
    requests: u64,
    errors: u64,
    tokens_in: u64,
    tokens_out: u64,
    ttfts: VecDeque<u64>,
    last: Option<LaneLast>,
}

struct Inner {
    next_id: u64,
    active: HashMap<u64, Active>,
    recent: VecDeque<RecentRow>,
    lanes: HashMap<String, LaneStats>,
    models: HashMap<String, ModelSeries>,
    plan: Option<PlanUsage>,
    /// `(second, count)` slots indexed by `second % 60`; a slot counts only while its second matches.
    buckets: [(u64, u32); SECONDS],
    error_times: VecDeque<u64>,
    last_error: Option<LastError>,
}

/// Counters, the last 200 requests and the in-flight set. One mutex, held only inside a call.
pub struct LiveState {
    started_ms: u64,
    inner: Mutex<Inner>,
    changes: watch::Sender<u64>,
    viewers: AtomicUsize,
}

impl LiveState {
    pub fn new(started_ms: u64) -> LiveState {
        LiveState {
            started_ms,
            inner: Mutex::new(Inner {
                next_id: 1,
                active: HashMap::new(),
                recent: VecDeque::new(),
                lanes: HashMap::new(),
                models: HashMap::new(),
                plan: None,
                buckets: [(0, 0); SECONDS],
                error_times: VecDeque::new(),
                last_error: None,
            }),
            changes: watch::channel(0).0,
            viewers: AtomicUsize::new(0),
        }
    }

    /// Counts one open dashboard feed until the guard drops.
    /// Dashboard live feeds open now.
    pub fn viewers(&self) -> usize {
        self.viewers.load(Ordering::Relaxed)
    }

    pub fn viewer(self: &Arc<Self>) -> Viewer {
        self.viewers.fetch_add(1, Ordering::Relaxed);
        Viewer {
            live: Arc::clone(self),
        }
    }

    /// A counter that moves on every `start` and `finish`, never on `byte`.
    pub fn subscribe(&self) -> watch::Receiver<u64> {
        self.changes.subscribe()
    }

    /// Registers a request and returns its id.
    pub fn start(&self, request: RequestStart) -> u64 {
        let id = {
            let mut inner = lock(&self.inner);
            let id = inner.next_id;
            inner.next_id += 1;
            let second = request.now_ms / 1000;
            let slot = &mut inner.buckets[(second % SECONDS as u64) as usize];
            if slot.0 == second {
                slot.1 += 1;
            } else {
                *slot = (second, 1);
            }
            inner.active.insert(
                id,
                Active {
                    started_ms: request.now_ms,
                    first_byte_ms: None,
                    last_byte_ms: request.now_ms,
                    session: request.session,
                    model: request.model,
                    lane: request.lane,
                },
            );
            id
        };
        self.bump();
        id
    }

    /// The first response byte: sets the request's time to first byte and its last byte time.
    pub fn first_byte(&self, id: u64, now_ms: u64) {
        let mut inner = lock(&self.inner);
        let Some(active) = inner.active.get_mut(&id) else {
            return;
        };
        if active.first_byte_ms.is_some() {
            active.last_byte_ms = now_ms;
            return;
        }
        active.first_byte_ms = Some(now_ms);
        active.last_byte_ms = now_ms;
        let ttft = now_ms.saturating_sub(active.started_ms);
        let lane = active.lane.clone();
        let ttfts = &mut inner.lanes.entry(lane).or_default().ttfts;
        ttfts.push_back(ttft);
        if ttfts.len() > TTFT_SAMPLES {
            ttfts.pop_front();
        }
    }

    /// Any later byte: only the last byte time moves, and no change is announced.
    pub fn byte(&self, id: u64, now_ms: u64) {
        if let Some(active) = lock(&self.inner).active.get_mut(&id) {
            active.last_byte_ms = now_ms;
        }
    }

    /// Ends a request: it leaves the in-flight set and enters the ring and the lane counters.
    pub fn finish(&self, id: u64, finish: Finish) {
        let changed = {
            let mut inner = lock(&self.inner);
            match inner.active.remove(&id) {
                Some(active) => {
                    record(&mut inner, id, active, finish);
                    true
                }
                None => false,
            }
        };
        if changed {
            self.bump();
        }
    }

    /// Credits each finished request a session log names by its `request-id`: the agent, and
    /// the usage and stop reason the router could not read, which then count in the charts,
    /// the model rows and the lane totals. The router's own reading wins; a request is counted
    /// once.
    pub fn credit(&self, sessions: &[Session]) {
        let tags: HashMap<&str, (&str, &RequestTag)> = sessions
            .iter()
            .flat_map(|session| {
                session
                    .request_ids
                    .iter()
                    .map(move |tag| (tag.request_id.as_str(), (session.id.as_str(), tag)))
            })
            .collect();
        if tags.is_empty() {
            return;
        }
        let mut inner = lock(&self.inner);
        let Inner {
            recent,
            models,
            lanes,
            ..
        } = &mut *inner;
        for row in recent.iter_mut() {
            let Some((session, tag)) = row.upstream_id.as_deref().and_then(|id| tags.get(id))
            else {
                continue;
            };
            if row.agent.is_none() {
                row.agent = Some(format!("{session}:{}", tag.agent_id));
            }
            let usage = tag.usage;
            row.tokens_in = row
                .tokens_in
                .or(Some(usage.input + usage.cache_read + usage.cache_write));
            row.cache_read = row.cache_read.or(Some(usage.cache_read));
            row.cache_write = row.cache_write.or(Some(usage.cache_write));
            row.tokens_out = row.tokens_out.or(Some(usage.output));
            if row.stop.is_none() {
                row.stop.clone_from(&tag.stop);
            }
        }
        for series in models.values_mut() {
            for sample in series.samples.iter_mut().filter(|s| !s.usage_known) {
                let Some((_, tag)) = sample.upstream_id.as_deref().and_then(|id| tags.get(id))
                else {
                    continue;
                };
                let usage = tag.usage;
                sample.tokens_in = usage.input + usage.cache_read + usage.cache_write;
                sample.cache_read = usage.cache_read;
                sample.cache_write = usage.cache_write;
                sample.tokens_out = usage.output;
                sample.usage_known = true;
                let lane = lanes.entry(series.lane.clone()).or_default();
                lane.tokens_in += sample.tokens_in;
                lane.tokens_out += sample.tokens_out;
            }
        }
    }

    pub fn snapshot(&self, now_ms: u64, inputs: &SnapshotInputs) -> Snapshot {
        let mut inner = lock(&self.inner);
        let cutoff = now_ms.saturating_sub(ERROR_WINDOW_MS);
        while inner.error_times.front().is_some_and(|at| *at <= cutoff) {
            inner.error_times.pop_front();
        }
        let now_second = now_ms / 1000;
        let per_second = (0..SECONDS as u64)
            .map(|back| {
                let Some(second) = (now_second + back).checked_sub(SECONDS as u64) else {
                    return 0;
                };
                let slot = inner.buckets[(second % SECONDS as u64) as usize];
                if slot.0 == second { slot.1 } else { 0 }
            })
            .collect();
        let mut in_flight: Vec<InFlightRow> = inner
            .active
            .iter()
            .map(|(id, active)| InFlightRow {
                id: *id,
                started_ms: active.started_ms,
                last_byte_ms: active.last_byte_ms,
                session: active.session.clone(),
                model: active.model.clone(),
                lane: active.lane.clone(),
                agent: None,
            })
            .collect();
        in_flight.sort_by_key(|row| row.id);
        let lanes = inputs
            .lanes
            .iter()
            .map(|meta| lane_row(meta, inner.lanes.get(&meta.id)))
            .collect();
        for series in inner.models.values_mut() {
            series.prune(now_ms);
        }
        inner.models.retain(|_, series| !series.is_empty());
        Snapshot {
            version: inputs.version.clone(),
            pid: inputs.pid,
            started_ms: self.started_ms,
            now_ms,
            wiring: WiringCard {
                state: inputs.wiring_state,
                proxy: inputs.wiring.proxy,
                ca: inputs.wiring.ca,
            },
            router: RouterStats {
                viewers: self.viewers.load(Ordering::Relaxed),
                connections: inputs.connections,
                cpu_pct: inputs.process.cpu_pct,
                rss_bytes: inputs.process.rss_bytes,
            },
            plan: inner.plan,
            buckets: stats::buckets(&inner.models, now_ms),
            models: stats::model_rows(&inner.models, now_ms),
            per_second,
            errors_5m: inner.error_times.len() as u32,
            last_error: inner.last_error.clone(),
            lanes,
            recent: inner.recent.iter().cloned().collect(),
            in_flight,
            jobs: inputs.jobs.clone(),
            sessions: inputs.sessions.clone(),
            worktrees: inputs.worktrees.clone(),
            plugins: inputs.plugins.clone(),
            harnesses: inputs.harnesses.clone(),
            hosts: inputs.hosts.clone(),
        }
    }

    fn bump(&self) {
        self.changes.send_modify(|count| *count += 1);
    }
}

fn record(inner: &mut Inner, id: u64, active: Active, finish: Finish) {
    let failed = is_error(finish.status);
    let reason = reason(finish.status, finish.error.as_deref());
    let tokens_in = finish.usage.as_ref().map(prompt_tokens);
    let tokens_out = finish
        .usage
        .as_ref()
        .and_then(|usage| usage.output_tokens)
        .map(non_negative);
    let cache_read = finish
        .usage
        .as_ref()
        .and_then(|usage| usage.cache_read_input_tokens)
        .map(non_negative);
    let cache_write = finish
        .usage
        .as_ref()
        .and_then(|usage| usage.cache_creation_input_tokens)
        .map(non_negative);
    let stop = finish
        .usage
        .as_ref()
        .and_then(|usage| usage.stop_reason.clone());
    let ttft_ms = active
        .first_byte_ms
        .map(|at| at.saturating_sub(active.started_ms));
    let dur_ms = finish.now_ms.saturating_sub(active.started_ms);
    if finish.plan.is_some() {
        inner.plan = finish.plan;
    }
    let session = active.session;
    if inner.models.contains_key(&active.model) || inner.models.len() < MAX_MODELS {
        let series = inner.models.entry(active.model.clone()).or_default();
        series.push(
            &active.lane,
            Sample {
                at_ms: finish.now_ms,
                failed,
                ttft_ms,
                dur_ms,
                tokens_in: tokens_in.unwrap_or(0),
                cache_read: cache_read.unwrap_or(0),
                cache_write: cache_write.unwrap_or(0),
                tokens_out: tokens_out.unwrap_or(0),
                session: session.clone(),
                upstream_id: finish.upstream_id.clone(),
                usage_known: finish.usage.is_some(),
            },
        );
        series.prune(finish.now_ms);
    }
    let stats = inner.lanes.entry(active.lane.clone()).or_default();
    stats.last = Some(LaneLast {
        status: finish.status,
        at_ms: finish.now_ms,
    });
    stats.requests += 1;
    stats.tokens_in += tokens_in.unwrap_or(0);
    stats.tokens_out += tokens_out.unwrap_or(0);
    if failed {
        stats.errors += 1;
        inner.error_times.push_back(finish.now_ms);
        inner.last_error = Some(LastError {
            at_ms: finish.now_ms,
            lane: active.lane.clone(),
            model: active.model.clone(),
            status: finish.status,
            reason: reason.clone(),
        });
    }
    inner.recent.push_front(RecentRow {
        id,
        at_ms: finish.now_ms,
        session,
        model: active.model,
        lane: active.lane,
        status: finish.status,
        reason,
        ttft_ms,
        tokens_in,
        tokens_out,
        agent: None,
        dur_ms,
        router_ms: finish.router_ms,
        cache_read,
        cache_write,
        stop,
        upstream_id: finish.upstream_id,
    });
    inner.recent.truncate(RING_CAPACITY);
}

/// The status the router records when the client left before the answer.
const CLIENT_LEFT: u16 = 499;

/// A failure on the lane's side. Status 0 stands for a request that never got an answer;
/// a client that left is not a lane problem.
fn is_error(status: u16) -> bool {
    status != CLIENT_LEFT && (status == 0 || status >= 400)
}

fn reason(status: u16, error: Option<&str>) -> Option<String> {
    if !is_error(status) && status != CLIENT_LEFT {
        return None;
    }
    let text = error.map(str::to_lowercase).unwrap_or_default();
    let word = if text.contains("timeout") || text.contains("timed out") {
        "timeout"
    } else if text.contains("refused") {
        "refused"
    } else {
        match status {
            429 => "quota",
            529 => "overloaded",
            CLIENT_LEFT => "client left",
            _ => "upstream",
        }
    };
    Some(word.to_string())
}

/// Every token the prompt cost, cache reads and writes included.
fn prompt_tokens(usage: &Usage) -> u64 {
    [
        usage.input_tokens,
        usage.cache_creation_input_tokens,
        usage.cache_read_input_tokens,
    ]
    .into_iter()
    .flatten()
    .map(non_negative)
    .sum()
}

fn non_negative(tokens: i64) -> u64 {
    tokens.max(0) as u64
}

fn lane_row(meta: &LaneMeta, stats: Option<&LaneStats>) -> LaneRow {
    let empty = LaneStats::default();
    let stats = stats.unwrap_or(&empty);
    let mut sorted: Vec<u64> = stats.ttfts.iter().copied().collect();
    sorted.sort_unstable();
    LaneRow {
        id: meta.id.clone(),
        name: meta.name.clone(),
        enabled: meta.enabled,
        models: meta.models.clone(),
        setup: meta.setup.clone(),
        key: meta.key,
        requests: stats.requests,
        errors: stats.errors,
        ttft_p50_ms: percentile(&sorted, 50),
        ttft_p95_ms: percentile(&sorted, 95),
        ttft_p50: percentile(&sorted, 50),
        last: stats.last,
        tokens_in: stats.tokens_in,
        tokens_out: stats.tokens_out,
    }
}

/// One open dashboard feed; dropping it takes the feed from the viewer count.
pub struct Viewer {
    live: Arc<LiveState>,
}

impl Drop for Viewer {
    fn drop(&mut self) {
        self.live.viewers.fetch_sub(1, Ordering::Relaxed);
    }
}

/// A lock's guard, past a poisoned lock: the data is counters, sane to keep using.
fn lock<T>(value: &Mutex<T>) -> MutexGuard<'_, T> {
    value.lock().unwrap_or_else(PoisonError::into_inner)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    const T0: u64 = 1_794_300_000_000;

    fn begin(live: &LiveState, lane: &str, model: &str, now_ms: u64) -> u64 {
        live.start(RequestStart {
            session: Some("cse_01JqXrugEVi7".into()),
            model: model.into(),
            lane: lane.into(),
            now_ms,
        })
    }

    fn end(status: u16, error: Option<&str>, now_ms: u64) -> Finish {
        Finish {
            status,
            error: error.map(str::to_string),
            now_ms,
            ..Finish::default()
        }
    }

    fn meta(id: &str) -> LaneMeta {
        LaneMeta {
            id: id.into(),
            name: id.into(),
            enabled: true,
            models: format!("{id}-*"),
            setup: None,
            key: None,
        }
    }

    fn inputs(lanes: &[&str]) -> SnapshotInputs {
        SnapshotInputs {
            version: "0.0.2".into(),
            pid: 7,
            wiring: Wiring {
                proxy: true,
                ca: false,
            },
            wiring_state: WiringState::Bad,
            lanes: lanes.iter().map(|id| meta(id)).collect(),
            jobs: Vec::new(),
            connections: 3,
            process: ProcessUsage {
                cpu_pct: Some(0.4),
                rss_bytes: Some(19_300_000),
            },
            ..SnapshotInputs::default()
        }
    }

    fn usage(input: i64, output: i64) -> Usage {
        Usage {
            input_tokens: Some(input),
            output_tokens: Some(output),
            ..Usage::default()
        }
    }

    #[test]
    fn a_request_runs_from_start_to_finish() {
        let live = LiveState::new(T0);
        let id = begin(&live, "zai", "glm-5.3", T0 + 1000);
        let during = live.snapshot(T0 + 2000, &inputs(&["zai"]));
        assert_eq!(during.in_flight.len(), 1);
        assert_eq!(during.in_flight[0].last_byte_ms, T0 + 1000);
        assert!(during.recent.is_empty());

        live.first_byte(id, T0 + 1300);
        live.byte(id, T0 + 1800);
        let streaming = live.snapshot(T0 + 2000, &inputs(&["zai"]));
        assert_eq!(streaming.in_flight[0].last_byte_ms, T0 + 1800);

        live.finish(
            id,
            Finish {
                usage: Some(usage(100, 20)),
                ..end(200, None, T0 + 2500)
            },
        );
        let after = live.snapshot(T0 + 3000, &inputs(&["zai"]));
        assert!(after.in_flight.is_empty());
        let row = &after.recent[0];
        assert_eq!(row.id, id);
        assert_eq!(row.at_ms, T0 + 2500);
        assert_eq!(row.status, 200);
        assert_eq!(row.ttft_ms, Some(300));
        assert_eq!(row.tokens_in, Some(100));
        assert_eq!(row.tokens_out, Some(20));
        assert_eq!(row.reason, None);
        let lane = &after.lanes[0];
        assert_eq!((lane.requests, lane.errors), (1, 0));
        assert_eq!((lane.tokens_in, lane.tokens_out), (100, 20));
    }

    #[test]
    fn cache_tokens_count_as_prompt_tokens() {
        let live = LiveState::new(T0);
        let id = begin(&live, "anthropic", "claude-opus-5-5", T0);
        live.finish(
            id,
            Finish {
                usage: Some(Usage {
                    input_tokens: Some(10),
                    output_tokens: Some(5),
                    cache_creation_input_tokens: Some(100),
                    cache_read_input_tokens: Some(1000),
                    ..Usage::default()
                }),
                ..end(200, None, T0 + 10)
            },
        );
        let snapshot = live.snapshot(T0 + 20, &inputs(&["anthropic"]));
        assert_eq!(snapshot.recent[0].tokens_in, Some(1110));
        assert_eq!(snapshot.lanes[0].tokens_in, 1110);
    }

    #[test]
    fn a_request_without_usage_has_null_tokens() {
        let live = LiveState::new(T0);
        let id = begin(&live, "zai", "glm-5.3", T0);
        live.finish(id, end(200, None, T0 + 10));
        let row = &live.snapshot(T0 + 20, &inputs(&["zai"])).recent[0];
        assert_eq!(
            (row.tokens_in, row.tokens_out, row.ttft_ms),
            (None, None, None)
        );
    }

    #[test]
    fn the_ring_is_newest_first_and_holds_200() {
        let live = LiveState::new(T0);
        for n in 0..205u64 {
            let id = begin(&live, "zai", "glm-5.3", T0 + n);
            live.finish(id, end(200, None, T0 + n));
        }
        let snapshot = live.snapshot(T0 + 300, &inputs(&["zai"]));
        assert_eq!(snapshot.recent.len(), 200);
        assert_eq!(snapshot.recent[0].id, 205);
        assert_eq!(snapshot.recent[199].id, 6);
        assert_eq!(snapshot.lanes[0].requests, 205);
    }

    #[test]
    fn the_session_shows_in_full_or_null() {
        let live = LiveState::new(T0);
        let with = begin(&live, "zai", "glm-5.3", T0);
        let without = live.start(RequestStart {
            session: None,
            model: "glm-5.3".into(),
            lane: "zai".into(),
            now_ms: T0,
        });
        let open = live.snapshot(T0, &inputs(&["zai"]));
        assert_eq!(
            open.in_flight[0].session.as_deref(),
            Some("cse_01JqXrugEVi7")
        );
        assert_eq!(open.in_flight[1].session, None);
        live.finish(with, end(200, None, T0 + 1));
        live.finish(without, end(200, None, T0 + 1));
        let done = live.snapshot(T0 + 2, &inputs(&["zai"]));
        assert_eq!(done.recent[1].session.as_deref(), Some("cse_01JqXrugEVi7"));
        assert_eq!(done.recent[0].session, None);
    }

    #[test]
    fn per_second_covers_the_last_60_whole_seconds() {
        let live = LiveState::new(T0);
        let base = T0 - T0 % 1000;
        for offset in [0, 100, 5_000, 59_999] {
            begin(&live, "zai", "m", base + offset);
        }
        // At base+60s the whole seconds are base..base+59s, oldest first.
        let series = live.snapshot(base + 60_000, &inputs(&[])).per_second;
        assert_eq!(series.len(), 60);
        assert_eq!(series[0], 2);
        assert_eq!(series[5], 1);
        assert_eq!(series[59], 1);
        assert_eq!(series.iter().sum::<u32>(), 4);
        // One second later the oldest bucket has rolled out.
        let rolled = live.snapshot(base + 61_000, &inputs(&[])).per_second;
        assert_eq!(rolled[0], 0);
        assert_eq!(rolled[4], 1);
        assert_eq!(rolled.iter().sum::<u32>(), 2);
        // The current, unfinished second is left out.
        let partial = live.snapshot(base + 500, &inputs(&[])).per_second;
        assert_eq!(partial.iter().sum::<u32>(), 0);
        // A slot reused by a later second does not carry the old count.
        begin(&live, "zai", "m", base + 60_000);
        let reused = live.snapshot(base + 61_000, &inputs(&[])).per_second;
        assert_eq!(reused[59], 1);
        assert_eq!(reused.iter().sum::<u32>(), 3);
    }

    #[test]
    fn the_lane_reports_percentiles_over_its_ttfts() {
        let live = LiveState::new(T0);
        for n in 1..=100u64 {
            let id = begin(&live, "zai", "glm-5.3", T0);
            live.first_byte(id, T0 + n * 10);
            live.finish(id, end(200, None, T0 + 2000));
        }
        let snapshot = live.snapshot(T0 + 3000, &inputs(&["zai", "kimi"]));
        assert_eq!(snapshot.lanes[0].ttft_p50_ms, Some(500));
        assert_eq!(snapshot.lanes[0].ttft_p95_ms, Some(950));
        assert_eq!(snapshot.lanes[1].ttft_p50_ms, None);
        assert_eq!(snapshot.lanes[1].ttft_p95_ms, None);
    }

    #[test]
    fn a_lane_keeps_only_its_last_500_ttfts() {
        let live = LiveState::new(T0);
        for n in 0..600u64 {
            let id = begin(&live, "zai", "glm-5.3", T0);
            // The first 100 are slow, the next 500 fast.
            live.first_byte(id, T0 + if n < 100 { 9000 } else { 10 });
            live.finish(id, end(200, None, T0 + 9500));
        }
        let lane = &live.snapshot(T0 + 10_000, &inputs(&["zai"])).lanes[0];
        assert_eq!(lane.ttft_p95_ms, Some(10));
        assert_eq!(lane.requests, 600);
    }

    #[test]
    fn errors_count_over_five_minutes_and_the_last_one_stays() {
        let live = LiveState::new(T0);
        let old = begin(&live, "zai", "glm-5.3", T0);
        live.finish(old, end(429, None, T0 + 1000));
        let recent = begin(&live, "kimi", "kimi-k3", T0 + 200_000);
        live.finish(recent, end(500, None, T0 + 200_000));
        let ok = begin(&live, "kimi", "kimi-k3", T0 + 200_000);
        live.finish(ok, end(200, None, T0 + 200_000));

        let early = live.snapshot(T0 + 250_000, &inputs(&["zai", "kimi"]));
        assert_eq!(early.errors_5m, 2);
        assert_eq!(early.lanes[0].errors, 1);
        assert_eq!(early.lanes[1].errors, 1);

        let late = live.snapshot(T0 + 400_000, &inputs(&["zai", "kimi"]));
        assert_eq!(late.errors_5m, 1);
        let last = late.last_error.expect("an error happened");
        assert_eq!(
            (last.at_ms, last.lane.as_str(), last.status),
            (T0 + 200_000, "kimi", 500)
        );
        assert_eq!(last.reason.as_deref(), Some("upstream"));

        let none = live.snapshot(T0 + 900_000, &inputs(&["zai"]));
        assert_eq!(none.errors_5m, 0);
        assert!(none.last_error.is_some());
    }

    #[test]
    fn reasons_come_from_status_and_error_text() {
        let cases = [
            (200, None, None),
            (429, None, Some("quota")),
            (529, None, Some("overloaded")),
            (499, None, Some("client left")),
            (500, None, Some("upstream")),
            (503, None, Some("upstream")),
            (400, None, Some("upstream")),
            (504, Some("upstream timeout after 30s"), Some("timeout")),
            (502, Some("Connection refused"), Some("refused")),
            (0, Some("request timed out"), Some("timeout")),
        ];
        for (status, error, want) in cases {
            assert_eq!(
                reason(status, error).as_deref(),
                want,
                "status {status} error {error:?}"
            );
        }
        let live = LiveState::new(T0);
        let id = begin(&live, "zai", "glm-5.3", T0);
        live.finish(id, end(429, None, T0 + 1));
        let row = &live.snapshot(T0 + 2, &inputs(&["zai"])).recent[0];
        assert_eq!(row.reason.as_deref(), Some("quota"));
    }

    #[test]
    fn subscribers_hear_start_and_finish_but_not_bytes() {
        let live = LiveState::new(T0);
        let mut rx = live.subscribe();
        assert!(!rx.has_changed().expect("sender alive"));
        let id = begin(&live, "zai", "glm-5.3", T0);
        assert_eq!(*rx.borrow_and_update(), 1);
        live.first_byte(id, T0 + 5);
        live.byte(id, T0 + 6);
        assert!(!rx.has_changed().expect("sender alive"));
        live.finish(id, end(200, None, T0 + 10));
        assert_eq!(*rx.borrow_and_update(), 2);
        live.finish(id, end(200, None, T0 + 11));
        assert!(!rx.has_changed().expect("sender alive"));
    }

    #[test]
    fn a_client_that_leaves_is_not_a_lane_error() {
        let live = LiveState::new(T0);
        let id = begin(&live, "zai", "glm-5.3", T0);
        live.finish(id, end(499, None, T0 + 100));
        let snapshot = live.snapshot(T0 + 200, &inputs(&["zai"]));
        assert_eq!(snapshot.lanes[0].requests, 1);
        assert_eq!(snapshot.lanes[0].errors, 0);
        assert_eq!(snapshot.errors_5m, 0);
        assert_eq!(snapshot.last_error, None);
        assert_eq!(snapshot.recent[0].status, 499);
        assert_eq!(snapshot.recent[0].reason.as_deref(), Some("client left"));
    }

    #[test]
    fn the_snapshot_serializes_to_the_contract() {
        let live = LiveState::new(T0);
        let id = begin(&live, "zai", "glm-5.3", T0 + 1000);
        live.finish(id, end(429, None, T0 + 1500));
        begin(&live, "anthropic", "claude-opus-5-5", T0 + 2000);
        let mut given = inputs(&["anthropic", "kimi"]);
        given.lanes[1].enabled = false;
        given.lanes[1].setup = Some("/kimi:setup".into());
        given.lanes[1].key = Some(false);
        given.jobs.push(JobRow {
            id: "j-7f3a".into(),
            harness: "opencode".into(),
            repo: "huddle".into(),
            title: "add lane tests".into(),
            state: "running".into(),
            started_ms: T0,
            ended_ms: None,
            files: None,
            add: None,
            del: None,
            why: None,
        });
        let json = serde_json::to_value(live.snapshot(T0 + 3000, &given)).expect("serializes");

        let keys = |value: &serde_json::Value| -> Vec<String> {
            let mut keys: Vec<String> =
                value.as_object().expect("object").keys().cloned().collect();
            keys.sort();
            keys
        };
        let sorted = |names: &[&str]| -> Vec<String> {
            let mut names: Vec<String> = names.iter().map(|name| name.to_string()).collect();
            names.sort();
            names
        };
        assert_eq!(
            keys(&json),
            sorted(&[
                "version",
                "pid",
                "started_ms",
                "now_ms",
                "wiring",
                "per_second",
                "errors_5m",
                "last_error",
                "lanes",
                "recent",
                "in_flight",
                "jobs",
                "router",
                "plan",
                "buckets",
                "models",
                "sessions",
                "worktrees",
                "plugins",
                "harnesses",
                "hosts"
            ])
        );
        assert_eq!(json["version"], "0.0.2");
        assert_eq!(json["pid"], 7);
        assert_eq!(json["started_ms"], T0);
        assert_eq!(json["now_ms"], T0 + 3000);
        assert_eq!(
            json["wiring"],
            serde_json::json!({"state": "bad", "proxy": true, "ca": false})
        );
        assert_eq!(
            keys(&json["last_error"]),
            sorted(&["at_ms", "lane", "model", "status", "reason"])
        );
        assert_eq!(json["last_error"]["reason"], "quota");
        assert_eq!(
            keys(&json["lanes"][0]),
            sorted(&[
                "id",
                "name",
                "enabled",
                "models",
                "setup",
                "key",
                "requests",
                "errors",
                "ttft_p50_ms",
                "ttft_p95_ms",
                "ttft_p50",
                "last",
                "tokens_in",
                "tokens_out"
            ])
        );
        assert!(json["lanes"][0]["key"].is_null());
        assert_eq!(json["lanes"][1]["key"], false);
        assert_eq!(json["lanes"][1]["setup"], "/kimi:setup");
        assert!(json["lanes"][1]["ttft_p50_ms"].is_null());
        assert_eq!(
            keys(&json["recent"][0]),
            sorted(&[
                "id",
                "at_ms",
                "session",
                "model",
                "lane",
                "status",
                "reason",
                "ttft_ms",
                "tokens_in",
                "tokens_out",
                "agent",
                "dur_ms",
                "router_ms",
                "cache_read",
                "cache_write",
                "stop",
                "upstream_id"
            ])
        );
        assert!(json["recent"][0]["tokens_in"].is_null());
        assert_eq!(
            keys(&json["in_flight"][0]),
            sorted(&[
                "id",
                "started_ms",
                "last_byte_ms",
                "session",
                "model",
                "lane",
                "agent"
            ])
        );
        assert_eq!(
            keys(&json["jobs"][0]),
            sorted(&[
                "id",
                "harness",
                "repo",
                "title",
                "state",
                "started_ms",
                "ended_ms",
                "files",
                "add",
                "del",
                "why"
            ])
        );
        assert_eq!(json["per_second"].as_array().expect("array").len(), 60);
    }

    fn answered(live: &LiveState, lane: &str, model: &str, at: u64, ttft: u64, finish: Finish) {
        let id = begin(live, lane, model, at);
        live.first_byte(id, at + ttft);
        live.finish(id, finish);
    }

    #[test]
    fn a_recent_row_carries_timing_cache_stop_and_the_upstream_id() {
        let live = LiveState::new(T0);
        let id = begin(&live, "anthropic", "claude-opus-5-5", T0);
        live.first_byte(id, T0 + 400);
        live.finish(
            id,
            Finish {
                usage: Some(Usage {
                    input_tokens: Some(10),
                    output_tokens: Some(5),
                    cache_creation_input_tokens: Some(100),
                    cache_read_input_tokens: Some(1000),
                    stop_reason: Some("end_turn".into()),
                }),
                upstream_id: Some("req_011CV".into()),
                router_ms: Some(2),
                ..end(200, None, T0 + 1500)
            },
        );
        let row = &live.snapshot(T0 + 2000, &inputs(&["anthropic"])).recent[0];
        assert_eq!(row.dur_ms, 1500);
        assert_eq!(row.router_ms, Some(2));
        assert_eq!((row.cache_read, row.cache_write), (Some(1000), Some(100)));
        assert_eq!(row.stop.as_deref(), Some("end_turn"));
        assert_eq!(row.upstream_id.as_deref(), Some("req_011CV"));
        assert_eq!(row.agent, None);
    }

    fn logged(id: &str, agent: &str, request: &str) -> huddle_sessions::Session {
        huddle_sessions::Session {
            id: id.into(),
            title: String::new(),
            project: None,
            cwd: None,
            branch: None,
            model: None,
            state: huddle_sessions::SessionState::Waiting,
            started_ms: None,
            last_ms: None,
            waiting_since_ms: None,
            context_tokens: None,
            context_limit: 200_000,
            cache_ttl_ms: 300_000,
            last_request_ms: None,
            agents: Vec::new(),
            request_ids: vec![huddle_sessions::RequestTag {
                request_id: request.into(),
                agent_id: agent.into(),
                usage: huddle_sessions::Usage {
                    input: 10,
                    cache_read: 200,
                    cache_write: 30,
                    output: 70,
                },
                stop: Some("tool_use".into()),
            }],
        }
    }

    fn unread(live: &LiveState, upstream: &str, at: u64) {
        answered(
            live,
            "anthropic",
            "claude-opus-5-5",
            at,
            400,
            Finish {
                upstream_id: Some(upstream.into()),
                ..end(200, None, at + 1_400)
            },
        );
    }

    #[test]
    fn q2_the_logs_usage_fills_the_row_the_charts_and_the_lane() {
        let live = LiveState::new(T0);
        let base = T0 - T0 % 30_000;
        unread(&live, "req_A", base + 1_000);
        live.credit(&[logged("s1", "agent-x", "req_A")]);
        let snapshot = live.snapshot(base + 10_000, &inputs(&["anthropic"]));
        let row = &snapshot.recent[0];
        assert_eq!(row.agent.as_deref(), Some("s1:agent-x"));
        assert_eq!(
            (
                row.tokens_in,
                row.cache_read,
                row.cache_write,
                row.tokens_out
            ),
            (Some(240), Some(200), Some(30), Some(70))
        );
        assert_eq!(row.stop.as_deref(), Some("tool_use"));
        let bucket = &snapshot.buckets.models["claude-opus-5-5"][29];
        assert_eq!((bucket.tok_in, bucket.tok_out), (240, 70));
        let model = &snapshot.models[0];
        assert_eq!((model.tokens_in, model.tokens_out), (240, 70));
        assert_eq!(model.out_tps_p50, Some(70));
        assert_eq!(
            (snapshot.lanes[0].tokens_in, snapshot.lanes[0].tokens_out),
            (240, 70)
        );
    }

    #[test]
    fn q2_a_request_is_credited_once() {
        let live = LiveState::new(T0);
        unread(&live, "req_A", T0);
        let logs = [logged("s1", "agent-x", "req_A")];
        live.credit(&logs);
        live.credit(&logs);
        let snapshot = live.snapshot(T0 + 5_000, &inputs(&["anthropic"]));
        assert_eq!(snapshot.lanes[0].tokens_in, 240);
        assert_eq!(snapshot.models[0].tokens_in, 240);
    }

    #[test]
    fn q2_the_routers_own_usage_wins_over_the_log() {
        let live = LiveState::new(T0);
        answered(
            &live,
            "anthropic",
            "claude-opus-5-5",
            T0,
            400,
            Finish {
                usage: Some(usage(999, 1)),
                upstream_id: Some("req_A".into()),
                ..end(200, None, T0 + 1_400)
            },
        );
        live.credit(&[logged("s1", "agent-x", "req_A")]);
        let snapshot = live.snapshot(T0 + 5_000, &inputs(&["anthropic"]));
        let row = &snapshot.recent[0];
        assert_eq!((row.tokens_in, row.tokens_out), (Some(999), Some(1)));
        assert_eq!(row.agent.as_deref(), Some("s1:agent-x"));
        assert_eq!(snapshot.lanes[0].tokens_in, 999);
    }

    #[test]
    fn q2_no_match_and_no_upstream_id_leave_the_row_alone() {
        let live = LiveState::new(T0);
        unread(&live, "req_other", T0);
        let id = begin(&live, "anthropic", "claude-opus-5-5", T0);
        live.finish(id, end(200, None, T0 + 10));
        live.credit(&[logged("s1", "agent-x", "req_A")]);
        let snapshot = live.snapshot(T0 + 5_000, &inputs(&["anthropic"]));
        for row in &snapshot.recent {
            assert_eq!((row.agent.as_deref(), row.tokens_in), (None, None));
        }
        assert_eq!(snapshot.lanes[0].tokens_in, 0);
    }

    #[test]
    fn a_request_with_no_usage_has_null_cache_and_stop() {
        let live = LiveState::new(T0);
        let id = begin(&live, "zai", "glm-5.3", T0);
        live.finish(id, end(200, None, T0 + 10));
        let row = &live.snapshot(T0 + 20, &inputs(&["zai"])).recent[0];
        assert_eq!(
            (row.cache_read, row.cache_write, row.router_ms),
            (None, None, None)
        );
        assert_eq!(
            (row.stop.as_deref(), row.upstream_id.as_deref()),
            (None, None)
        );
    }

    #[test]
    fn buckets_and_models_follow_finished_requests() {
        let live = LiveState::new(T0);
        let base = T0 - T0 % 30_000;
        let ok = Finish {
            usage: Some(usage(100, 40)),
            ..end(200, None, base + 1_800)
        };
        answered(&live, "zai", "glm-5.3", base + 1_000, 300, ok);
        answered(
            &live,
            "zai",
            "glm-5.3",
            base + 2_000,
            500,
            end(500, None, base + 2_500),
        );
        answered(
            &live,
            "zai",
            "glm-5.3",
            base + 3_000,
            500,
            end(499, None, base + 3_500),
        );
        let snapshot = live.snapshot(base + 10_000, &inputs(&["zai"]));
        let last = &snapshot.buckets.models["glm-5.3"][29];
        assert_eq!((last.n, last.err), (3, 1));
        assert_eq!((last.tok_in, last.tok_out), (100, 40));
        assert_eq!(snapshot.buckets.end_ms, base + 30_000);
        let model = &snapshot.models[0];
        assert_eq!((model.requests, model.errors), (3, 1));
        assert_eq!(model.lane, "zai");
        assert_eq!(model.last_ms, base + 3_500);
    }

    #[test]
    fn a_lane_reports_its_last_answer_and_ttft_p50() {
        let live = LiveState::new(T0);
        answered(&live, "zai", "glm-5.3", T0, 200, end(200, None, T0 + 500));
        answered(
            &live,
            "zai",
            "glm-5.3",
            T0 + 1000,
            400,
            end(429, None, T0 + 1500),
        );
        let snapshot = live.snapshot(T0 + 2000, &inputs(&["zai", "kimi"]));
        let zai = &snapshot.lanes[0];
        assert_eq!(zai.ttft_p50, zai.ttft_p50_ms);
        assert_eq!(zai.ttft_p50, Some(200));
        assert_eq!(
            zai.last,
            Some(LaneLast {
                status: 429,
                at_ms: T0 + 1500
            })
        );
        assert_eq!(snapshot.lanes[1].last, None);
    }

    #[test]
    fn the_plan_is_the_latest_one_an_answer_carried() {
        let live = LiveState::new(T0);
        assert_eq!(live.snapshot(T0, &inputs(&[])).plan, None);
        let plan = |pct| PlanUsage {
            five_hour_pct: Some(pct),
            five_hour_reset_ms: Some(T0 + 1000),
            week_pct: Some(10),
            week_reset_ms: Some(T0 + 9000),
        };
        let id = begin(&live, "anthropic", "claude-opus-5-5", T0);
        live.finish(
            id,
            Finish {
                plan: Some(plan(50)),
                ..end(200, None, T0 + 1)
            },
        );
        let id = begin(&live, "anthropic", "claude-opus-5-5", T0 + 2);
        live.finish(id, end(200, None, T0 + 3));
        assert_eq!(live.snapshot(T0 + 4, &inputs(&[])).plan, Some(plan(50)));
        let id = begin(&live, "anthropic", "claude-opus-5-5", T0 + 5);
        live.finish(
            id,
            Finish {
                plan: Some(plan(62)),
                ..end(200, None, T0 + 6)
            },
        );
        assert_eq!(live.snapshot(T0 + 7, &inputs(&[])).plan, Some(plan(62)));
    }

    #[test]
    fn the_router_card_counts_open_feeds_and_connections() {
        let live = Arc::new(LiveState::new(T0));
        assert_eq!(live.snapshot(T0, &inputs(&[])).router.viewers, 0);
        let first = live.viewer();
        let second = live.viewer();
        let router = live.snapshot(T0, &inputs(&[])).router;
        assert_eq!((router.viewers, router.connections), (2, 3));
        assert_eq!(router.cpu_pct, Some(0.4));
        assert_eq!(router.rss_bytes, Some(19_300_000));
        drop(first);
        drop(second);
        assert_eq!(live.snapshot(T0, &inputs(&[])).router.viewers, 0);
    }
}
