//! huddle-router: server.

use std::convert::Infallible;
use std::io;
use std::net::{Ipv4Addr, SocketAddr};
use std::pin::Pin;
use std::sync::Arc;
use std::time::Duration;

use std::task::{Context, Poll};

use http_body_util::combinators::BoxBody;
use http_body_util::{BodyExt, Full};
use hyper::body::{Bytes, Frame, Incoming};
use hyper::header::{CONNECTION, CONTENT_LENGTH, CONTENT_TYPE, HOST, HeaderValue};
use hyper::{Method, Request, Response, StatusCode};
use tokio::net::TcpListener;
use tokio::sync::watch;

use crate::dashboard::{Dashboard, LIVE_PATH, PAGE_PATH, STATE_PATH};
use crate::health::{HEALTH_PATH, health_answer};

pub const ROUTER_NAME: &str = "huddle-router";

/// Sleep after a failed accept before accepting again; a transient failure must not end the server.
const ACCEPT_BACKOFF: Duration = Duration::from_millis(20);

/// A body error cuts the connection (the only honest ending once a head went out).
#[derive(Debug)]
pub struct BodyCut;

impl std::fmt::Display for BodyCut {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("connection cut")
    }
}

impl std::error::Error for BodyCut {}

pub type RespBody = BoxBody<Bytes, BodyCut>;

pub type HandlerFuture = Pin<Box<dyn Future<Output = Response<RespBody>> + Send>>;

pub type Handler = Arc<dyn Fn(Request<Incoming>) -> HandlerFuture + Send + Sync>;

#[derive(Debug, Clone, Copy)]
pub struct ServerLimits {
    pub header_read: Duration,
}

impl Default for ServerLimits {
    fn default() -> Self {
        Self {
            header_read: Duration::from_secs(125),
        }
    }
}

pub fn full_body(bytes: impl Into<Bytes>) -> RespBody {
    BoxBody::new(Full::new(bytes.into()).map_err(|_| BodyCut))
}

pub fn empty_body() -> RespBody {
    full_body(Bytes::new())
}

/// `{"type":"error","error":{"type":"<typ>","message":"<message>"}}`, compact, Content-Type application/json.
pub fn anthropic_error(status: StatusCode, typ: &str, message: &str) -> Response<RespBody> {
    // The wrapper's own type is always "error"; the detail carries the failure's type.
    let body = AnthropicError {
        typ: "error",
        error: AnthropicErrorDetail { typ, message },
    };
    // Serializing this fixed shape cannot fail; an empty body keeps the answer well-formed.
    let json = serde_json::to_string(&body).unwrap_or_default();
    json_answer(status, json)
}

#[derive(serde::Serialize)]
struct AnthropicError<'a> {
    #[serde(rename = "type")]
    typ: &'a str,
    error: AnthropicErrorDetail<'a>,
}

#[derive(serde::Serialize)]
struct AnthropicErrorDetail<'a> {
    #[serde(rename = "type")]
    typ: &'a str,
    message: &'a str,
}

/// `Content-Type: application/json`, the given compact JSON body.
pub fn json_answer(status: StatusCode, json: String) -> Response<RespBody> {
    let mut response = Response::new(full_body(json));
    *response.status_mut() = status;
    response
        .headers_mut()
        .insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
    response
}

/// The request body limit: 64 MiB.
pub const BODY_LIMIT: u64 = 67_108_864;

/// A request path the router may forward: exactly one leading slash. `//host/x` would make the
/// upstream URL resolve to another host, so it is refused, and so is anything absolute.
pub fn safe_path(path_and_query: &str) -> bool {
    path_and_query.starts_with('/') && !path_and_query.starts_with("//")
}

/// Reads the whole body, or answers why it could not be: a bad path 400, over the limit 413, a
/// body that errors cuts the connection.
// The err side is the whole answer; the 128-byte move only happens on a failure path.
#[allow(clippy::result_large_err)]
pub async fn receive(
    req: Request<Incoming>,
    limit: u64,
) -> Result<(hyper::http::request::Parts, Bytes), Response<RespBody>> {
    if !safe_path(req.uri().path_and_query().map_or("", |pq| pq.as_str())) {
        return Err(anthropic_error(
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "huddle-router router: bad request path",
        ));
    }
    let (parts, mut body) = req.into_parts();
    // A body whose length is announced within the limit is read into one buffer of that size;
    // anything else grows as it arrives.
    let announced = parts
        .headers
        .get(CONTENT_LENGTH)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse::<u64>().ok())
        .filter(|&len| (1..=limit).contains(&len));
    let mut buf = announced.map_or_else(Vec::new, |len| Vec::with_capacity(len as usize));
    let mut size: u64 = 0;
    let mut too_large = false;
    while let Some(frame) = body.frame().await {
        let frame = match frame {
            // A drain error is ignored; the over-limit answer goes out anyway.
            Err(_) if too_large => break,
            // The client's body failed: cut the connection, nothing is delivered.
            Err(_) => return Err(cut_response()),
            Ok(frame) => frame,
        };
        let Some(datum) = frame.into_data().ok() else {
            continue;
        };
        size = size.saturating_add(datum.len() as u64);
        if size > limit {
            // Past the limit nothing is kept; the rest is drained before answering, so a client
            // still uploading can read the 413.
            too_large = true;
            continue;
        }
        buf.extend_from_slice(&datum);
    }
    if too_large {
        let mut response = anthropic_error(
            StatusCode::PAYLOAD_TOO_LARGE,
            "request_too_large",
            "huddle-router router: request body over 64 MiB",
        );
        response
            .headers_mut()
            .insert(CONNECTION, HeaderValue::from_static("close"));
        return Err(response);
    }
    Ok((parts, Bytes::from(buf)))
}

/// A response whose body errors immediately: hyper closes the connection, no complete answer
/// goes out.
pub fn cut_response() -> Response<RespBody> {
    let mut response = Response::new(BoxBody::new(CutBody));
    // A status that never reads as success; the connection carries no complete answer.
    *response.status_mut() = StatusCode::INTERNAL_SERVER_ERROR;
    response
}

/// A body that errors at its first poll.
struct CutBody;

impl hyper::body::Body for CutBody {
    type Data = Bytes;
    type Error = BodyCut;

    fn poll_frame(
        self: Pin<&mut Self>,
        _: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Self::Data>, Self::Error>>> {
        Poll::Ready(Some(Err(BodyCut)))
    }
}

/// The entry flow's collaborators: what CONNECT gets, what every other request gets, and where
/// the provider names for the health answer come from.
pub struct EntryCtx {
    /// `Some(port)` in production; `None` skips the Host guard in tests that bind port 0 only.
    pub port: Option<u16>,
    /// Serves CONNECT requests.
    pub connect: Handler,
    /// Serves every request that is neither CONNECT nor the health path: it reads
    /// the body with `receive` and routes it.
    pub forward: Handler,
    /// The provider names the health answer lists, read fresh per request so the
    /// list stays current.
    pub providers: Arc<dyn Fn() -> Vec<String> + Send + Sync>,
    /// The page, the state and the live feed; `None` leaves those paths to `forward`.
    pub dashboard: Option<Arc<Dashboard>>,
}

/// True, after answering 421, when the request does not name this port on the loopback: a
/// DNS-rebinding guard. The compare ignores case and accepts the IPv6 loopback; a missing Host
/// header is a mismatch.
fn refuse_host(req: &Request<Incoming>, port: u16) -> bool {
    let accepted = req
        .headers()
        .get(HOST)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|host| {
            host.eq_ignore_ascii_case(&format!("127.0.0.1:{port}"))
                || host.eq_ignore_ascii_case(&format!("localhost:{port}"))
                || host.eq_ignore_ascii_case(&format!("[::1]:{port}"))
        });
    !accepted
}

/// The entry flow: CONNECT first, then the Host guard, then the health path (never forwarded),
/// then the dashboard's paths, then forward.
pub fn entry_handler(ctx: Arc<EntryCtx>) -> Handler {
    Arc::new(move |req: Request<Incoming>| {
        let ctx = ctx.clone();
        Box::pin(async move {
            // A CONNECT names the tunnel's target in the authority, so the Host guard cannot
            // apply to it; only a proxy client sends one.
            if req.method() == Method::CONNECT {
                return (ctx.connect)(req).await;
            }
            if ctx.port.is_some_and(|port| refuse_host(&req, port)) {
                return anthropic_error(
                    StatusCode::MISDIRECTED_REQUEST,
                    "invalid_request_error",
                    "huddle-router router: wrong Host",
                );
            }
            // The health path is answered here, whatever the method: nothing is forwarded from it.
            if req.uri().path() == HEALTH_PATH {
                if req.method() == Method::GET {
                    return health_answer(&(ctx.providers)());
                }
                return anthropic_error(
                    StatusCode::NOT_FOUND,
                    "not_found_error",
                    "huddle-router: no such path",
                );
            }
            if let Some(dashboard) = &ctx.dashboard
                && let Some(answer) = dashboard_answer(dashboard, &req)
            {
                return answer;
            }
            (ctx.forward)(req).await
        })
    })
}

/// The dashboard's answer for this request, or none when it is not a dashboard path. A bare
/// `GET /` is the page; a wrong method on the state and live paths is a 404, as on health.
fn dashboard_answer(
    dashboard: &Arc<Dashboard>,
    req: &Request<Incoming>,
) -> Option<Response<RespBody>> {
    let get = req.method() == Method::GET;
    match req.uri().path() {
        PAGE_PATH if get => Some(dashboard.page_answer()),
        STATE_PATH if get => Some(dashboard.state_answer()),
        LIVE_PATH if get => Some(dashboard.live_answer()),
        STATE_PATH | LIVE_PATH => Some(anthropic_error(
            StatusCode::NOT_FOUND,
            "not_found_error",
            "huddle-router: no such path",
        )),
        _ => None,
    }
}

/// Binds `127.0.0.1:port`.
pub async fn bind(port: u16) -> io::Result<TcpListener> {
    TcpListener::bind(SocketAddr::from((Ipv4Addr::LOCALHOST, port))).await
}

/// Serves `listener` until `stop` becomes true, then stops accepting and returns.
pub async fn serve(
    listener: TcpListener,
    handler: Handler,
    limits: ServerLimits,
    mut stop: watch::Receiver<bool>,
) -> io::Result<()> {
    loop {
        tokio::select! {
            accepted = listener.accept() => {
                let (stream, _addr) = match accepted {
                    Ok(conn) => conn,
                    // An accept error is transient (EMFILE and friends): back off, keep serving.
                    Err(_) => {
                        tokio::time::sleep(ACCEPT_BACKOFF).await;
                        continue;
                    }
                };
                let _ = stream.set_nodelay(true);
                let handler = handler.clone();
                tokio::spawn(async move {
                    let svc = hyper::service::service_fn(move |req: Request<Incoming>| {
                        let handler = handler.clone();
                        async move { Ok::<_, Infallible>(handler(req).await) }
                    });
                    // One timer covers the header read and the keep-alive idle: an idle connection
                    // closes at the header limit, and a request in flight has no read or write timeout.
                    let _ = hyper::server::conn::http1::Builder::new()
                        .timer(hyper_util::rt::TokioTimer::new())
                        .header_read_timeout(limits.header_read)
                        .keep_alive(true)
                        .serve_connection(hyper_util::rt::TokioIo::new(stream), svc)
                        .with_upgrades()
                        .await;
                });
            }
            _ = wait_stop(&mut stop) => return Ok(()),
        }
    }
}

async fn wait_stop(stop: &mut watch::Receiver<bool>) {
    if stop.wait_for(|v| *v).await.is_err() {
        // A dropped sender means nobody can stop this server, so it never fires.
        std::future::pending::<()>().await;
    }
}
