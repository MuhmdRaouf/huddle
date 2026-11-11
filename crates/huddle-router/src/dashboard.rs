//! huddle-router: dashboard.
//!
//! The page, the state snapshot and the live feed, served from the router's entry handler.
//! The page reads the feed only while it is open: a connected client holds one task, and with
//! no client nothing is scheduled.

use std::sync::Arc;
use std::time::Duration;

use http_body_util::BodyExt;
use hyper::body::{Bytes, Frame};
use hyper::header::{CACHE_CONTROL, CONTENT_TYPE, HeaderValue};
use hyper::{Response, StatusCode};
use tokio::sync::mpsc;
use tokio::time::{Instant, sleep_until};

use crate::anthropic::StreamingBody;
use crate::dispatch::now_ms;
use crate::join;
use crate::live::{
    JobRow, LaneMeta, LiveState, ProcessUsage, Snapshot, SnapshotInputs, Wiring, WiringState,
};
use crate::server::{RespBody, full_body, json_answer};
use huddle_catalog::providers::providers;
use huddle_sessions::{Session, Worktree};

pub const PAGE_PATH: &str = "/";
pub const STATE_PATH: &str = "/huddle-router/state";
pub const LIVE_PATH: &str = "/huddle-router/live";

const PAGE: &str = include_str!("dashboard.html");

/// The fewest milliseconds between two snapshots on one feed.
const MIN_GAP: Duration = Duration::from_secs(1);
/// How often an open feed with no request in flight sends a comment so the connection stays up.
const PING_EVERY: Duration = Duration::from_secs(15);
/// How often an open feed refreshes while a request is in flight, so its last-byte time moves.
const BUSY_EVERY: Duration = Duration::from_secs(5);

/// Answers whether the keystore holds a key for a provider id; `None` when unknown.
pub type KeyPresent = Arc<dyn Fn(&str) -> Option<bool> + Send + Sync>;

/// What the snapshot needs from outside the router. Each closure answers from a cache and
/// never blocks: a slow read refreshes the cache off the request path.
#[derive(Clone)]
pub struct DashboardInputs {
    /// The running version.
    pub version: String,
    /// Whether Claude Code is pointed at this router.
    pub wiring: Arc<dyn Fn() -> Wiring + Send + Sync>,
    /// Whether the keystore holds a key for a provider id.
    pub key_present: KeyPresent,
    /// The delegated jobs that are live or waiting for review.
    pub jobs: Arc<dyn Fn() -> Vec<JobRow> + Send + Sync>,
    /// The hosts besides the loopback that may reach the dashboard's read-only paths.
    pub hosts: Arc<dyn Fn() -> Vec<String> + Send + Sync>,
    /// Connections open to the router now.
    pub connections: Arc<dyn Fn() -> usize + Send + Sync>,
    /// The wiring card's state.
    pub wiring_state: Arc<dyn Fn() -> WiringState + Send + Sync>,
    /// The router process's CPU and memory. Asked only while a page is open.
    pub process: Arc<dyn Fn() -> ProcessUsage + Send + Sync>,
    /// Claude Code's sessions and the projects' worktrees. Asked only while a page is open.
    pub sessions: Arc<dyn Fn() -> Machine + Send + Sync>,
    /// The plugin page: `{state, rows, checks}`. Asked only while a page is open.
    pub plugins: Arc<dyn Fn() -> serde_json::Value + Send + Sync>,
    /// The harness page: `{state, rows}`. Asked only while a page is open.
    pub harnesses: Arc<dyn Fn() -> serde_json::Value + Send + Sync>,
}

/// What the session logs and git say about the machine.
#[derive(Debug, Clone, Default)]
pub struct Machine {
    pub sessions: Vec<Session>,
    pub worktrees: Vec<Worktree>,
}

/// A page that has no reading yet.
fn checking() -> serde_json::Value {
    serde_json::json!({ "state": "checking", "rows": [], "checks": [] })
}

impl Default for DashboardInputs {
    /// Nothing known: no wiring, no key answers, no jobs, no extra hosts.
    fn default() -> Self {
        DashboardInputs {
            version: String::new(),
            wiring: Arc::new(Wiring::default),
            key_present: Arc::new(|_| None),
            jobs: Arc::new(Vec::new),
            hosts: Arc::new(Vec::new),
            connections: Arc::new(|| 0),
            wiring_state: Arc::new(WiringState::default),
            process: Arc::new(ProcessUsage::default),
            sessions: Arc::new(Machine::default),
            plugins: Arc::new(checking),
            harnesses: Arc::new(checking),
        }
    }
}

/// The dashboard's routes over one live state.
pub struct Dashboard {
    live: Arc<LiveState>,
    inputs: DashboardInputs,
    /// The provider ids with a lane now.
    enabled: Arc<dyn Fn() -> Vec<String> + Send + Sync>,
}

impl Dashboard {
    pub fn new(
        live: Arc<LiveState>,
        inputs: DashboardInputs,
        enabled: Arc<dyn Fn() -> Vec<String> + Send + Sync>,
    ) -> Dashboard {
        Dashboard {
            live,
            inputs,
            enabled,
        }
    }

    /// The page, never cached: it changes with the binary.
    pub fn page_answer(&self) -> Response<RespBody> {
        let mut response = Response::new(full_body(PAGE));
        let headers = response.headers_mut();
        headers.insert(
            CONTENT_TYPE,
            HeaderValue::from_static("text/html; charset=utf-8"),
        );
        headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    /// The snapshot as one JSON answer.
    pub fn state_answer(&self) -> Response<RespBody> {
        let mut response = json_answer(StatusCode::OK, self.snapshot_json());
        response
            .headers_mut()
            .insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    /// The snapshot as a server-sent event stream: one at once, then at most one a second while
    /// the live state changes. The task ends when the client leaves.
    pub fn live_answer(self: &Arc<Self>) -> Response<RespBody> {
        let (tx, rx) = mpsc::channel(2);
        tokio::spawn(Arc::clone(self).feed(tx));
        let mut response = Response::new(StreamingBody { rx }.boxed());
        let headers = response.headers_mut();
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("text/event-stream"));
        headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    async fn feed(self: Arc<Self>, tx: mpsc::Sender<Result<Frame<Bytes>, crate::server::BodyCut>>) {
        let _viewer = self.live.viewer();
        let mut changes = self.live.subscribe();
        let mut last_sent = Instant::now();
        let mut snapshot = self.snapshot();
        if !send(&tx, event_of(&snapshot)).await {
            return;
        }
        loop {
            let wait = if snapshot.in_flight.is_empty() {
                PING_EVERY
            } else {
                BUSY_EVERY
            };
            let moved = tokio::select! {
                changed = changes.changed() => {
                    if changed.is_err() {
                        return;
                    }
                    true
                }
                _ = sleep_until(last_sent + wait) => false,
                _ = tx.closed() => return,
            };
            if moved {
                // Changes that land during the gap fold into the one snapshot that follows it.
                sleep_until(last_sent + MIN_GAP).await;
                changes.borrow_and_update();
            } else if snapshot.in_flight.is_empty() {
                if !send(&tx, ": ping\n\n".to_owned()).await {
                    return;
                }
                last_sent = Instant::now();
                continue;
            }
            snapshot = self.snapshot();
            if !send(&tx, event_of(&snapshot)).await {
                return;
            }
            last_sent = Instant::now();
        }
    }

    fn snapshot(&self) -> Snapshot {
        let enabled = (self.enabled)();
        let mut lanes = vec![LaneMeta {
            id: "anthropic".to_owned(),
            name: "Anthropic".to_owned(),
            enabled: true,
            models: "claude-*".to_owned(),
            setup: None,
            key: None,
        }];
        lanes.extend(providers().iter().map(|provider| {
            let on = enabled.iter().any(|id| id == provider.id);
            LaneMeta {
                id: provider.id.to_owned(),
                name: provider.display.to_owned(),
                enabled: on,
                models: provider
                    .model_prefixes
                    .iter()
                    .map(|prefix| format!("{prefix}*"))
                    .collect::<Vec<_>>()
                    .join(", "),
                setup: (!on).then(|| format!("{}setup", provider.slash)),
                key: (self.inputs.key_present)(provider.id),
            }
        }));
        // The log, git and `ps` reads belong to an open page: without a viewer none is asked.
        let watched = self.live.viewers() > 0;
        let mut inputs = SnapshotInputs {
            version: self.inputs.version.clone(),
            pid: std::process::id(),
            wiring: (self.inputs.wiring)(),
            wiring_state: (self.inputs.wiring_state)(),
            lanes,
            jobs: (self.inputs.jobs)(),
            connections: (self.inputs.connections)(),
            hosts: (self.inputs.hosts)(),
            plugins: checking(),
            harnesses: checking(),
            ..SnapshotInputs::default()
        };
        if watched {
            let machine = (self.inputs.sessions)();
            inputs.process = (self.inputs.process)();
            inputs.sessions = machine.sessions;
            inputs.worktrees = machine.worktrees;
            inputs.plugins = (self.inputs.plugins)();
            inputs.harnesses = (self.inputs.harnesses)();
        }
        let mut snapshot = self.live.snapshot(now_ms(), &inputs);
        join::apply(&mut snapshot);
        snapshot
    }

    fn snapshot_json(&self) -> String {
        // The snapshot is plain data; an empty body keeps the answer well-formed.
        serde_json::to_string(&self.snapshot()).unwrap_or_default()
    }
}

/// One snapshot as a server-sent event.
fn event_of(snapshot: &Snapshot) -> String {
    let json = serde_json::to_string(snapshot).unwrap_or_default();
    format!("event: snapshot\ndata: {json}\n\n")
}

/// Sends one frame; false when the client is gone.
async fn send(
    tx: &mpsc::Sender<Result<Frame<Bytes>, crate::server::BodyCut>>,
    text: String,
) -> bool {
    tx.send(Ok(Frame::data(Bytes::from(text)))).await.is_ok()
}
