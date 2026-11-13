//! huddle-router: dispatch.
//!
//! The router itself: it reads the model off the body, picks the lane or Anthropic, and wires
//! the entry handler, the CONNECT handler, the Anthropic path and the lanes together.

use crate::answer_facts::{self, AnswerFacts};
use crate::anthropic::{
    AnswerOutcome, AnswerTap, AnthropicPath, OutcomeHook, PassCtx, StreamedRoute, UpstreamFailure,
    WholeRoute, is_model_request, pass_through_handler,
};
use crate::body::model_of;
use crate::connect::{ConnectCtx, Decrypt, TunnelTracker, connect_handler};
use crate::crashlog::is_event;
use crate::dashboard::{Dashboard, DashboardInputs, HISTORY_PATH, LIVE_PATH, STATE_PATH};
use crate::keycache::sources_for;
use crate::lane::{Lane, LaneError, Logger};
use crate::live::{Finish, HistorySink, LiveState, RequestStart};
pub use crate::probe::ProbeTiming;
use crate::probe::{self, ProbeHandle, ProbeTarget};
use crate::quota::{QuotaFetcher, catalog_urls};
use crate::router_events::{PathFailing, PathRecovery};
use crate::server::{
    BODY_LIMIT, EntryCtx, Handler, ServerLimits, cut_response, entry_handler, receive,
};
use crate::sse::Usage;
use crate::trace::Trace;
use crate::upstream::{FailureCode, IDLE_TIMEOUT, UpstreamClient, UpstreamConfig};
use huddle_catalog::providers::{provider_by_id, provider_ids};
use huddle_catalog::routing::{ResolvedIds, Route, decide_resolved};
use huddle_config::{env::Env, paths::Paths};
use huddle_pki::leaf::LeafCache;
use hyper::Method;
use hyper::body::Bytes;
use hyper::header::HeaderMap;
use hyper::http::request::Parts;
use rustls::RootCertStore;
use std::sync::{Arc, Mutex, OnceLock, PoisonError, RwLock, Weak};
use std::time::{Instant, SystemTime, UNIX_EPOCH};

/// The path of a model request the dashboard lists.
const MESSAGES_PATH: &str = "/v1/messages";

/// The header Claude Code names its session in.
const SESSION_HEADER: &str = "x-claude-code-session-id";

/// How close together three failures mean a failing Anthropic path.
const FAIL_WINDOW_MS: u64 = 60_000;

/// How long one raise keeps the next one quiet.
const RAISE_QUIET_MS: u64 = 5 * 60_000;

/// The Anthropic path's failure streak: three upstream failures within a minute mean the path
/// is failing, and the first success after a raise means it recovered. A few integers the
/// caller keeps under its own lock, so counting stays off the request path.
#[derive(Default)]
pub struct FailStreak {
    count: u32,
    first_at: u64,
    raised: bool,
    raised_at: u64,
    /// Failures since the last success, and when the first of them came.
    run_failures: u32,
    run_since: u64,
}

/// What a recovery reports: how long the path was down and how many requests failed.
#[derive(Debug, PartialEq, Eq)]
pub struct Recovery {
    pub down_ms: u64,
    pub failed: u32,
}

impl FailStreak {
    /// Counts one failure at `now`. True when this failure is the third within a minute of its
    /// run and the last raise is five minutes old or more.
    pub fn fail(&mut self, now: u64) -> bool {
        if self.run_failures == 0 {
            self.run_since = now;
        }
        self.run_failures += 1;
        if now.saturating_sub(self.first_at) > FAIL_WINDOW_MS {
            self.count = 1;
            self.first_at = now;
        } else {
            self.count += 1;
        }
        if self.count != 3 {
            return false;
        }
        if self.raised && now.saturating_sub(self.raised_at) < RAISE_QUIET_MS {
            return false;
        }
        self.raised = true;
        self.raised_at = now;
        true
    }

    /// When the failing run began and how many requests it has cost, while the path is raised.
    pub fn failing(&self) -> Option<(u64, u32)> {
        self.raised.then_some((self.run_since, self.run_failures))
    }

    /// Counts one success and resets the run. Some only for the first success after a raise.
    pub fn ok(&mut self, now: u64) -> Option<Recovery> {
        self.count = 0;
        let recovery = Recovery {
            down_ms: now.saturating_sub(self.run_since),
            failed: self.run_failures,
        };
        self.run_failures = 0;
        if !self.raised {
            return None;
        }
        self.raised = false;
        Some(recovery)
    }
}

/// `6m 56s`, `1h 02m 03s`, `12s`.
fn human_duration(ms: u64) -> String {
    let secs = ms / 1000;
    let (hours, minutes, seconds) = (secs / 3600, secs % 3600 / 60, secs % 60);
    if hours > 0 {
        format!("{hours}h {minutes:02}m {seconds:02}s")
    } else if minutes > 0 {
        format!("{minutes}m {seconds:02}s")
    } else {
        format!("{seconds}s")
    }
}

/// The router's timings a test may shorten: the probe's rounds and the upstream silence budget.
#[derive(Debug, Clone, Copy)]
pub struct Tuning {
    pub probe: ProbeTiming,
    pub idle: std::time::Duration,
}

impl Default for Tuning {
    fn default() -> Tuning {
        Tuning {
            probe: ProbeTiming::default(),
            idle: IDLE_TIMEOUT,
        }
    }
}

/// The OS trust store as the upstream client's roots; a certificate that does not parse is
/// skipped and logged.
pub fn os_roots(log: &dyn Fn(String)) -> RootCertStore {
    let loaded = rustls_native_certs::load_native_certs();
    for error in &loaded.errors {
        log(format!(
            "huddle-router: reading the OS trust store failed: {error}"
        ));
    }
    let mut roots = RootCertStore::empty();
    roots.add_parsable_certificates(loaded.certs);
    roots
}

/// The router's fixed wiring: the environment, the paths, the port, the Anthropic origin,
/// where the metadata lines go, the history sink when finished requests are recorded, and what
/// the dashboard reads from outside the router.
pub struct RouterConfig {
    pub env: Env,
    pub paths: Paths,
    pub port: u16,
    pub anthropic_url: hyper::Uri,
    pub log: Logger,
    pub history: Option<Arc<dyn HistorySink>>,
    pub dashboard: DashboardInputs,
}

/// The router: one set of provider lanes, the Anthropic path for every model no lane claims,
/// and the entry wiring that serves both.
pub struct Router {
    lanes: Arc<RwLock<Arc<Vec<Arc<Lane>>>>>,
    client: Arc<UpstreamClient>,
    anthropic: Arc<AnthropicPath>,
    whole_route: WholeRoute,
    env: Env,
    paths: Paths,
    port: u16,
    log: Logger,
    live: Arc<LiveState>,
    dashboard: DashboardInputs,
    tunnels: Arc<TunnelTracker>,
}

impl Router {
    /// Lanes for the given enabled providers; the rest of the wiring is fixed.
    pub fn new(config: RouterConfig, enabled: &[String]) -> Arc<Router> {
        Router::with_tuning(config, enabled, Tuning::default())
    }

    /// Like `new`, with the probe and silence timings set by the caller.
    pub fn with_tuning(config: RouterConfig, enabled: &[String], tuning: Tuning) -> Arc<Router> {
        let live = Arc::new(match config.history.clone() {
            Some(sink) => LiveState::with_sink(now_ms(), sink),
            None => LiveState::new(now_ms()),
        });
        let log = events_noted(config.log, &live);
        let roots = os_roots(&*log);
        let client = Arc::new(UpstreamClient::new(UpstreamConfig {
            env: config.env.clone(),
            roots,
            idle: tuning.idle,
        }));
        let tunnels = TunnelTracker::new();

        // One line per raise and one per recovery: the third failure in a minute names the
        // code, the phase and its fix hint, the first success after a raise names the recovery.
        let on_outcome = failure_hook(&log, &config.anthropic_url, tuning.probe, Arc::clone(&live));

        // Inside a decrypted conversation every non-model request rides to the Anthropic
        // origin unchanged.
        let pass_through = pass_through_handler(Arc::new(PassCtx {
            client: Arc::clone(&client),
            target: config.anthropic_url.clone(),
            body_limit: BODY_LIMIT,
            note: pass_note(Arc::clone(&log)),
            on_outcome: Some(Arc::clone(&on_outcome)),
        }));

        let lanes = Arc::new(RwLock::new(Arc::new(build_lanes(
            &config.env,
            &config.paths,
            &client,
            &log,
            enabled,
        ))));

        // The route closures read the lane set through the router's own weak handle, so a
        // reload swaps what they see and the router is never kept alive by a request.
        Arc::new_cyclic(|router: &Weak<Router>| {
            let is_anthropic: Arc<dyn Fn(&str) -> bool + Send + Sync> = {
                let lanes = Arc::clone(&lanes);
                Arc::new(move |model: &str| {
                    let (enabled, resolved) = lane_names(&lanes);
                    matches!(
                        decide_resolved(model, &enabled, &resolved),
                        Route::Anthropic
                    )
                })
            };
            let lane_streams: Arc<dyn Fn(&str) -> bool + Send + Sync> = {
                let lanes = Arc::clone(&lanes);
                Arc::new(move |model: &str| {
                    let (enabled, resolved) = lane_names(&lanes);
                    match decide_resolved(model, &enabled, &resolved) {
                        Route::Lane(provider) => provider.strip.is_empty(),
                        Route::Anthropic => false,
                    }
                })
            };
            let streamed_route: StreamedRoute = {
                let router = Weak::clone(router);
                Arc::new(move |parts, body, model| {
                    let router = Weak::clone(&router);
                    Box::pin(async move {
                        let Some(router) = router.upgrade() else {
                            return cut_response();
                        };
                        match lane_for(&router, &model) {
                            Some(lane) => {
                                let events = RequestEvents::from_parts(&parts);
                                events.track(lane.provider().id, &model);
                                lane.forward_streamed(parts, body, &model).await
                            }
                            // The lane left between the head and this call; the client retries.
                            None => cut_response(),
                        }
                    })
                })
            };
            let whole_route: WholeRoute = {
                let router = Weak::clone(router);
                Arc::new(move |parts, body| {
                    let router = Weak::clone(&router);
                    Box::pin(async move {
                        let Some(router) = router.upgrade() else {
                            return cut_response();
                        };
                        let model = model_of(&body);
                        let method = parts.method.as_str().to_owned();
                        let path = parts.uri.path().to_owned();
                        let (enabled, resolved) = lane_names(&router.lanes);
                        let events = RequestEvents::from_parts(&parts);
                        match decide_resolved(&model, &enabled, &resolved) {
                            Route::Lane(provider) => match lane_by_id(&router.lanes, provider.id) {
                                Some(lane) => {
                                    events.track(provider.id, &model);
                                    lane.forward(parts, body, &model).await
                                }
                                None => cut_response(),
                            },
                            Route::Anthropic => {
                                events.track("anthropic", &model);
                                let answer = router.anthropic.forward_whole(parts, body).await;
                                let why = answer
                                    .extensions()
                                    .get::<UpstreamFailure>()
                                    .map(|f| {
                                        format!(" error {} {}", f.code.as_str(), f.trace.line())
                                    })
                                    .unwrap_or_default();
                                (router.log)(format!(
                                    "{method} {path} model={} -> anthropic {}{why}",
                                    model_or_dash(&model),
                                    answer.status().as_u16(),
                                ));
                                answer
                            }
                        }
                    })
                })
            };
            let anthropic = Arc::new(AnthropicPath {
                client: Arc::clone(&client),
                target: config.anthropic_url.clone(),
                body_limit: BODY_LIMIT,
                is_anthropic,
                lane_streams,
                streamed_route,
                whole_route: Arc::clone(&whole_route),
                pass_through,
                on_outcome: Some(on_outcome),
            });
            Router {
                lanes,
                client,
                anthropic,
                whole_route,
                env: config.env,
                paths: config.paths,
                port: config.port,
                log,
                live,
                dashboard: config.dashboard,
                tunnels,
            }
        })
    }

    /// The router's log: every event line it takes also reaches the dashboard's recent events.
    pub fn logger(&self) -> Logger {
        Arc::clone(&self.log)
    }

    /// Files an event line the caller already logged, such as the start line written before the
    /// router existed, in the dashboard's recent events.
    pub fn note_event(&self, line: &str) {
        self.live.note_event(now_ms(), line);
    }

    /// The router's entry handler: CONNECT first, then the Host guard, then the health answer,
    /// then the forwarded request. The handler holds the router, so serving keeps it alive.
    pub fn handler(self: &Arc<Self>) -> Handler {
        let connect = connect_handler(Arc::new(ConnectCtx {
            env: self.env.clone(),
            dial: None,
            tunnels: Arc::clone(&self.tunnels),
            decrypt: Some(Arc::new(Decrypt {
                leaf: Arc::new(LeafCache::new(
                    self.paths.ca_dir.clone(),
                    pass_note(Arc::clone(&self.log)),
                )),
                intercept: self.intercept_with_context(),
                limits: ServerLimits::default(),
            })),
        }));
        let router = Arc::clone(self);
        let forward: Handler = Arc::new(move |req| {
            let router = Arc::clone(&router);
            Box::pin(async move {
                let started = Instant::now();
                match receive(req, BODY_LIMIT).await {
                    Ok((mut parts, body)) => {
                        let context = router.context(
                            started,
                            &parts.method,
                            parts.uri.path(),
                            &parts.headers,
                        );
                        parts.extensions.insert(context);
                        (router.whole_route)(parts, body).await
                    }
                    Err(answer) => answer,
                }
            })
        });
        let providers: Arc<dyn Fn() -> Vec<String> + Send + Sync> = {
            let router = Arc::clone(self);
            Arc::new(move || router.providers())
        };
        let mut inputs = self.dashboard.clone();
        let tunnels = Arc::clone(&self.tunnels);
        inputs.connections = Arc::new(move || tunnels.count());
        let quota = QuotaFetcher::new(
            Arc::clone(&self.client),
            {
                let (env, paths) = (self.env.clone(), self.paths.clone());
                Arc::new(move |provider| sources_for(provider, &env, &paths))
            },
            catalog_urls(&self.env),
        );
        let dashboard = Arc::new(
            Dashboard::new(Arc::clone(&self.live), inputs, Arc::clone(&providers))
                .with_quota(quota),
        );
        let entry = entry_handler(Arc::new(EntryCtx {
            port: Some(self.port),
            connect,
            forward,
            providers,
            dashboard: Some(dashboard),
            dashboard_hosts: Arc::clone(&self.dashboard.hosts),
        }));
        logged_admin(entry, Arc::clone(&self.log))
    }

    /// The decrypted conversation's handler. A model request carries its context from the
    /// moment it arrives, before its body is read, as a plain-HTTP one does.
    fn intercept_with_context(self: &Arc<Self>) -> Handler {
        let intercept = Arc::clone(&self.anthropic).intercept_handler();
        let router = Arc::clone(self);
        Arc::new(move |mut req| {
            if is_model_request(&req) {
                let context = router.context(
                    Instant::now(),
                    req.method(),
                    req.uri().path(),
                    req.headers(),
                );
                req.extensions_mut().insert(context);
            }
            intercept(req)
        })
    }

    /// The context of a request that just arrived. A `POST /v1/messages` also enters the live
    /// state once its lane is known; `count_tokens` and every other request stay off the page.
    fn context(
        &self,
        received: Instant,
        method: &Method,
        path: &str,
        headers: &HeaderMap,
    ) -> Arc<RequestContext> {
        let live = (method == Method::POST && path == MESSAGES_PATH).then(|| LiveEntry {
            state: Arc::clone(&self.live),
            session: session_of(headers),
            id: OnceLock::new(),
        });
        Arc::new(RequestContext {
            received: Some(received),
            live,
            ..RequestContext::default()
        })
    }

    /// The CONNECT tunnel counter, for the update drain.
    pub fn tunnels(&self) -> Arc<TunnelTracker> {
        Arc::clone(&self.tunnels)
    }

    /// The enabled providers' ids, in lane order.
    pub fn providers(&self) -> Vec<String> {
        self.lanes
            .read()
            .unwrap_or_else(PoisonError::into_inner)
            .iter()
            .map(|lane| lane.provider().id.to_string())
            .collect()
    }

    /// Swaps the lanes for the given enabled providers. An empty list changes nothing: the old
    /// lanes keep serving. A lane that stays enabled keeps its state, so its key cache
    /// survives; only lanes for newly enabled providers are built. A request already routed
    /// keeps its lane.
    pub fn reload(self: &Arc<Self>, names: &[String]) {
        if names.is_empty() {
            return;
        }
        let fresh = build_lanes(&self.env, &self.paths, &self.client, &self.log, names);
        let old = self
            .lanes
            .read()
            .unwrap_or_else(PoisonError::into_inner)
            .clone();
        let mut lanes = Vec::new();
        for id in provider_ids() {
            if !names.iter().any(|name| name == id) {
                continue;
            }
            if let Some(kept) = old.iter().find(|lane| lane.provider().id == id) {
                lanes.push(Arc::clone(kept));
                continue;
            }
            if let Some(lane) = fresh.iter().find(|lane| lane.provider().id == id) {
                lanes.push(Arc::clone(lane));
            }
        }
        *self.lanes.write().unwrap_or_else(PoisonError::into_inner) = Arc::new(lanes);
    }
}

/// Per-request facts every part of the path fills in; lives in the request's extensions.
#[derive(Default)]
pub struct RequestContext {
    /// Set when the entry handler first saw the request.
    pub received: Option<Instant>,
    /// `router_us`: microseconds from `received` to the first attempt leaving.
    pub sent_us: OnceLock<u64>,
    pub usage: Mutex<Option<Usage>>,
    pub error: Mutex<Option<String>>,
    /// The Anthropic answer's `request-id` and plan numbers.
    pub answer_facts: Mutex<AnswerFacts>,
    /// Set for a request the dashboard lists.
    live: Option<LiveEntry>,
}

/// A request's place in the live state: filled in once the route is known.
struct LiveEntry {
    state: Arc<LiveState>,
    session: Option<String>,
    id: OnceLock<u64>,
}

impl RequestContext {
    /// Enters the request in the live state under its lane; a second call does nothing.
    fn track(&self, lane: &str, model: &str) {
        let Some(live) = &self.live else {
            return;
        };
        live.id.get_or_init(|| {
            live.state.start(RequestStart {
                session: live.session.clone(),
                model: model.to_owned(),
                lane: lane.to_owned(),
                now_ms: now_ms(),
            })
        });
    }

    /// Leaves the live state with the request's status, error and usage as they stand.
    fn finish_live(&self, status: u16) {
        let Some(live) = &self.live else {
            return;
        };
        let Some(id) = live.id.get() else {
            return;
        };
        let facts = lock(&self.answer_facts).clone();
        live.state.finish(
            *id,
            Finish {
                status,
                error: lock(&self.error).clone(),
                usage: lock(&self.usage).clone(),
                upstream_id: facts.upstream_id,
                plan: facts.plan,
                router_ms: self.sent_us.get().map(|us| us / 1000),
                now_ms: now_ms(),
            },
        );
    }

    /// Call just before the first attempt; a later call for a retry does nothing.
    pub fn mark_sent(&self) {
        let Some(received) = self.received else {
            return;
        };
        let _ = self.sent_us.set(received.elapsed().as_micros() as u64);
    }

    fn set_error(&self, error: Option<String>) {
        *lock(&self.error) = error;
    }

    fn set_usage(&self, usage: Option<Usage>) {
        *lock(&self.usage) = usage;
    }
}

/// The request's context, read off the request's extensions. `emit` is a no-op without it, so
/// lanes built outside the dispatch cost nothing.
#[derive(Clone)]
pub struct RequestEvents {
    context: Option<Arc<RequestContext>>,
}

impl RequestEvents {
    /// Reads the context a request carries.
    pub fn from_parts(parts: &Parts) -> RequestEvents {
        RequestEvents {
            context: parts.extensions.get::<Arc<RequestContext>>().cloned(),
        }
    }

    /// True when the request is followed: its end reaches the live state and the history.
    pub fn active(&self) -> bool {
        self.context.is_some()
    }

    /// Enters the request in the live state under this lane and model.
    pub fn track(&self, lane: &str, model: &str) {
        if let Some(context) = &self.context {
            context.track(lane, model);
        }
    }

    /// The answer's tap with the live feed in front of it: the feed sees every frame, then
    /// `inner` does. Without a live entry the tap is `inner` unchanged.
    pub fn live_tap(&self, inner: Option<Box<dyn AnswerTap>>) -> Option<Box<dyn AnswerTap>> {
        let live = self
            .context
            .as_ref()
            .and_then(|context| context.live.as_ref());
        let Some((live, id)) = live.and_then(|live| Some((live, *live.id.get()?))) else {
            return inner;
        };
        Some(Box::new(LiveTap {
            state: Arc::clone(&live.state),
            id,
            seen: false,
            inner,
        }))
    }

    /// Keeps the `request-id` and plan numbers an Anthropic answer's head carries.
    pub fn note_answer(&self, headers: &HeaderMap) {
        if let Some(context) = &self.context {
            *lock(&context.answer_facts) = answer_facts::read(headers);
        }
    }

    /// Marks the first attempt leaving.
    pub fn mark_sent(&self) {
        if let Some(context) = &self.context {
            context.mark_sent();
        }
    }

    /// Ends the request: it leaves the live state and the history sink gets its row.
    /// Nothing happens without the context.
    pub fn emit(&self, status: u16, error: Option<String>) {
        let Some(context) = &self.context else {
            return;
        };
        context.set_error(error);
        context.finish_live(status);
    }

    pub fn set_usage(&self, usage: Option<Usage>) {
        if let Some(context) = &self.context {
            context.set_usage(usage);
        }
    }
}

/// Reads every frame of an answer for the live feed, then hands it to the usage tap behind it.
struct LiveTap {
    state: Arc<LiveState>,
    id: u64,
    seen: bool,
    inner: Option<Box<dyn AnswerTap>>,
}

impl AnswerTap for LiveTap {
    fn chunk(&mut self, chunk: &Bytes) {
        if self.seen {
            self.state.byte(self.id, now_ms());
        } else {
            self.seen = true;
            self.state.first_byte(self.id, now_ms());
        }
        if let Some(inner) = &mut self.inner {
            inner.chunk(chunk);
        }
    }

    fn end(&mut self) {
        if let Some(inner) = &mut self.inner {
            inner.end();
        }
    }
}

/// The session id header's value, when the request carries a usable one.
fn session_of(headers: &HeaderMap) -> Option<String> {
    headers
        .get(SESSION_HEADER)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
}

/// The lanes for the given enabled providers, in catalog order. A lane that cannot be built is
/// skipped with one log line; a name the catalog does not know is logged.
fn build_lanes(
    env: &Env,
    paths: &Paths,
    client: &Arc<UpstreamClient>,
    log: &Logger,
    names: &[String],
) -> Vec<Arc<Lane>> {
    let mut lanes = Vec::new();
    for id in provider_ids() {
        if !names.iter().any(|name| name == id) {
            continue;
        }
        let Some(provider) = provider_by_id(id) else {
            continue;
        };
        match Lane::build(
            provider,
            env,
            sources_for(provider, env, paths),
            Arc::clone(client),
            Arc::clone(log),
        ) {
            Ok(lane) => lanes.push(lane),
            Err(LaneError { name, message }) => {
                log(format!("huddle-router: skipping {name}: {name}: {message}"));
            }
        }
    }
    for name in names {
        if provider_by_id(name).is_none() {
            log(format!("huddle-router: no provider named {name}"));
        }
    }
    lanes
}

/// The lane table a routing decision reads: the lanes' provider ids and their resolved model
/// ids, read fresh so a reload swaps what the next request sees.
fn lane_names(lanes: &RwLock<Arc<Vec<Arc<Lane>>>>) -> (Vec<&'static str>, Vec<ResolvedIds>) {
    let lanes = lanes.read().unwrap_or_else(PoisonError::into_inner);
    (
        lanes.iter().map(|lane| lane.provider().id).collect(),
        lanes.iter().map(|lane| lane.resolved_ids()).collect(),
    )
}

/// The lane this model goes to now, or none when the decision is Anthropic or the lane left in
/// a reload.
fn lane_for(router: &Router, model: &str) -> Option<Arc<Lane>> {
    let (enabled, resolved) = lane_names(&router.lanes);
    match decide_resolved(model, &enabled, &resolved) {
        Route::Lane(provider) => lane_by_id(&router.lanes, provider.id),
        Route::Anthropic => None,
    }
}

/// Logs every request to `/huddle-router/*` as `<method> <path> -> <status>`, except the
/// dashboard's own reads of the state, the live feed and the history. Headers, bodies and
/// queries never reach the line.
fn logged_admin(inner: Handler, log: Logger) -> Handler {
    Arc::new(move |req| {
        let inner = Arc::clone(&inner);
        let log = Arc::clone(&log);
        let path = req.uri().path().to_owned();
        let method = req.method().as_str().to_owned();
        let logged = path.starts_with("/huddle-router/")
            && !matches!(path.as_str(), STATE_PATH | LIVE_PATH | HISTORY_PATH);
        Box::pin(async move {
            let answer = inner(req).await;
            if logged {
                log(format!("{method} {path} -> {}", answer.status().as_u16()));
            }
            answer
        })
    })
}

/// The lane serving this provider id, read fresh.
fn lane_by_id(lanes: &RwLock<Arc<Vec<Arc<Lane>>>>, id: &'static str) -> Option<Arc<Lane>> {
    lanes
        .read()
        .unwrap_or_else(PoisonError::into_inner)
        .iter()
        .find(|lane| lane.provider().id == id)
        .cloned()
}

/// `inner` with every event line also filed in the live state, stamped with the router's clock.
fn events_noted(inner: Logger, live: &Arc<LiveState>) -> Logger {
    let live = Arc::clone(live);
    Arc::new(move |line: String| {
        if is_event(&line) {
            live.note_event(now_ms(), &line);
        }
        inner(line);
    })
}

/// The lane logger as a borrowed-line note: one borrowed line in, one owned line out.
fn pass_note(log: Logger) -> Arc<dyn Fn(&str) + Send + Sync> {
    Arc::new(move |line: &str| log(line.to_string()))
}

/// The fix hint a failing Anthropic path names: by the code where it says more than the phase.
fn fail_hint(code: FailureCode, trace: &Trace) -> &'static str {
    match code {
        FailureCode::ECert => {
            "a certificate check failed; huddle-router never routes through another router, so restart sessions started before an update"
        }
        FailureCode::EConnRefused => "the upstream refused the connection; check HTTPS_PROXY",
        _ => trace.phase.hint(),
    }
}

/// What the failure hook keeps between outcomes: the streak and the probe that runs while the
/// path fails.
struct PathWatch {
    streak: FailStreak,
    probe: Option<ProbeHandle>,
}

/// The hook every Anthropic outcome passes through. A raise starts the probe and logs the
/// failing line; a recovery stops the probe and logs how long and how many.
fn failure_hook(
    log: &Logger,
    target: &hyper::Uri,
    timing: ProbeTiming,
    live: Arc<LiveState>,
) -> OutcomeHook {
    let probe_target = probe_target_of(target);
    let watch = Mutex::new(PathWatch {
        streak: FailStreak::default(),
        probe: None,
    });
    let log = Arc::clone(log);
    Arc::new(move |outcome| {
        let now = now_ms();
        let line = {
            let mut watch = lock(&watch);
            match outcome {
                AnswerOutcome::Failed { code, trace } => {
                    live.upstream_failed(trace.phase.as_str());
                    let line = raise(&mut watch, now, code, trace);
                    start_probe_on_raise(&mut watch, &line, &probe_target, timing, &log);
                    show_failing(&watch, &live, code, &trace);
                    line
                }
                AnswerOutcome::Cut { code, .. } => {
                    let trace = Trace::body();
                    live.upstream_failed(trace.phase.as_str());
                    let line = raise(&mut watch, now, code, trace);
                    start_probe_on_raise(&mut watch, &line, &probe_target, timing, &log);
                    show_failing(&watch, &live, code, &trace);
                    line
                }
                AnswerOutcome::Finished { status, trace, .. } if status < 500 => {
                    watch.streak.ok(now).map(|recovery| {
                        watch.probe = None;
                        live.path_recovered(PathRecovery {
                            at_ms: now,
                            after_ms: recovery.down_ms,
                            failed: recovery.failed,
                        });
                        format!(
                            "huddle-router: Anthropic path recovered after {}; {} requests failed; now via {}",
                            human_duration(recovery.down_ms),
                            recovery.failed,
                            trace.route()
                        )
                    })
                }
                _ => None,
            }
        };
        if let Some(line) = line {
            log(line);
        }
    })
}

/// The dashboard's view of a path that is raised: updated with each further failure.
fn show_failing(watch: &PathWatch, live: &LiveState, code: FailureCode, trace: &Trace) {
    if let Some((since_ms, failed)) = watch.streak.failing() {
        live.path_failing(PathFailing {
            since_ms,
            code: code.as_str().to_owned(),
            phase: trace.phase.as_str().to_owned(),
            peer: trace.peer.map(|peer| peer.to_string()),
            failed,
            hint: fail_hint(code, trace).to_owned(),
        });
    }
}

/// A raise starts the one probe task that runs until the path recovers.
fn start_probe_on_raise(
    watch: &mut PathWatch,
    raised: &Option<String>,
    target: &Option<ProbeTarget>,
    timing: ProbeTiming,
    log: &Logger,
) {
    if raised.is_some() {
        watch.probe = target
            .clone()
            .and_then(|target| probe::start(target, timing, Arc::clone(log)));
    }
}

/// Counts one failure; the failing line when it raises.
fn raise(watch: &mut PathWatch, now: u64, code: FailureCode, trace: Trace) -> Option<String> {
    watch.streak.fail(now).then(|| {
        format!(
            "huddle-router: Anthropic path failing ({} {}): {}",
            code.as_str(),
            trace.place(),
            fail_hint(code, &trace)
        )
    })
}

/// The host and port the probe looks at: the Anthropic origin's own.
fn probe_target_of(target: &hyper::Uri) -> Option<ProbeTarget> {
    let host = target.host()?.to_owned();
    let port = target
        .port_u16()
        .unwrap_or(if target.scheme_str() == Some("http") {
            80
        } else {
            443
        });
    Some(ProbeTarget { host, port })
}

/// The model on a log line, or `-` when the body named none.
fn model_or_dash(model: &str) -> &str {
    if model.is_empty() { "-" } else { model }
}

/// The router's clock in milliseconds since the epoch.
pub(crate) fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|elapsed| elapsed.as_millis() as u64)
        .unwrap_or(0)
}

/// A lock's guard, past a poisoned lock: the data is a few integers, sane to keep using.
fn lock<T>(value: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    value.lock().unwrap_or_else(PoisonError::into_inner)
}
