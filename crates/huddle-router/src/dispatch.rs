//! huddle-router: dispatch.
//!
//! The router itself: it reads the model off the body, picks the lane or Anthropic, and wires
//! the entry handler, the CONNECT handler, the Anthropic path and the lanes together.

use crate::anthropic::{
    AnswerOutcome, AnswerTap, AnthropicPath, OutcomeHook, PassCtx, PassEvent, PassHook, PassWord,
    StreamedRoute, WholeRoute, is_model_request, pass_through_handler,
};
use crate::body::model_of;
use crate::connect::{ConnectCtx, Decrypt, TunnelTracker, connect_handler};
use crate::dashboard::{Dashboard, DashboardInputs};
use crate::events::{EventSink, RouteEvent, iso_utc_ms};
use crate::keycache::sources_for;
use crate::lane::{Lane, LaneError, Logger};
use crate::live::{Finish, LiveState, RequestStart};
use crate::server::{
    BODY_LIMIT, EntryCtx, Handler, ServerLimits, cut_response, entry_handler, receive,
};
use crate::sse::Usage;
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
}

impl FailStreak {
    /// Counts one failure at `now`. True when this failure is the third within a minute of its
    /// run and the last raise is five minutes old or more.
    pub fn fail(&mut self, now: u64) -> bool {
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

    /// Counts one success and resets the run. True only for the first success after a raise.
    pub fn ok(&mut self, _now: u64) -> bool {
        self.count = 0;
        if !self.raised {
            return false;
        }
        self.raised = false;
        true
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
/// where the metadata lines go, the route-event writer when events are on, and what the
/// dashboard reads from outside the router.
pub struct RouterConfig {
    pub env: Env,
    pub paths: Paths,
    pub port: u16,
    pub anthropic_url: hyper::Uri,
    pub log: Logger,
    pub events: Option<EventSink>,
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
    events: Option<EventSink>,
    live: Arc<LiveState>,
    dashboard: DashboardInputs,
    tunnels: Arc<TunnelTracker>,
}

impl Router {
    /// Lanes for the given enabled providers; the rest of the wiring is fixed.
    pub fn new(config: RouterConfig, enabled: &[String]) -> Arc<Router> {
        let log = config.log;
        let events = config.events.clone();
        // The event writer makes its own directory, but not its parents; the router home is
        // the router's to make. A failure leaves the writer its stderr line.
        if events.is_some() {
            let _ = std::fs::create_dir_all(config.paths.router_dir.join("events"));
        }
        let roots = os_roots(&*log);
        let client = Arc::new(UpstreamClient::new(UpstreamConfig {
            env: config.env.clone(),
            roots,
            idle: IDLE_TIMEOUT,
        }));
        let tunnels = TunnelTracker::new();
        let live = Arc::new(LiveState::new(now_ms()));

        // Every passed request writes one route event off the request path.
        let on_pass: Option<PassHook> = events.clone().map(|sink| {
            Arc::new(move |event: &PassEvent| {
                let error = match event.word {
                    PassWord::Finished => None,
                    PassWord::Cut(code) => Some(format!("stream cut ({})", code.as_str())),
                    PassWord::Error(code) => Some(format!("connection failed ({})", code.as_str())),
                    PassWord::Cancelled => Some("client closed".to_owned()),
                };
                sink.send(RouteEvent {
                    ts: iso_utc_ms(crate::events::now_ms()),
                    plugin: "huddle-router".to_owned(),
                    model: String::new(),
                    route: "anthropic-pass".to_owned(),
                    status: event.status,
                    latency_ms: event.ms,
                    usage: None,
                    error,
                    router_us: None,
                });
            }) as PassHook
        });

        // Inside a decrypted conversation every non-model request rides to the Anthropic
        // origin unchanged.
        let pass_through = pass_through_handler(Arc::new(PassCtx {
            client: Arc::clone(&client),
            target: config.anthropic_url.clone(),
            body_limit: BODY_LIMIT,
            note: pass_note(Arc::clone(&log)),
            on_pass,
        }));

        // One line per raise and one per recovery: the third failure in a minute names the
        // code and its fix hint, the first success after a raise names the recovery.
        let on_outcome: OutcomeHook = {
            let streak = Mutex::new(FailStreak::default());
            let log = Arc::clone(&log);
            Arc::new(move |outcome| {
                let now = now_ms();
                match outcome {
                    AnswerOutcome::Failed { code } | AnswerOutcome::Cut { code, .. } => {
                        if lock(&streak).fail(now) {
                            log(format!(
                                "huddle-router: Anthropic path failing ({}): {}",
                                code.as_str(),
                                fail_hint(code)
                            ));
                        }
                    }
                    AnswerOutcome::Finished { status, .. }
                        if status < 500 && lock(&streak).ok(now) =>
                    {
                        log("huddle-router: Anthropic path recovered".to_string());
                    }
                    _ => {}
                }
            })
        };

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
                                events.set_route("provider");
                                events.set_plugin(lane.provider().id);
                                events.set_asked_model(&model);
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
                            Route::Lane(provider) => {
                                events.set_route("provider");
                                events.set_plugin(provider.id);
                                events.set_asked_model(&model);
                                match lane_by_id(&router.lanes, provider.id) {
                                    Some(lane) => {
                                        events.track(provider.id, &model);
                                        lane.forward(parts, body, &model).await
                                    }
                                    None => cut_response(),
                                }
                            }
                            Route::Anthropic => {
                                events.set_route("anthropic");
                                events.set_plugin("huddle-router");
                                events.set_asked_model(&model);
                                events.set_served_model(&model);
                                events.track("anthropic", &model);
                                let answer = router.anthropic.forward_whole(parts, body).await;
                                (router.log)(format!(
                                    "{method} {path} model={} -> anthropic {}",
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
                events,
                live,
                dashboard: config.dashboard,
                tunnels,
            }
        })
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
                intercept: self.intercept_with_events(),
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
                        if let Some(events) = &router.events {
                            parts.extensions.insert(events.clone());
                        }
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
        let dashboard = Arc::new(Dashboard::new(
            Arc::clone(&self.live),
            self.dashboard.clone(),
            Arc::clone(&providers),
        ));
        entry_handler(Arc::new(EntryCtx {
            port: Some(self.port),
            connect,
            forward,
            providers,
            dashboard: Some(dashboard),
            dashboard_hosts: Arc::clone(&self.dashboard.hosts),
        }))
    }

    /// The decrypted conversation's handler. A model request carries its context and the event
    /// sink from the moment it arrives, before its body is read, as a plain-HTTP one does.
    fn intercept_with_events(self: &Arc<Self>) -> Handler {
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
                if let Some(events) = &router.events {
                    req.extensions_mut().insert(events.clone());
                }
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
    /// The model the event names.
    pub served_model: Mutex<String>,
    /// Set by the route choice.
    pub asked_model: Mutex<String>,
    pub usage: Mutex<Option<Usage>>,
    pub error: Mutex<Option<String>>,
    /// `provider`, `anthropic` or `refused`.
    pub route: Mutex<String>,
    pub plugin: Mutex<String>,
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
        live.state.finish(
            *id,
            Finish {
                status,
                error: lock(&self.error).clone(),
                usage: lock(&self.usage).clone(),
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

    /// Milliseconds since the entry handler saw the request.
    pub fn latency_ms(&self) -> u64 {
        self.received
            .map_or(0, |received| received.elapsed().as_millis() as u64)
    }

    /// The request's route event, as of now.
    pub fn event(&self, status: u16, latency_ms: u64) -> RouteEvent {
        RouteEvent {
            ts: iso_utc_ms(crate::events::now_ms()),
            plugin: lock(&self.plugin).clone(),
            model: lock(&self.served_model).clone(),
            route: lock(&self.route).clone(),
            status,
            latency_ms,
            usage: lock(&self.usage).clone(),
            error: lock(&self.error).clone(),
            router_us: self.sent_us.get().copied(),
        }
    }

    fn set_served_model(&self, model: &str) {
        *lock(&self.served_model) = model.to_owned();
    }

    fn set_asked_model(&self, model: &str) {
        *lock(&self.asked_model) = model.to_owned();
    }

    fn set_route(&self, route: &str) {
        *lock(&self.route) = route.to_owned();
    }

    fn set_plugin(&self, plugin: &str) {
        *lock(&self.plugin) = plugin.to_owned();
    }

    fn set_error(&self, error: Option<String>) {
        *lock(&self.error) = error;
    }

    fn set_usage(&self, usage: Option<Usage>) {
        *lock(&self.usage) = usage;
    }
}

/// The request's context and its event writer, read off the request's extensions. The
/// `emit`s are no-ops when the request carries neither, so answers on eventless routers and
/// lanes built outside the dispatch cost nothing.
#[derive(Clone)]
pub struct RequestEvents {
    context: Option<Arc<RequestContext>>,
    sink: Option<EventSink>,
}

impl RequestEvents {
    /// Reads the context and the sink a request carries.
    pub fn from_parts(parts: &Parts) -> RequestEvents {
        RequestEvents {
            context: parts.extensions.get::<Arc<RequestContext>>().cloned(),
            sink: parts.extensions.get::<EventSink>().cloned(),
        }
    }

    /// True when the request is followed: its end reaches the live state and the event file.
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

    /// Marks the first attempt leaving.
    pub fn mark_sent(&self) {
        if let Some(context) = &self.context {
            context.mark_sent();
        }
    }

    /// Ends the request: it leaves the live state and, with a sink, writes its route event.
    /// Nothing happens without the context.
    pub fn emit(&self, status: u16, error: Option<String>) {
        let Some(context) = &self.context else {
            return;
        };
        context.set_error(error);
        let latency_ms = context.latency_ms();
        context.finish_live(status);
        if let Some(sink) = &self.sink {
            sink.send(context.event(status, latency_ms));
        }
    }

    pub fn set_route(&self, route: &str) {
        if let Some(context) = &self.context {
            context.set_route(route);
        }
    }

    pub fn set_plugin(&self, plugin: &str) {
        if let Some(context) = &self.context {
            context.set_plugin(plugin);
        }
    }

    pub fn set_asked_model(&self, model: &str) {
        if let Some(context) = &self.context {
            context.set_asked_model(model);
        }
    }

    pub fn set_served_model(&self, model: &str) {
        if let Some(context) = &self.context {
            context.set_served_model(model);
        }
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

/// The lane serving this provider id, read fresh.
fn lane_by_id(lanes: &RwLock<Arc<Vec<Arc<Lane>>>>, id: &'static str) -> Option<Arc<Lane>> {
    lanes
        .read()
        .unwrap_or_else(PoisonError::into_inner)
        .iter()
        .find(|lane| lane.provider().id == id)
        .cloned()
}

/// The lane logger as a borrowed-line note: one borrowed line in, one owned line out.
fn pass_note(log: Logger) -> Arc<dyn Fn(&str) + Send + Sync> {
    Arc::new(move |line: &str| log(line.to_string()))
}

/// The fix hint a failing Anthropic path names, by the failure's code.
fn fail_hint(code: FailureCode) -> &'static str {
    match code {
        FailureCode::ECert => {
            "a certificate check failed; huddle-router never routes through another router, so restart sessions started before an update"
        }
        FailureCode::EConnRefused => "the upstream refused the connection; check HTTPS_PROXY",
        FailureCode::ETimedOut => "the upstream timed out; check the network",
        _ => "see the router log",
    }
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
