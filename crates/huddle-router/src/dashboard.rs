//! huddle-router: dashboard.
//!
//! The page, the state snapshot and the live feed, served from the router's entry handler.
//! The page reads the feed only while it is open: a connected client holds one task, and with
//! no client nothing is scheduled.

use std::collections::BTreeMap;
use std::collections::VecDeque;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use http_body_util::BodyExt;
use hyper::body::{Bytes, Frame};
use hyper::header::{CACHE_CONTROL, CONTENT_ENCODING, CONTENT_TYPE, HeaderValue, VARY};
use hyper::{Response, StatusCode};
use tokio::sync::{mpsc, oneshot, watch};
use tokio::time::{Instant, sleep_until};

use crate::anthropic::StreamingBody;
use crate::connect::Phase;
use crate::dispatch::now_ms;
use crate::history_view::{self, Query, Refusal};
use crate::join;
use crate::live::{
    JobRow, LaneMeta, LiveState, ProcessUsage, Snapshot, SnapshotInputs, Supervision, Wiring,
    WiringState,
};
use crate::quota::QuotaFetcher;
use crate::server::{RespBody, full_body, json_answer};
use huddle_catalog::providers::providers;
use huddle_history::{GroupBy, History};
use huddle_sessions::{Session, Worktree};

pub const PAGE_PATH: &str = "/";
pub const STATE_PATH: &str = "/huddle-router/state";
pub const LIVE_PATH: &str = "/huddle-router/live";
pub const HISTORY_PATH: &str = "/huddle-router/history";

/// The dashboard page: the files in `dashboard/` joined by `build.rs`. Only tests read this; the
/// router serves `PAGE_GZIP`, so the plain text is not part of the binary.
pub const PAGE: &str = include_str!(concat!(env!("OUT_DIR"), "/page.html"));

/// The page gzipped at build time: the only copy the binary holds.
const PAGE_GZIP: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/page.html.gz"));

/// What a client that cannot read gzip is told.
const NEEDS_GZIP: &str =
    "The huddle dashboard is served gzip-compressed. Open it in a browser that accepts gzip.\n";

/// True when an `Accept-Encoding` value allows gzip: `gzip` or `*`, with a quality above zero.
fn accepts_gzip(accept_encoding: Option<&str>) -> bool {
    accept_encoding.is_some_and(|value| {
        value.split(',').any(|item| {
            let mut parts = item.split(';');
            let coding = parts.next().unwrap_or("").trim();
            let quality = parts
                .filter_map(|p| p.trim().strip_prefix("q="))
                .find_map(|q| q.trim().parse::<f32>().ok())
                .unwrap_or(1.0);
            (coding.eq_ignore_ascii_case("gzip") || coding == "*") && quality > 0.0
        })
    })
}

/// The kept history when nothing says otherwise.
const DEFAULT_KEEP_DAYS: u64 = 90;

/// The shortest gap between two snapshots on one feed: at most four a second.
const MIN_GAP: Duration = Duration::from_millis(250);
/// Open dashboard feeds at once; a newer feed closes the oldest past this many.
pub const MAX_LIVE_STREAMS: usize = 8;
/// Milliseconds past a second's end before the follow-up snapshot, so the second counts as whole.
const SETTLE_SLACK_MS: u64 = 20;
/// How often an open feed with no request in flight sends a comment so the connection stays up.
const PING_EVERY: Duration = Duration::from_secs(15);
/// How often an open feed refreshes while a request is in flight, so its last-byte time moves.
const BUSY_EVERY: Duration = Duration::from_secs(5);

/// Answers whether the keystore holds a key for a provider id; `None` when unknown.
pub type KeyPresent = Arc<dyn Fn(&str) -> Option<bool> + Send + Sync>;

/// Opens the history database for reading, or answers none when there is no database. Called on
/// a blocking thread, once per history request.
pub type HistoryOpen = Arc<dyn Fn() -> Option<History> + Send + Sync>;

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
    /// The supervising parent and its worker: one small file read.
    pub supervision: Arc<dyn Fn() -> Option<Supervision> + Send + Sync>,
    /// Claude Code's sessions and the projects' worktrees. Asked only while a page is open.
    pub sessions: Arc<dyn Fn() -> Machine + Send + Sync>,
    /// The plugin page: `{state, rows, checks}`. Asked only while a page is open.
    pub plugins: Arc<dyn Fn() -> serde_json::Value + Send + Sync>,
    /// The harness page: `{state, rows}`. Asked only while a page is open.
    pub harnesses: Arc<dyn Fn() -> serde_json::Value + Send + Sync>,
    /// The history database for `/huddle-router/history`. Called only when such a request comes.
    pub history: HistoryOpen,
    /// How many days of history the router keeps (`[history] keep_days`).
    pub keep_days: Arc<dyn Fn() -> u64 + Send + Sync>,
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
            supervision: Arc::new(|| None),
            sessions: Arc::new(Machine::default),
            plugins: Arc::new(checking),
            harnesses: Arc::new(checking),
            history: Arc::new(|| None),
            keep_days: Arc::new(|| DEFAULT_KEEP_DAYS),
        }
    }
}

/// The dashboard's routes over one live state.
pub struct Dashboard {
    live: Arc<LiveState>,
    inputs: DashboardInputs,
    /// The provider ids with a lane now.
    enabled: Arc<dyn Fn() -> Vec<String> + Send + Sync>,
    /// Asks each set-up provider for its quota while a page is open.
    quota: Option<Arc<QuotaFetcher>>,
    /// The server's drain: the live feed ends once it starts, and the page reconnects to the
    /// router that took over.
    drain: Option<watch::Receiver<Phase>>,
    /// The open feeds, oldest first, each with the sender that closes it.
    feeds: Mutex<VecDeque<oneshot::Sender<()>>>,
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
            quota: None,
            drain: None,
            feeds: Mutex::new(VecDeque::new()),
        }
    }

    /// Asks each set-up provider for its quota while a page is open.
    pub fn with_quota(mut self, quota: Arc<QuotaFetcher>) -> Dashboard {
        self.quota = Some(quota);
        self
    }

    /// Ends every live feed when `drain` leaves Serving.
    pub fn ending_with(mut self, drain: watch::Receiver<Phase>) -> Dashboard {
        self.drain = Some(drain);
        self
    }

    /// The page gzipped, never cached: it changes with the binary. A client whose
    /// `Accept-Encoding` does not allow gzip gets a short plain-text notice instead.
    pub fn page_answer(&self, accept_encoding: Option<&str>) -> Response<RespBody> {
        let gzip = accepts_gzip(accept_encoding);
        let mut response = if gzip {
            Response::new(full_body(Bytes::from_static(PAGE_GZIP)))
        } else {
            let mut notice = Response::new(full_body(NEEDS_GZIP));
            *notice.status_mut() = StatusCode::NOT_ACCEPTABLE;
            notice
        };
        let headers = response.headers_mut();
        let content_type = if gzip {
            "text/html; charset=utf-8"
        } else {
            "text/plain; charset=utf-8"
        };
        headers.insert(CONTENT_TYPE, HeaderValue::from_static(content_type));
        if gzip {
            headers.insert(CONTENT_ENCODING, HeaderValue::from_static("gzip"));
        }
        headers.insert(VARY, HeaderValue::from_static("accept-encoding"));
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

    /// The snapshot without its recent rows, for a page that does not show them. Every other field,
    /// the counts derived from the rows included, is the full answer's.
    pub fn state_slim_answer(&self) -> Response<RespBody> {
        let mut response = json_answer(StatusCode::OK, slim_json(self.snapshot()));
        response
            .headers_mut()
            .insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    /// The snapshot after one quota refresh that counts as a viewer for that ask. The five
    /// minute limit per provider still holds, so a caller asking often reads the cached answer.
    pub async fn state_answer_with_quota(&self) -> Response<RespBody> {
        if let Some(quota) = &self.quota {
            quota.refresh(now_ms(), 1, &(self.enabled)()).await;
        }
        let mut response = json_answer(
            StatusCode::OK,
            serde_json::to_string(&self.snapshot_with(true)).unwrap_or_default(),
        );
        response
            .headers_mut()
            .insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    /// The history answer for a query string. The database work runs on a blocking thread.
    pub async fn history_answer(&self, query: &str) -> Response<RespBody> {
        let now = now_ms();
        let parsed = match history_view::parse(query, now, (self.inputs.keep_days)()) {
            Ok(parsed) => parsed,
            Err(Refusal::Bad(reason)) => return plain(StatusCode::BAD_REQUEST, &reason),
            Err(Refusal::UnknownView) => return plain(StatusCode::NOT_FOUND, "no such view"),
        };
        let open = Arc::clone(&self.inputs.history);
        let machine = Arc::clone(&self.inputs.sessions);
        let worked = tokio::task::spawn_blocking(move || match open() {
            None => Ok(history_view::empty_answer(&parsed)),
            Some(history) => {
                let aliases = if matches!(parsed, Query::Totals(_, GroupBy::Project, _)) {
                    folder_aliases(&machine().worktrees)
                } else {
                    BTreeMap::new()
                };
                history_view::answer(&history, &parsed, &aliases)
            }
        })
        .await;
        let mut response = match worked {
            Ok(Ok(value)) => json_answer(StatusCode::OK, value.to_string()),
            Ok(Err(_)) | Err(_) => plain(StatusCode::INTERNAL_SERVER_ERROR, "history unavailable"),
        };
        response
            .headers_mut()
            .insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    /// The snapshot as a server-sent event stream: one at once, then at most four a second while
    /// the live state changes. `slim` leaves the recent rows out, as `/state?recent=0` does. Past
    /// `MAX_LIVE_STREAMS` open feeds the oldest closes. The task ends when the client leaves.
    pub fn live_answer(self: &Arc<Self>, slim: bool) -> Response<RespBody> {
        let (tx, rx) = mpsc::channel(2);
        let evicted = self.register_feed();
        tokio::spawn(Arc::clone(self).feed(tx, slim, evicted));
        let mut response = Response::new(StreamingBody { rx }.boxed());
        let headers = response.headers_mut();
        headers.insert(CONTENT_TYPE, HeaderValue::from_static("text/event-stream"));
        headers.insert(CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }

    /// Adds a feed to the open set and closes the oldest ones past the cap. The receiver
    /// resolves when this feed is closed that way.
    fn register_feed(&self) -> oneshot::Receiver<()> {
        let (close, evicted) = oneshot::channel();
        let mut feeds = self.feeds.lock().unwrap_or_else(PoisonError::into_inner);
        // A finished feed has dropped its receiver; only open feeds count against the cap.
        feeds.retain(|feed| !feed.is_closed());
        feeds.push_back(close);
        while feeds.len() > MAX_LIVE_STREAMS {
            if let Some(oldest) = feeds.pop_front() {
                let _ = oldest.send(());
            }
        }
        evicted
    }

    async fn feed(
        self: Arc<Self>,
        tx: mpsc::Sender<Result<Frame<Bytes>, crate::server::BodyCut>>,
        slim: bool,
        mut evicted: oneshot::Receiver<()>,
    ) {
        let _viewer = self.live.viewer();
        let mut changes = self.live.subscribe();
        let mut last_sent = Instant::now();
        let snapshot = self.snapshot();
        let mut idle = snapshot.in_flight.is_empty();
        if !send(&tx, event_of(snapshot, slim)).await {
            return;
        }
        // After a push for a change, one more follows once the second it landed in has ended: the
        // per-second series counts whole seconds only.
        let mut settle: Option<Instant> = None;
        loop {
            let (wake, settling) = match settle {
                Some(at) => (at, true),
                None => (
                    last_sent + if idle { PING_EVERY } else { BUSY_EVERY },
                    false,
                ),
            };
            let moved = tokio::select! {
                changed = changes.changed() => {
                    if changed.is_err() {
                        return;
                    }
                    true
                }
                _ = sleep_until(wake) => false,
                _ = tx.closed() => return,
                _ = &mut evicted => return,
                () = drained(self.drain.clone()) => return,
            };
            if moved {
                // Changes that land during the gap fold into the one snapshot that follows it.
                sleep_until(last_sent + MIN_GAP).await;
                changes.borrow_and_update();
            } else if idle && !settling {
                if !send(&tx, ": ping\n\n".to_owned()).await {
                    return;
                }
                last_sent = Instant::now();
                continue;
            }
            let snapshot = self.snapshot();
            idle = snapshot.in_flight.is_empty();
            if !send(&tx, event_of(snapshot, slim)).await {
                return;
            }
            last_sent = Instant::now();
            settle = moved.then(|| {
                let to_next_second = 1000 - now_ms() % 1000 + SETTLE_SLACK_MS;
                last_sent + MIN_GAP.max(Duration::from_millis(to_next_second))
            });
        }
    }

    fn snapshot(&self) -> Snapshot {
        self.snapshot_with(false)
    }

    /// `with_quota` adds the cached quota answers to a snapshot nobody watches.
    fn snapshot_with(&self, with_quota: bool) -> Snapshot {
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
            supervision: (self.inputs.supervision)(),
            hosts: (self.inputs.hosts)(),
            plugins: checking(),
            harnesses: checking(),
            ..SnapshotInputs::default()
        };
        if let Some(quota) = &self.quota {
            if watched {
                // The asks run off the snapshot's path; the answers land in a later snapshot.
                let (fetcher, viewers, ids) = (Arc::clone(quota), self.live.viewers(), enabled);
                inputs.quota = quota.view();
                tokio::spawn(async move { fetcher.refresh(now_ms(), viewers, &ids).await });
            } else if with_quota {
                inputs.quota = quota.view();
            }
        }
        if watched {
            let machine = (self.inputs.sessions)();
            inputs.process = (self.inputs.process)();
            inputs.sessions = machine.sessions;
            inputs.worktrees = machine.worktrees;
            inputs.plugins = (self.inputs.plugins)();
            inputs.harnesses = (self.inputs.harnesses)();
        }
        self.live.credit(&inputs.sessions);
        let mut snapshot = self.live.snapshot(now_ms(), &inputs);
        join::apply(&mut snapshot);
        snapshot
    }

    fn snapshot_json(&self) -> String {
        // The snapshot is plain data; an empty body keeps the answer well-formed.
        serde_json::to_string(&self.snapshot()).unwrap_or_default()
    }
}

/// Each linked worktree's folder name mapped to its repository's name, so history rows written
/// under a folder name total under the repository.
fn folder_aliases(worktrees: &[Worktree]) -> BTreeMap<String, String> {
    worktrees
        .iter()
        .filter_map(|tree| {
            let folder = std::path::Path::new(&tree.path).file_name()?.to_str()?;
            (folder != tree.project).then(|| (folder.to_string(), tree.project.clone()))
        })
        .collect()
}

/// A one-line plain-text answer.
fn plain(status: StatusCode, reason: &str) -> Response<RespBody> {
    let mut response = Response::new(full_body(format!("{reason}\n")));
    *response.status_mut() = status;
    response.headers_mut().insert(
        CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    response
}

/// The snapshot without its recent rows, as JSON. Every other field, the counts derived from the
/// rows included, is the full snapshot's.
fn slim_json(mut snapshot: Snapshot) -> String {
    snapshot.recent.clear();
    let json = serde_json::to_string(&snapshot).unwrap_or_default();
    // The emptied rows serialize as `"recent":[]` between two fields; a quote inside a value is
    // escaped, so the raw text appears once.
    match json.matches(",\"recent\":[]").count() {
        1 => json.replacen(",\"recent\":[]", "", 1),
        _ => serde_json::to_value(&snapshot)
            .map(|mut value| {
                if let Some(object) = value.as_object_mut() {
                    object.remove("recent");
                }
                value.to_string()
            })
            .unwrap_or_default(),
    }
}

/// One snapshot as a server-sent event.
fn event_of(snapshot: Snapshot, slim: bool) -> String {
    let json = if slim {
        slim_json(snapshot)
    } else {
        serde_json::to_string(&snapshot).unwrap_or_default()
    };
    format!("event: snapshot\ndata: {json}\n\n")
}

/// Sends one frame; false when the client is gone.
async fn send(
    tx: &mpsc::Sender<Result<Frame<Bytes>, crate::server::BodyCut>>,
    text: String,
) -> bool {
    tx.send(Ok(Frame::data(Bytes::from(text)))).await.is_ok()
}

/// Resolves once `drain` has left Serving; never when there is no drain to watch.
async fn drained(drain: Option<watch::Receiver<Phase>>) {
    let Some(mut drain) = drain else {
        return std::future::pending().await;
    };
    if drain
        .wait_for(|phase| *phase != Phase::Serving)
        .await
        .is_err()
    {
        std::future::pending::<()>().await;
    }
}

#[cfg(test)]
mod gzip_tests {
    use super::accepts_gzip;

    #[test]
    fn accept_encoding_allows_gzip_only_with_a_positive_quality() {
        assert!(accepts_gzip(Some("gzip")));
        assert!(accepts_gzip(Some("br, GZIP;q=0.5")));
        assert!(accepts_gzip(Some("*")));
        assert!(!accepts_gzip(Some("identity")));
        assert!(!accepts_gzip(Some("gzip;q=0")));
        assert!(!accepts_gzip(None));
    }
}

#[cfg(test)]
#[path = "../build_support/minify_plan.rs"]
mod minify_plan;

#[cfg(test)]
mod served_page_tests {
    use std::io::Read;
    use std::process::Command;

    use flate2::read::GzDecoder;
    use huddle_testkit::temp_home::TempHome;

    use super::{PAGE, PAGE_GZIP};

    fn served() -> String {
        let mut text = String::new();
        GzDecoder::new(PAGE_GZIP)
            .read_to_string(&mut text)
            .expect("the served bytes are gzip");
        text
    }

    fn script_of(page: &str) -> &str {
        let open = page.find("<script>\n").expect("a script tag") + "<script>\n".len();
        let close = page.rfind("</script>").expect("a closing script tag");
        &page[open..close]
    }

    #[test]
    fn e8_minified_page_parses_and_boots() {
        let served = served();
        assert!(served.starts_with("<!doctype html>"));
        assert!(served.contains("<title>Huddle Admin</title>"));
        // The markup is untouched, so every id the script reads is still in the page.
        let markup_start = PAGE.find("<body>").expect("body");
        let markup_end = PAGE.find("<script>").expect("script");
        assert!(served.contains(&PAGE[markup_start..markup_end]));
        // Minifying never grows the page.
        assert!(served.len() <= PAGE.len());

        // The same parse check the Merger runs: `node --check` on the joined script.
        let home = TempHome::new();
        let file = home.root().join("page-script.js");
        std::fs::write(&file, script_of(&served)).expect("write the script");
        match Command::new("node").arg("--check").arg(&file).output() {
            Ok(out) => assert!(
                out.status.success(),
                "node --check failed: {}",
                String::from_utf8_lossy(&out.stderr)
            ),
            Err(_) => eprintln!("node is not installed: parse check skipped"),
        }
    }
}
