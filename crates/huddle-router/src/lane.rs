//! huddle-router: lane.
//!
//! One provider's route: the lane is built from the catalog and the environment, and every
//! request goes to the provider with the provider's key set and its body as it came.

use std::sync::{Arc, Mutex, MutexGuard, PoisonError};

use http_body_util::BodyExt;
use huddle_catalog::providers::Provider;
use huddle_catalog::routing::ResolvedIds;
use huddle_config::env::Env;
use huddle_keystore::port::SecretString;
use hyper::body::Bytes;
use hyper::header::{
    ACCEPT_ENCODING, AUTHORIZATION, CONTENT_ENCODING, CONTENT_LENGTH, HeaderMap, HeaderName,
    HeaderValue,
};
use hyper::http::request::Parts;
use hyper::{Response, StatusCode, Uri};

use crate::adapt::adapt;
use crate::anthropic::{
    AnswerOutcome, AnswerTap, OutcomeHook, OutcomeWatch, StreamShape, event_of, is_event_stream,
    stream_answer,
};
use crate::dispatch::RequestEvents;
use crate::keycache::{KEY_BUDGET, KeyAnswer, KeyCache, KeySources, STORE_SHARE};
use crate::refusal::{
    INSPECT_LIMIT, Refusal, RefusalNames, Replacement, error_reason, key_timeout_answer,
    may_refuse, no_key_answer, redact_key, refusal_answer, refusal_of, threads_beta_answer,
    threads_beta_tokens, unusable_key_answer,
};
use crate::server::{RespBody, anthropic_error, cut_response, full_body};
use crate::sse::{JsonUsageTracker, SseUsageTracker, Usage};
use crate::trace::{Phase, Trace};
use crate::upstream::{
    FailureCode, RequestBody, StreamedBody, UpstreamAnswer, UpstreamClient, UpstreamRequest,
    forward_headers, is_loopback_host, response_headers, upstream_url,
};

/// Receives one metadata line per request: method, path, model, and how it ended.
pub type Logger = Arc<dyn Fn(String) + Send + Sync>;

/// A lane that cannot be built: the environment variable that named the bad value, and why.
#[derive(Debug)]
pub struct LaneError {
    pub name: String,
    pub message: String,
}

/// One provider's route: its endpoint, its resolved model ids, and its key.
pub struct Lane {
    provider: &'static Provider,
    target: Uri,
    resolved: ResolvedIds,
    keys: KeyCache,
    client: Arc<UpstreamClient>,
    log: Logger,
}

impl Lane {
    /// Builds a lane; an endpoint override that does not parse is LaneError naming the env var.
    pub fn build(
        provider: &'static Provider,
        env: &Env,
        sources: KeySources,
        client: Arc<UpstreamClient>,
        log: Logger,
    ) -> Result<Arc<Lane>, LaneError> {
        let prefix = provider.env_prefix;
        let resolved = ResolvedIds {
            provider: provider.id,
            main: env_override(env, &format!("{prefix}_MODEL_MAIN"), provider.main.id),
            flash: env_override(env, &format!("{prefix}_MODEL_FLASH"), provider.flash.id),
        };
        let target = target_url(provider, env)?;
        let keys = KeyCache::new(sources, STORE_SHARE);
        Ok(Arc::new(Lane {
            provider,
            target,
            resolved,
            keys,
            client,
            log,
        }))
    }

    /// The provider this lane serves.
    pub fn provider(&self) -> &'static Provider {
        self.provider
    }

    /// The model ids this lane answers to, env overrides applied.
    pub fn resolved_ids(&self) -> ResolvedIds {
        self.resolved.clone()
    }

    /// Forwards one request, read whole, for this lane.
    pub async fn forward(&self, parts: Parts, body: Bytes, model: &str) -> Response<RespBody> {
        let adapted = adapt(&body, self.provider);
        let length = adapted.len() as u64;
        self.send(parts, RequestBody::Whole(adapted), length, model)
            .await
    }

    /// Forwards one request as its body arrives; only for a provider with an empty `strip`.
    pub async fn forward_streamed(
        &self,
        parts: Parts,
        body: Arc<StreamedBody>,
        model: &str,
    ) -> Response<RespBody> {
        let length = body.len();
        self.send(parts, RequestBody::Streamed(body), length, model)
            .await
    }

    /// One upstream request: refusals first, then the key, then the provider. The body goes
    /// out as handed over; a streamed body's bytes are never read here.
    async fn send(
        &self,
        parts: Parts,
        body: RequestBody,
        length: u64,
        model: &str,
    ) -> Response<RespBody> {
        let events = RequestEvents::from_parts(&parts);
        let method = parts.method.as_str();
        let path = parts.uri.path();
        // A message-threads beta has no provider answer; it is refused before anything goes out.
        let tokens = threads_beta_tokens(
            parts
                .headers
                .get_all("anthropic-beta")
                .iter()
                .filter_map(|value| value.to_str().ok()),
        );
        if !tokens.is_empty() {
            self.note(method, path, model, "-> refused: message threads");
            let answer = threads_beta_answer(self.provider.id, &tokens);
            events.emit(answer.status, Some("message threads beta".to_owned()));
            return respond(answer);
        }
        let key = match self.keys.get(KEY_BUDGET).await {
            KeyAnswer::Found(key) => key,
            KeyAnswer::NoKey => {
                self.note(method, path, model, "-> provider: no key");
                let answer = no_key_answer(&self.names());
                events.emit(answer.status, Some("no key".to_owned()));
                return respond(answer);
            }
            KeyAnswer::TimedOut => {
                self.note(method, path, model, "-> provider: key lookup timed out");
                let answer = key_timeout_answer(self.provider.id, self.provider.display);
                events.emit(answer.status, Some("key lookup timed out".to_owned()));
                return respond(answer);
            }
            KeyAnswer::Unusable(why) => {
                self.note(method, path, model, "-> provider: no key");
                let answer = unusable_key_answer(&self.names(), &why);
                events.emit(answer.status, Some("no key".to_owned()));
                return respond(answer);
            }
        };
        let path_and_query = parts.uri.path_and_query().map_or("/", |pq| pq.as_str());
        let url = match upstream_url(&self.target, path_and_query) {
            Ok(url) => url,
            Err(_) => {
                events.emit(502, Some("bad request path".to_owned()));
                return anthropic_error(
                    StatusCode::BAD_GATEWAY,
                    "api_error",
                    &format!("{} router: bad request path", self.provider.id),
                );
            }
        };
        if !forwardable(&self.target) {
            events.emit(
                502,
                Some("provider upstream must be https unless it is loopback".to_owned()),
            );
            return anthropic_error(
                StatusCode::BAD_GATEWAY,
                "api_error",
                &format!(
                    "{} router: provider upstream must be https unless it is loopback ({})",
                    self.provider.id,
                    self.target.authority().map_or("", |a| a.as_str()),
                ),
            );
        }
        let mut headers = forward_headers(&parts.headers, &self.target, length);
        headers.remove("x-api-key");
        headers.remove(AUTHORIZATION);
        let Ok(bearer) = HeaderValue::from_str(&format!("Bearer {}", key.expose())) else {
            self.note(method, path, model, "-> provider: no key");
            let answer = unusable_key_answer(
                &self.names(),
                "the key holds characters a header cannot carry",
            );
            events.emit(answer.status, Some("no key".to_owned()));
            return respond(answer);
        };
        headers.insert(AUTHORIZATION, bearer);
        headers.insert(ACCEPT_ENCODING, HeaderValue::from_static("identity"));
        let streamed = match &body {
            RequestBody::Streamed(stream) => Some(Arc::clone(stream)),
            RequestBody::Whole(_) => None,
        };
        let request = UpstreamRequest {
            url,
            method: parts.method.clone(),
            headers,
            body,
        };
        // The tap's usage lands in `usage_out`; the per-request hook writes it into the
        // request's event when the answer ends.
        let usage_out: Arc<Mutex<Option<Usage>>> = Arc::new(Mutex::new(None));
        let hook: Option<OutcomeHook> = events.active().then({
            let events = events.clone();
            let usage_out = Arc::clone(&usage_out);
            move || {
                Arc::new(move |outcome: AnswerOutcome| {
                    if let AnswerOutcome::Finished { .. } = outcome {
                        let usage = lock(&usage_out).clone();
                        events.set_usage(usage);
                    }
                    let (status, error) = event_of(outcome);
                    events.emit(status, error);
                }) as OutcomeHook
            }
        });
        events.mark_sent();
        match self.client.send(request).await {
            Err(failure)
                if failure.client_closed
                    || streamed.as_deref().is_some_and(StreamedBody::client_failed) =>
            {
                events.emit(499, Some("client closed".to_owned()));
                cut_response()
            }
            Err(failure) => {
                self.read_failed(failure.code, failure.trace, method, path, model, &events)
            }
            Ok(answer) => {
                let status = answer.status.as_u16();
                let plain = !compressed(&answer.headers);
                if may_refuse(status) && plain {
                    match self
                        .judge_error(answer, &key, method, path, model, &events)
                        .await
                    {
                        Judged::Replaced(response) => response,
                        Judged::Passed {
                            status,
                            headers,
                            body,
                        } => plain_answer(status, &headers, body, false),
                        Judged::Oversize { prefix, answer } => {
                            self.note(
                                method,
                                path,
                                model,
                                &format!("-> provider {}", answer.status.as_u16()),
                            );
                            stream_answer(
                                *answer,
                                self.client.idle(),
                                OutcomeWatch::new(hook.clone()),
                                StreamShape {
                                    prefix,
                                    tap: events.live_tap(None),
                                },
                            )
                        }
                    }
                } else {
                    self.note(method, path, model, &format!("-> provider {status}"));
                    let tap = if plain
                        && (answer.status.is_success() || answer.status.is_redirection())
                    {
                        Some(
                            Box::new(LaneTap::new(&answer.headers, Arc::clone(&usage_out)))
                                as Box<dyn AnswerTap>,
                        )
                    } else {
                        None
                    };
                    stream_answer(
                        answer,
                        self.client.idle(),
                        OutcomeWatch::new(hook.clone()),
                        StreamShape {
                            prefix: Bytes::new(),
                            tap: events.live_tap(tap),
                        },
                    )
                }
            }
        }
    }

    fn note(&self, method: &str, path: &str, model: &str, tail: &str) {
        (self.log)(format!("{method} {path} model={model} {tail}"));
    }

    /// Reads a provider error answer before the client sees it: a refused key or an empty
    /// balance becomes the lane's own non-retryable 400 and forgets a refused key, an error
    /// quoting the key goes out with the key blanked, any other error passes with its reason
    /// on the log line. An answer that outgrows the inspect limit is released with its own
    /// head, the bytes read so far sent first.
    async fn judge_error(
        &self,
        answer: UpstreamAnswer,
        key: &SecretString,
        method: &str,
        path: &str,
        model: &str,
        events: &RequestEvents,
    ) -> Judged {
        let UpstreamAnswer {
            status,
            headers,
            mut body,
            conn,
            upgrade,
            trace,
        } = answer;
        let mut held: Vec<u8> = Vec::new();
        loop {
            match tokio::time::timeout(self.client.idle(), body.frame()).await {
                Ok(Some(Ok(frame))) => {
                    if let Some(data) = frame.data_ref() {
                        held.extend_from_slice(data);
                        if held.len() > INSPECT_LIMIT {
                            return Judged::Oversize {
                                prefix: Bytes::from(held),
                                answer: Box::new(UpstreamAnswer {
                                    status,
                                    headers,
                                    body,
                                    conn,
                                    upgrade,
                                    trace,
                                }),
                            };
                        }
                    }
                }
                Ok(None) => break,
                Ok(Some(Err(_))) => {
                    return Judged::Replaced(self.read_failed(
                        FailureCode::EAborted,
                        trace.at(Phase::Body),
                        method,
                        path,
                        model,
                        events,
                    ));
                }
                Err(_) => {
                    return Judged::Replaced(self.read_failed(
                        FailureCode::ETimedOut,
                        trace.at(Phase::Body),
                        method,
                        path,
                        model,
                        events,
                    ));
                }
            }
        }
        conn.finish();
        let held = Bytes::from(held);
        let reason = error_reason(&held, key.expose());
        if let Some(refusal) = refusal_of(status.as_u16(), &held) {
            let tail = reason
                .as_deref()
                .map_or(String::new(), |r| format!(" ({r})"));
            self.note(
                method,
                path,
                model,
                &format!("-> provider {}{tail}", status.as_u16()),
            );
            if refusal == Refusal::Key {
                // The key may have been rotated: the kept answer is dropped so the next
                // request reads it again.
                self.keys.clear();
            }
            let why = format!(
                "{} answered HTTP {}",
                self.provider.display,
                status.as_u16()
            );
            self.note(
                method,
                path,
                model,
                &format!("-> provider answered 400 ({})", refusal_word(refusal)),
            );
            let answer = refusal_answer(&self.names(), refusal, &why);
            events.emit(answer.status, Some(refusal_word(refusal).to_owned()));
            return Judged::Replaced(respond(answer));
        }
        if let Some(redacted) = redact_key(&held, key.expose()) {
            self.note(
                method,
                path,
                model,
                &format!("-> provider answered {} (key redacted)", status.as_u16()),
            );
            events.emit(status.as_u16(), Some("key redacted".to_owned()));
            return Judged::Replaced(plain_answer(
                status.as_u16(),
                &headers,
                Bytes::from(redacted),
                true,
            ));
        }
        let tail = reason
            .as_deref()
            .map_or(String::new(), |r| format!(" ({r})"));
        self.note(
            method,
            path,
            model,
            &format!("-> provider {}{tail}", status.as_u16()),
        );
        events.emit(status.as_u16(), reason);
        Judged::Passed {
            status: status.as_u16(),
            headers,
            body: held,
        }
    }

    /// The answer a request that never got a whole error body gets: the same answer and log
    /// line a failed upstream call brings.
    fn read_failed(
        &self,
        code: FailureCode,
        trace: Trace,
        method: &str,
        path: &str,
        model: &str,
        events: &RequestEvents,
    ) -> Response<RespBody> {
        self.note(
            method,
            path,
            model,
            &format!("-> provider error {} {}", code.as_str(), trace.line()),
        );
        let status = if code == FailureCode::ETimedOut {
            504
        } else {
            502
        };
        events.emit(status, Some(code.as_str().to_owned()));
        let status = match code {
            FailureCode::ETimedOut => StatusCode::GATEWAY_TIMEOUT,
            _ => StatusCode::BAD_GATEWAY,
        };
        anthropic_error(
            status,
            "api_error",
            &format!(
                "{} router: provider unreachable ({})",
                self.provider.id,
                code.as_str()
            ),
        )
    }

    fn names(&self) -> RefusalNames {
        RefusalNames {
            display: self.provider.display.to_owned(),
            slash: self.provider.slash.to_owned(),
            billing_url: self.provider.billing_url.to_owned(),
        }
    }
}

/// Whether the provider may be handed this request: an https target, or http on this machine.
fn forwardable(target: &Uri) -> bool {
    target.scheme_str() == Some("https") || target.host().is_some_and(is_loopback_host)
}

/// A refusal as the client's answer: its status, its headers, its whole body.
fn respond(replacement: Replacement) -> Response<RespBody> {
    let mut response = Response::new(full_body(replacement.body));
    *response.status_mut() =
        StatusCode::from_u16(replacement.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    let headers = response.headers_mut();
    for (name, value) in replacement.headers {
        if let (Ok(name), Ok(value)) = (HeaderName::try_from(name), HeaderValue::try_from(value)) {
            headers.insert(name, value);
        }
    }
    response
}

/// What the judge decided about a provider error answer.
enum Judged {
    /// A different answer goes to the client: a refusal, a redacted body, or a read that
    /// failed before the judge could decide.
    Replaced(Response<RespBody>),
    /// The upstream's own answer, bytes and head as they arrived.
    Passed {
        status: u16,
        headers: HeaderMap,
        body: Bytes,
    },
    /// The answer outgrew the inspect limit: the bytes read so far go first, the rest
    /// streams as it arrives.
    Oversize {
        prefix: Bytes,
        answer: Box<UpstreamAnswer>,
    },
}

/// Reads usage off a passing lane answer; the frames are never touched or delayed.
pub(crate) struct LaneTap {
    tracker: Option<LaneTracker>,
    usage_out: Arc<Mutex<Option<Usage>>>,
}

/// The usage reader one answer's content type picks: an event stream carries its usage in
/// its events, a JSON answer in its top-level `usage`.
enum LaneTracker {
    Event(SseUsageTracker),
    Body(JsonUsageTracker),
}

impl LaneTracker {
    fn feed(&mut self, chunk: &[u8]) {
        match self {
            LaneTracker::Event(tracker) => tracker.feed(chunk),
            LaneTracker::Body(tracker) => tracker.feed(chunk),
        }
    }

    /// The usage the whole answer carried, ending the tracker.
    fn finish(self) -> Option<Usage> {
        match self {
            LaneTracker::Event(tracker) => tracker.usage().cloned(),
            LaneTracker::Body(tracker) => tracker.finish(),
        }
    }
}

impl LaneTap {
    /// A tap for one answer, writing its final usage to `usage_out`.
    pub(crate) fn new(headers: &HeaderMap, usage_out: Arc<Mutex<Option<Usage>>>) -> LaneTap {
        let tracker = if is_event_stream(headers) {
            LaneTracker::Event(SseUsageTracker::new())
        } else {
            LaneTracker::Body(JsonUsageTracker::new())
        };
        LaneTap {
            tracker: Some(tracker),
            usage_out,
        }
    }
}

impl AnswerTap for LaneTap {
    fn chunk(&mut self, chunk: &Bytes) {
        if let Some(tracker) = &mut self.tracker {
            tracker.feed(chunk);
        }
    }

    fn end(&mut self) {
        let usage = self.tracker.take().and_then(LaneTracker::finish);
        *lock(&self.usage_out) = usage;
    }
}

/// A lock's guard, kept through a poisoning: plain data no writer leaves broken.
fn lock<T>(value: &Mutex<T>) -> MutexGuard<'_, T> {
    value.lock().unwrap_or_else(PoisonError::into_inner)
}

/// Whether an answer is compressed: a lane asks its provider for `identity`, so a compressed
/// lane answer passes unjudged and unread, and so does a compressed Anthropic answer.
pub(crate) fn compressed(headers: &HeaderMap) -> bool {
    match headers
        .get(CONTENT_ENCODING)
        .and_then(|value| value.to_str().ok())
    {
        Some(encoding) => encoding.trim() != "identity",
        None => false,
    }
}

/// The upstream's own answer to the client: hop-level headers dropped, and the framing
/// headers with them when the body was read and served again.
fn plain_answer(status: u16, headers: &HeaderMap, body: Bytes, reread: bool) -> Response<RespBody> {
    let mut response = Response::new(full_body(body));
    *response.status_mut() =
        StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    let mut filtered = response_headers(headers);
    if reread {
        filtered.remove(CONTENT_LENGTH);
        filtered.remove(CONTENT_ENCODING);
    }
    *response.headers_mut() = filtered;
    response
}

/// The refusal's word on the log line.
fn refusal_word(refusal: Refusal) -> &'static str {
    match refusal {
        Refusal::Key => "key refused",
        Refusal::Balance => "out of balance",
        Refusal::Forbidden => "forbidden",
    }
}

/// The env value set and not empty (after trimming), else the fallback.
fn env_override(env: &Env, name: &str, fallback: &'static str) -> String {
    env_value(env, name).map_or_else(|| fallback.to_owned(), str::to_owned)
}

/// The env value set and not empty (after trimming), or none.
fn env_value<'a>(env: &'a Env, name: &str) -> Option<&'a str> {
    env.get(name)
        .map(str::trim)
        .filter(|value| !value.is_empty())
}

/// The lane's endpoint: `<P>_ROUTER_URL`, else `<P>_BASE_URL`, else the catalog's. A value
/// that does not parse, or names no scheme and host, is LaneError naming its env var.
fn target_url(provider: &Provider, env: &Env) -> Result<Uri, LaneError> {
    let prefix = provider.env_prefix;
    let router_knob = format!("{prefix}_ROUTER_URL");
    let base_knob = format!("{prefix}_BASE_URL");
    let (value, name) = match env_value(env, &router_knob) {
        Some(value) => (value.to_owned(), router_knob),
        None => match env_value(env, &base_knob) {
            Some(value) => (value.to_owned(), base_knob),
            None => (provider.base_url.to_owned(), router_knob),
        },
    };
    let url: Uri = value
        .parse()
        .map_err(|err: hyper::http::uri::InvalidUri| LaneError {
            name: name.clone(),
            message: err.to_string(),
        })?;
    if url.scheme_str().is_none() || url.host().is_none() {
        return Err(LaneError {
            name,
            message: "the URL needs a scheme and a host".to_owned(),
        });
    }
    Ok(url)
}
