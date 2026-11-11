//! huddle-router: anthropic.
//!
//! The model requests of a decrypted CONNECT: a request whose announced body names a claude
//! model, or a model whose lane forwards bodies without editing them, is relayed upstream as
//! the body arrives; every other body is read whole first and handed to the whole route; a
//! request that is not a model request goes to the pass-through.

use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use http_body_util::BodyExt;
use hyper::body::{Body, Bytes, Frame, Incoming};
use hyper::header::{CONNECTION, CONTENT_LENGTH, CONTENT_TYPE, HeaderMap, HeaderValue, UPGRADE};
use hyper::http::request::Parts;
use hyper::{Method, Request, Response, StatusCode, Uri};
use hyper_util::rt::TokioIo;
use tokio::sync::mpsc;

use crate::body;
use crate::dispatch::RequestEvents;
use crate::server::{
    BodyCut, Handler, HandlerFuture, RespBody, anthropic_error, cut_response, empty_body, receive,
    safe_path,
};
use crate::upstream::{
    Failure, FailureCode, RequestBody, StreamedBody, UpstreamAnswer, UpstreamClient,
    UpstreamRequest, forward_headers, response_headers, upstream_url,
};

/// How much of an announced body is read before its model picks the route: the model sits at
/// the start of a Claude Code request.
pub const STREAM_HEAD_BYTES: usize = 64 * 1024;

/// The paths of model requests; the query never takes part.
const MODEL_PATHS: [&str; 2] = ["/v1/messages", "/v1/messages/count_tokens"];

/// A request that was read whole and must still be routed by model.
pub type WholeRoute = Arc<dyn Fn(Parts, Bytes) -> HandlerFuture + Send + Sync>;

/// A model request a provider lane takes as its body arrives.
pub type StreamedRoute =
    Arc<dyn Fn(Parts, Arc<StreamedBody>, String) -> HandlerFuture + Send + Sync>;

/// How one answer ended, for whatever the dispatch and the events record.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AnswerOutcome {
    /// The upstream's answer ended and the client got all of it.
    Finished { status: u16, bytes: u64 },
    /// The upstream failed before the head; the client got the 502 or the 504.
    Failed { code: FailureCode },
    /// The upstream's body broke after the head.
    Cut { code: FailureCode, bytes: u64 },
    /// The client left, before or after the head.
    ClientClosed { bytes: u64 },
}

/// Told how every answer ends, exactly once per answer.
pub type OutcomeHook = Arc<dyn Fn(AnswerOutcome) + Send + Sync>;

/// The route event's status and error line for one answer's outcome.
pub fn event_of(outcome: AnswerOutcome) -> (u16, Option<String>) {
    match outcome {
        AnswerOutcome::Finished { status, .. } => (status, None),
        AnswerOutcome::Failed { code } => (
            if code == FailureCode::ETimedOut {
                504
            } else {
                502
            },
            Some(code.as_str().to_owned()),
        ),
        AnswerOutcome::Cut { code, .. } => (502, Some(format!("stream cut ({})", code.as_str()))),
        AnswerOutcome::ClientClosed { .. } => (499, Some("client closed".to_owned())),
    }
}

/// True for the requests the router routes by model; every other request passes through.
pub(crate) fn is_model_request<B>(req: &Request<B>) -> bool {
    req.method() == Method::POST && MODEL_PATHS.contains(&req.uri().path())
}

/// The model requests of a decrypted conversation and their routes.
pub struct AnthropicPath {
    pub client: Arc<UpstreamClient>,
    /// The Anthropic origin: the scheme, host and path every model request rides to.
    pub target: Uri,
    /// The largest body a request may announce or carry.
    pub body_limit: u64,
    /// True when no provider lane claims this model.
    pub is_anthropic: Arc<dyn Fn(&str) -> bool + Send + Sync>,
    /// True when this model's lane forwards the body without editing it.
    pub lane_streams: Arc<dyn Fn(&str) -> bool + Send + Sync>,
    /// Serves a lane-bound body as it arrives.
    pub streamed_route: StreamedRoute,
    /// Serves a request read whole.
    pub whole_route: WholeRoute,
    /// Serves every non-model request inside the CONNECT.
    pub pass_through: Handler,
    /// Told how every answer ends, exactly once per answer.
    pub on_outcome: Option<OutcomeHook>,
}

impl AnthropicPath {
    /// The decrypted conversation's handler: the model requests take the announced-body flow
    /// below, every other request the pass-through.
    pub fn intercept_handler(self: Arc<Self>) -> Handler {
        Arc::new(move |req: Request<Incoming>| {
            let path = Arc::clone(&self);
            Box::pin(async move { path.serve(req).await })
        })
    }

    async fn serve(self: Arc<Self>, req: Request<Incoming>) -> Response<RespBody> {
        if !is_model_request(&req) {
            return (self.pass_through)(req).await;
        }
        self.model_request(req).await
    }

    /// The model request: an announced body within the limit streams once its head names a
    /// model a relay serves; anything else is read whole first, an over-limit or failing body
    /// answered before anything is sent.
    async fn model_request(self: Arc<Self>, req: Request<Incoming>) -> Response<RespBody> {
        let Some(len) = announced(&req, self.body_limit) else {
            return match receive(req, self.body_limit).await {
                Ok((parts, body)) => (self.whole_route)(parts, body).await,
                Err(answer) => answer,
            };
        };
        if !safe_path(req.uri().path_and_query().map_or("", |pq| pq.as_str())) {
            return bad_path();
        }
        let (parts, mut body) = req.into_parts();
        let stream = StreamedBody::new(len);
        let head = match read_head(&mut body, &stream).await {
            Ok(head) => head,
            Err(answer) => return answer,
        };
        let (model, settled) = body::model_head(&head);
        let relays = settled
            && (head.len() as u64) < len
            && ((self.is_anthropic)(&model) || (self.lane_streams)(&model));
        if !relays {
            return match fill(body, Arc::clone(&stream)).await {
                Ok(()) => match stream.whole_bytes() {
                    Some(whole) => (self.whole_route)(parts, whole).await,
                    None => cut_response(),
                },
                Err(answer) => answer,
            };
        }
        // The rest of the body fills the stream while the answer is already being relayed.
        tokio::spawn(fill(body, Arc::clone(&stream)));
        if (self.is_anthropic)(&model) {
            self.forward_streamed(parts, stream).await
        } else {
            (self.streamed_route)(parts, stream, model).await
        }
    }

    /// A model request whose body is in hand, Anthropic-bound: the client's headers and bytes
    /// pass unchanged.
    pub async fn forward_whole(&self, parts: Parts, body: Bytes) -> Response<RespBody> {
        let events = RequestEvents::from_parts(&parts);
        let request = match self.request(&parts, RequestBody::Whole(body)) {
            Ok(request) => request,
            Err(answer) => return answer,
        };
        events.mark_sent();
        let mut done = OutcomeWatch::new(Some(self.answer_hook(events)));
        match self.client.send(request).await {
            Ok(answer) => stream_answer(answer, self.client.idle(), done, StreamShape::default()),
            Err(failure) if failure.client_closed => {
                done.finish(AnswerOutcome::ClientClosed { bytes: 0 });
                cut_response()
            }
            Err(failure) => {
                done.finish(AnswerOutcome::Failed { code: failure.code });
                failed(failure)
            }
        }
    }

    /// A model request relayed as its body arrives: the upstream reads the stream while the
    /// client fills it. A client that stopped short ends the call with no answer.
    async fn forward_streamed(
        &self,
        parts: Parts,
        stream: Arc<StreamedBody>,
    ) -> Response<RespBody> {
        let events = RequestEvents::from_parts(&parts);
        let request = match self.request(&parts, RequestBody::Streamed(Arc::clone(&stream))) {
            Ok(request) => request,
            Err(answer) => return answer,
        };
        events.mark_sent();
        let mut done = OutcomeWatch::new(Some(self.answer_hook(events)));
        match self.client.send(request).await {
            Ok(answer) => stream_answer(answer, self.client.idle(), done, StreamShape::default()),
            Err(failure) if failure.client_closed || stream.client_failed() => {
                done.finish(AnswerOutcome::ClientClosed { bytes: 0 });
                cut_response()
            }
            Err(failure) => {
                done.finish(AnswerOutcome::Failed { code: failure.code });
                failed(failure)
            }
        }
    }

    /// The per-request outcome hook: the path's shared hook first, then the request's route
    /// event.
    fn answer_hook(&self, events: RequestEvents) -> OutcomeHook {
        let shared = self.on_outcome.clone();
        Arc::new(move |outcome| {
            if let Some(shared) = shared.as_ref() {
                shared(outcome);
            }
            if events.active() {
                let (status, error) = event_of(outcome);
                events.emit(status, error);
            }
        })
    }

    /// The upstream call for a body bound for Anthropic: the destination and the announced
    /// length, the client's headers with the hop-by-hop ones dropped. Nothing else is added.
    // The err side is the whole answer; the 128-byte move only happens on a failure path.
    #[allow(clippy::result_large_err)]
    fn request(
        &self,
        parts: &Parts,
        body: RequestBody,
    ) -> Result<UpstreamRequest, Response<RespBody>> {
        let path_and_query = parts.uri.path_and_query().map_or("/", |pq| pq.as_str());
        let url = upstream_url(&self.target, path_and_query).map_err(|code| {
            failed(Failure {
                code,
                client_closed: false,
            })
        })?;
        let len = match &body {
            RequestBody::Whole(bytes) => bytes.len() as u64,
            RequestBody::Streamed(stream) => stream.len(),
        };
        Ok(UpstreamRequest {
            url,
            method: parts.method.clone(),
            headers: forward_headers(&parts.headers, &self.target, len),
            body,
        })
    }
}

/// The announced body length: a Content-Length in `1..=limit`. A chunked body, an absent
/// length, a zero one and one over the limit announce nothing.
fn announced(req: &Request<Incoming>, limit: u64) -> Option<u64> {
    req.headers()
        .get(CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<u64>().ok())
        .filter(|&len| (1..=limit).contains(&len))
}

/// A request path the router may not forward.
fn bad_path() -> Response<RespBody> {
    anthropic_error(
        StatusCode::BAD_REQUEST,
        "invalid_request_error",
        "huddle-router router: bad request path",
    )
}

/// The client stopped short: the stream never fills, and the conversation ends with no
/// answer.
fn cut_client(stream: &StreamedBody) -> Response<RespBody> {
    stream.fail();
    cut_response()
}

/// Reads the announced body's first frames: every frame pushed into the stream unchanged,
/// the first `STREAM_HEAD_BYTES` bytes also copied into the head. Ends when the head is in
/// or the body is whole; a client that stops before either cuts the connection.
// The err side is the whole answer; the 128-byte move only happens on a failure path.
#[allow(clippy::result_large_err)]
async fn read_head(
    body: &mut Incoming,
    stream: &StreamedBody,
) -> Result<Vec<u8>, Response<RespBody>> {
    let target = stream.len().min(STREAM_HEAD_BYTES as u64);
    let mut head = Vec::with_capacity(STREAM_HEAD_BYTES);
    while stream.filled() < target {
        let Some(Ok(frame)) = body.frame().await else {
            return Err(cut_client(stream));
        };
        let Some(data) = frame.data_ref() else {
            continue;
        };
        stream.push(data.clone());
        let room = STREAM_HEAD_BYTES - head.len();
        if room > 0 {
            head.extend_from_slice(&data[..data.len().min(room)]);
        }
    }
    Ok(head)
}

/// Reads the rest of the body into the stream: every frame pushed unchanged, in order. A
/// client that stops short, or a frame that fails, marks the stream failed; the relay ends
/// with no answer.
// The err side is the whole answer; the 128-byte move only happens on a failure path.
#[allow(clippy::result_large_err)]
async fn fill(body: Incoming, stream: Arc<StreamedBody>) -> Result<(), Response<RespBody>> {
    let mut body = body;
    while !stream.is_whole() {
        let Some(Ok(frame)) = body.frame().await else {
            return Err(cut_client(&stream));
        };
        let Some(data) = frame.data_ref() else {
            continue;
        };
        stream.push(data.clone());
    }
    Ok(())
}

/// Reads a passing answer without touching or delaying it: a usage tap.
pub trait AnswerTap: Send {
    /// Reads a frame on its way to the client; the frame itself is not touched or delayed.
    fn chunk(&mut self, chunk: &Bytes);
    /// Called once when the upstream ended (not on a cut).
    fn end(&mut self);
}

/// How one answer is shaped on its way out: bytes a judge read first, then a tap that only
/// reads.
#[derive(Default)]
pub struct StreamShape {
    /// Bytes already read from the upstream, sent first (they are not re-tapped).
    pub prefix: Bytes,
    pub tap: Option<Box<dyn AnswerTap>>,
}

/// The upstream answer for the client: the head passes as it arrived, the body streams frame
/// by frame as it arrives. The pump owns the answer body and its connection.
pub(crate) fn stream_answer(
    answer: UpstreamAnswer,
    idle: Duration,
    done: OutcomeWatch,
    shape: StreamShape,
) -> Response<RespBody> {
    let (tx, rx) = mpsc::channel(8);
    let mut response = Response::new(StreamingBody { rx }.boxed());
    *response.status_mut() = answer.status;
    *response.headers_mut() = response_headers(&answer.headers);
    tokio::spawn(pump(answer, idle, tx, done, shape));
    response
}

/// Reads the upstream body under the same silence budget the request had, and forwards every
/// frame the moment it arrives. Ends: the body ends, the client leaves (which ends the
/// upstream call too), or the upstream breaks, when an event stream ends with the terminal
/// error event and any other answer is cut.
async fn pump(
    answer: UpstreamAnswer,
    idle: Duration,
    tx: mpsc::Sender<Result<Frame<Bytes>, BodyCut>>,
    mut done: OutcomeWatch,
    mut shape: StreamShape,
) {
    let status = answer.status.as_u16();
    let sse = is_event_stream(&answer.headers);
    let mut body = answer.body;
    let conn = answer.conn;
    let mut tail = BoundaryTail::new();
    let mut bytes = 0u64;
    // Bytes the judge held back before releasing the answer reach the client first, as they
    // arrived; the tap sees only what the pump reads itself.
    let prefix = shape.prefix;
    if !prefix.is_empty() {
        bytes += prefix.len() as u64;
        if tx.send(Ok(Frame::data(prefix))).await.is_err() {
            done.finish(AnswerOutcome::ClientClosed { bytes });
            return;
        }
    }
    loop {
        match tokio::time::timeout(idle, body.frame()).await {
            Ok(Some(Ok(frame))) => {
                if let Some(data) = frame.data_ref() {
                    bytes += data.len() as u64;
                    tail.push(data);
                    if let Some(tap) = shape.tap.as_mut() {
                        tap.chunk(data);
                    }
                }
                if tx.send(Ok(frame)).await.is_err() {
                    // The client left: dropping the body and the connection ends the upstream
                    // call too.
                    done.finish(AnswerOutcome::ClientClosed { bytes });
                    return;
                }
            }
            Ok(None) => {
                conn.finish();
                if let Some(tap) = shape.tap.as_mut() {
                    tap.end();
                }
                done.finish(AnswerOutcome::Finished { status, bytes });
                return;
            }
            Ok(Some(Err(_))) => {
                end_failed(FailureCode::EAborted, sse, &tail, bytes, &tx, &mut done).await;
                return;
            }
            Err(_) => {
                end_failed(FailureCode::ETimedOut, sse, &tail, bytes, &tx, &mut done).await;
                return;
            }
        }
    }
}

/// Ends a broken answer after its head: an event stream gets one terminal error event, on an
/// event boundary, and then ends cleanly; any other answer reports a body error, which cuts
/// the connection.
async fn end_failed(
    code: FailureCode,
    sse: bool,
    tail: &BoundaryTail,
    bytes: u64,
    tx: &mpsc::Sender<Result<Frame<Bytes>, BodyCut>>,
    done: &mut OutcomeWatch,
) {
    if sse {
        let prefix = if tail.at_boundary() { "" } else { "\n\n" };
        let _ = tx
            .send(Ok(Frame::data(Bytes::from(format!(
                "{prefix}{}",
                cut_event(code)
            )))))
            .await;
    } else {
        let _ = tx.send(Err(BodyCut)).await;
    }
    done.finish(AnswerOutcome::Cut { code, bytes });
}

/// The terminal event a cut event stream ends with: one typed error the client reads instead
/// of a cut socket. The shape is fixed and the code is one of a known set, so the JSON needs
/// no escaping.
fn cut_event(code: FailureCode) -> String {
    format!(
        "event: error\ndata: {{\"type\":\"error\",\"error\":{{\"type\":\"api_error\",\"message\":\"upstream connection failed ({})\"}}}}\n\n",
        code.as_str()
    )
}

/// Whether the answer's content type names an event stream.
pub(crate) fn is_event_stream(headers: &HeaderMap) -> bool {
    headers
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.contains("text/event-stream"))
}

/// Whether the bytes seen so far end on an event boundary: nothing seen yet, or the stream
/// ends with a blank line, tracked across chunks and both line endings.
struct BoundaryTail {
    tail: [u8; 4],
    seen: u64,
}

impl BoundaryTail {
    fn new() -> Self {
        BoundaryTail {
            tail: [0; 4],
            seen: 0,
        }
    }

    fn push(&mut self, data: &[u8]) {
        self.seen += data.len() as u64;
        let fresh = &data[data.len().saturating_sub(4)..];
        self.tail.rotate_left(fresh.len());
        self.tail[4 - fresh.len()..].copy_from_slice(fresh);
    }

    fn at_boundary(&self) -> bool {
        self.seen == 0 || &self.tail[2..] == b"\n\n" || self.tail == *b"\r\n\r\n"
    }
}

/// Fires the answer's outcome exactly once: at the end the pump saw, or, when the answer's
/// future is dropped first, as a client that left before anything was sent.
pub(crate) struct OutcomeWatch {
    hook: Option<Box<dyn FnOnce(AnswerOutcome) + Send>>,
}

impl OutcomeWatch {
    pub(crate) fn new(hook: Option<OutcomeHook>) -> Self {
        OutcomeWatch {
            hook: hook.map(|hook| {
                Box::new(move |outcome| hook(outcome)) as Box<dyn FnOnce(AnswerOutcome) + Send>
            }),
        }
    }

    fn finish(&mut self, outcome: AnswerOutcome) {
        if let Some(hook) = self.hook.take() {
            hook(outcome);
        }
    }
}

impl Drop for OutcomeWatch {
    fn drop(&mut self) {
        self.finish(AnswerOutcome::ClientClosed { bytes: 0 });
    }
}

/// The answer's body: the frames the pump forwards, in order; a body error cuts the
/// connection.
struct StreamingBody {
    rx: mpsc::Receiver<Result<Frame<Bytes>, BodyCut>>,
}

impl Body for StreamingBody {
    type Data = Bytes;
    type Error = BodyCut;

    fn poll_frame(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Self::Data>, Self::Error>>> {
        self.get_mut().rx.poll_recv(cx)
    }
}

/// An upstream failure that is not the client's leaving: one Anthropic-shaped error, 504
/// when the upstream stayed silent, 502 for the rest.
fn failed(failure: Failure) -> Response<RespBody> {
    let status = match failure.code {
        FailureCode::ETimedOut => StatusCode::GATEWAY_TIMEOUT,
        _ => StatusCode::BAD_GATEWAY,
    };
    anthropic_error(
        status,
        "api_error",
        &format!(
            "huddle-router router: anthropic unreachable ({})",
            failure.code.as_str()
        ),
    )
}

/// One passed request's metadata, told to the event record at the same moment as the log line.
pub struct PassEvent {
    /// The request's method.
    pub method: String,
    /// The request's path, never its query.
    pub path: String,
    /// The upstream authority the request rode to.
    pub host: String,
    /// The status the client saw; `0` before a head arrived.
    pub status: u16,
    /// The answer body's bytes.
    pub bytes: u64,
    /// The request's whole duration.
    pub ms: u64,
    /// How the pass-through ended.
    pub word: PassWord,
}

/// How one passed request ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PassWord {
    /// The answer ended and the client got all of it.
    Finished,
    /// The upstream's body broke after its head.
    Cut(FailureCode),
    /// The request never got a head from the upstream.
    Error(FailureCode),
    /// The client left before the answer ended.
    Cancelled,
}

/// Told each passed request's metadata, exactly once per request.
pub type PassHook = Arc<dyn Fn(&PassEvent) + Send + Sync>;

/// What the pass-through needs: the upstream client, the Anthropic origin, the body limit,
/// where the log line goes, and the event record.
pub struct PassCtx {
    pub client: Arc<UpstreamClient>,
    /// The Anthropic origin every passed request rides to.
    pub target: Uri,
    pub body_limit: u64,
    /// Receives one metadata-only log line per request.
    pub note: Arc<dyn Fn(&str) + Send + Sync>,
    /// Receives the same request's metadata at the same moment.
    pub on_pass: Option<PassHook>,
}

/// Serves every request that is not a model request: it reaches Anthropic with only its host
/// changed, its answer streams back as it arrives, an upgrade is relayed both ways, and one
/// metadata-only line is logged per request.
pub fn pass_through_handler(ctx: Arc<PassCtx>) -> Handler {
    Arc::new(move |req: Request<Incoming>| {
        let ctx = ctx.clone();
        Box::pin(async move { pass(ctx, req).await }) as HandlerFuture
    })
}

async fn pass(ctx: Arc<PassCtx>, req: Request<Incoming>) -> Response<RespBody> {
    let started = Instant::now();
    let host = ctx
        .target
        .authority()
        .map_or_else(String::new, |a| a.as_str().to_owned());
    let upgraded = req.headers().contains_key(UPGRADE);
    // An upgrade request has no body to read; its tunnel is taken before the answer, or the
    // upgrade has nowhere to land.
    let mut client_tunnel = None;
    let (parts, body) = if upgraded {
        let mut req = req;
        client_tunnel = Some(hyper::upgrade::on(&mut req));
        let (parts, _) = req.into_parts();
        (parts, Bytes::new())
    } else {
        match receive(req, ctx.body_limit).await {
            Ok(whole) => whole,
            Err(resp) => return resp,
        }
    };
    let method = parts.method.as_str().to_owned();
    let path = parts.uri.path().to_owned();
    let path_and_query = parts
        .uri
        .path_and_query()
        .map_or("/", |pq| pq.as_str())
        .to_owned();
    let url = match upstream_url(&ctx.target, &path_and_query) {
        Ok(url) => url,
        Err(code) => return pass_error(&ctx, &started, &method, &path, &host, code),
    };
    let mut headers = forward_headers(&parts.headers, &ctx.target, body.len() as u64);
    if upgraded {
        // forward_headers drops the connection headers; the handshake must survive the hop.
        headers.insert(CONNECTION, HeaderValue::from_static("upgrade"));
        if let Some(value) = parts.headers.get(UPGRADE) {
            headers.insert(UPGRADE, value.clone());
        }
    }
    let request = UpstreamRequest {
        url,
        method: parts.method.clone(),
        headers,
        body: RequestBody::Whole(body),
    };
    match ctx.client.send(request).await {
        Err(failure) => pass_error(&ctx, &started, &method, &path, &host, failure.code),
        Ok(mut answer) if answer.status == StatusCode::SWITCHING_PROTOCOLS => {
            match (client_tunnel, answer.upgrade.take()) {
                (Some(client), Some(upstream)) => {
                    // The sender must outlive the upgrade: dropping it ends the connection
                    // before the tunnel is in. It drops, unpoolled, when the relay ends.
                    let conn = answer.conn;
                    tokio::spawn(async move {
                        if let (Ok(client), Ok(upstream)) = tokio::join!(client, upstream) {
                            let mut client = TokioIo::new(client);
                            let mut upstream = TokioIo::new(upstream);
                            let _ = tokio::io::copy_bidirectional(&mut client, &mut upstream).await;
                        }
                        drop(conn);
                    });
                }
                // The upstream answered an upgrade the client never asked for, or its tunnel
                // never arrived: the answer carries nothing.
                _ => {
                    return pass_error(
                        &ctx,
                        &started,
                        &method,
                        &path,
                        &host,
                        FailureCode::EUpstream,
                    );
                }
            }
            let mut response = Response::new(empty_body());
            *response.status_mut() = answer.status;
            *response.headers_mut() = response_headers(&answer.headers);
            response
        }
        Ok(answer) => {
            let status = answer.status.as_u16();
            let note = Arc::clone(&ctx.note);
            let on_pass = ctx.on_pass.clone();
            let report = move |outcome: AnswerOutcome| {
                let ms = started.elapsed().as_millis() as u64;
                let (word, status, bytes, line) = match outcome {
                    AnswerOutcome::Finished { status, bytes } => (
                        PassWord::Finished,
                        status,
                        bytes,
                        format!(
                            "{method} {path} host={host} \u{2192} anthropic-pass {status} bytes={bytes} ms={ms}"
                        ),
                    ),
                    AnswerOutcome::Cut { code, bytes } => (
                        PassWord::Cut(code),
                        status,
                        bytes,
                        format!(
                            "{method} {path} host={host} \u{2192} anthropic-pass cut ({}) status={status} bytes={bytes} ms={ms}",
                            code.as_str()
                        ),
                    ),
                    AnswerOutcome::ClientClosed { bytes } => (
                        PassWord::Cancelled,
                        status,
                        bytes,
                        format!(
                            "{method} {path} host={host} \u{2192} anthropic-pass cancelled bytes={bytes} ms={ms}"
                        ),
                    ),
                    AnswerOutcome::Failed { code } => (
                        PassWord::Error(code),
                        0,
                        0,
                        format!(
                            "{method} {path} host={host} \u{2192} anthropic-pass error {} ms={ms}",
                            code.as_str()
                        ),
                    ),
                };
                let event = PassEvent {
                    method: method.clone(),
                    path: path.clone(),
                    host: host.clone(),
                    status,
                    bytes,
                    ms,
                    word,
                };
                (note)(&line);
                if let Some(on_pass) = &on_pass {
                    (on_pass)(&event);
                }
            };
            let done = OutcomeWatch::new(Some(Arc::new(report)));
            stream_answer(answer, ctx.client.idle(), done, StreamShape::default())
        }
    }
}

/// A passed request that never got a head: one log line with the code, then a plain 502 whose
/// body names nothing else, since any Anthropic client may be on the other end.
fn pass_error(
    ctx: &PassCtx,
    started: &Instant,
    method: &str,
    path: &str,
    host: &str,
    code: FailureCode,
) -> Response<RespBody> {
    let ms = started.elapsed().as_millis() as u64;
    let line = format!(
        "{method} {path} host={host} \u{2192} anthropic-pass error {} ms={ms}",
        code.as_str()
    );
    let event = PassEvent {
        method: method.to_owned(),
        path: path.to_owned(),
        host: host.to_owned(),
        status: 0,
        bytes: 0,
        ms,
        word: PassWord::Error(code),
    };
    (ctx.note)(&line);
    if let Some(on_pass) = &ctx.on_pass {
        (on_pass)(&event);
    }
    unreachable_answer()
}

/// The pass-through's unreachable answer: always 502, and the message carries no code.
fn unreachable_answer() -> Response<RespBody> {
    anthropic_error(
        StatusCode::BAD_GATEWAY,
        "api_error",
        "huddle-router router: anthropic unreachable",
    )
}
