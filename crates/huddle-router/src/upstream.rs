//! huddle-router: upstream.
//!
//! The keep-alive HTTP/1.1 client for upstream calls. One request is one attempt on a pooled or
//! fresh connection, retried once on a fresh connection when a reused socket died before the
//! whole body was handed over; the body goes out in small frames so the last-byte moment is
//! known; transport failures carry errno-style codes. The proxy environment applies: a target
//! whose proxy variable names a proxy rides a CONNECT tunnel, and a value naming huddle-router
//! itself is no proxy.

use std::collections::{HashMap, VecDeque};
use std::error::Error as StdError;
use std::fmt;
use std::io::{self, ErrorKind};
use std::net::{IpAddr, Ipv6Addr, SocketAddr};
use std::pin::Pin;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use crate::trace::{Conn, Phase, Trace};
use huddle_config::env::Env;
use huddle_config::paths::DEFAULT_ROUTER_PORT;
use hyper::body::{Body, Bytes, Frame, Incoming, SizeHint};
use hyper::client::conn::http1::{SendRequest, handshake};
use hyper::header::{CONTENT_LENGTH, HOST, HeaderMap, HeaderValue};
use hyper::{Error as HyperError, Method, Request, StatusCode, Uri};
use hyper_util::rt::TokioIo;
use rustls::pki_types::ServerName;
use rustls::{ClientConfig, RootCertStore};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::net::{TcpStream, lookup_host};
use tokio::sync::mpsc;
use tokio::sync::{Notify, OwnedSemaphorePermit, Semaphore};
use tokio_rustls::TlsConnector;
use tokio_rustls::client::TlsStream;

/// How long an upstream that sends nothing is given up on: silence, never a total duration.
pub const IDLE_TIMEOUT: Duration = Duration::from_secs(600);

const MAX_CONNS_PER_HOST: usize = 256;
const MAX_IDLE_PER_HOST: usize = 16;
const MAX_IDLE_TOTAL: usize = 256;
/// An idle connection older than this is discarded when another returns and on checkout.
const IDLE_CONN_TTL: Duration = Duration::from_secs(90);
/// The largest body slice handed to the wire in one frame.
const FRAME: usize = 64 * 1024;

/// The errno-style code a failure reports, with `ECERT` for certificate failures.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FailureCode {
    ETimedOut,
    ECanceled,
    ECert,
    EConnReset,
    EPipe,
    EConnRefused,
    EUpstream,
    EAborted,
}

impl FailureCode {
    pub fn as_str(&self) -> &'static str {
        match self {
            FailureCode::ETimedOut => "ETIMEDOUT",
            FailureCode::ECanceled => "ECANCELED",
            FailureCode::ECert => "ECERT",
            FailureCode::EConnReset => "ECONNRESET",
            FailureCode::EPipe => "EPIPE",
            FailureCode::EConnRefused => "ECONNREFUSED",
            FailureCode::EUpstream => "EUPSTREAM",
            FailureCode::EAborted => "EABORTED",
        }
    }
}

/// How one upstream request failed.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Failure {
    pub code: FailureCode,
    pub client_closed: bool,
    /// Where the last attempt ended.
    pub trace: Trace,
}

/// One failed attempt's verdict: the code a final report carries, whether the attempt ran on a
/// pooled socket, whether the body's last byte was handed to the transport, whether the socket
/// went away mid-request, and whether the body can still be read from byte 0 once the attempt
/// has ended.
struct AttemptFailure {
    code: FailureCode,
    trace: Trace,
    reused: bool,
    handed_over: bool,
    closed_early: bool,
    replayable: bool,
}

impl From<AttemptFailure> for Failure {
    fn from(f: AttemptFailure) -> Failure {
        Failure {
            code: f.code,
            client_closed: false,
            trace: f.trace,
        }
    }
}

/// A reused socket earns one retry on a fresh connection, and only before the whole body was
/// handed over: after it the upstream may hold the whole request, so it is never asked twice. A
/// body that dropped a chunk past its replay prefix cannot be sent again from byte 0, so its
/// failure is final and the client gets the cut.
fn retryable(f: &AttemptFailure) -> bool {
    f.reused && !f.handed_over && f.closed_early && f.replayable
}

/// Headers that describe one connection, never the message: they are not copied across a hop.
const HOP_BY_HOP: [&str; 11] = [
    "connection",
    "content-length",
    "host",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "proxy-connection",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
];

/// Response headers that describe one hop's connection or framing, not the answer.
const RESPONSE_HOP: [&str; 3] = ["connection", "keep-alive", "transfer-encoding"];

/// The upstream URL: the target's origin, its path with one trailing `/` trimmed, then the
/// request's own path and query. Pure string assembly, never URL resolution, so a request path
/// cannot change the host.
pub fn upstream_url(target: &Uri, request_path_and_query: &str) -> Result<Uri, FailureCode> {
    let Some(scheme) = target.scheme_str() else {
        return Err(FailureCode::EUpstream);
    };
    if scheme != "http" && scheme != "https" {
        return Err(FailureCode::EUpstream);
    }
    let Some(authority) = target.authority() else {
        return Err(FailureCode::EUpstream);
    };
    let mut path = target.path().to_owned();
    if path.ends_with('/') {
        path.pop();
    }
    let assembled = format!("{scheme}://{authority}{path}{request_path_and_query}");
    Uri::try_from(assembled).map_err(|_| FailureCode::EUpstream)
}

/// The client's headers for the next hop: hop-by-hop ones dropped, `Host` and `Content-Length`
/// set for the target. Every other header passes unchanged; nothing is added.
pub fn forward_headers(incoming: &HeaderMap, target: &Uri, length: u64) -> HeaderMap {
    let mut out = HeaderMap::new();
    for (name, value) in incoming.iter() {
        if HOP_BY_HOP.contains(&name.as_str()) {
            continue;
        }
        out.append(name.clone(), value.clone());
    }
    if let Some(authority) = target.authority()
        && let Ok(host) = HeaderValue::from_str(authority.as_str())
    {
        out.insert(HOST, host);
    }
    if let Ok(length) = HeaderValue::from_str(&length.to_string()) {
        out.insert(CONTENT_LENGTH, length);
    }
    out
}

/// The upstream's response headers for the client: the connection-level ones belong to each hop,
/// and so does the framing.
pub fn response_headers(incoming: &HeaderMap) -> HeaderMap {
    let mut out = HeaderMap::new();
    for (name, value) in incoming.iter() {
        if RESPONSE_HOP.contains(&name.as_str()) {
            continue;
        }
        out.append(name.clone(), value.clone());
    }
    out
}

/// What a streamed body hands its reader next.
pub(crate) enum NextChunk {
    Chunk(Bytes),
    End,
    Failed,
    /// The reader is behind a chunk the body already dropped: it cannot be read from byte 0.
    NotReplayable,
}

/// The bytes of a streamed body kept from byte 0 for a retry.
pub const REPLAY_PREFIX: u64 = 1024 * 1024;
/// How far past the replay prefix the client may run ahead of the upstream before `push` waits.
pub const SEND_WINDOW: u64 = 256 * 1024;

struct StreamState {
    /// The chunks still held, from chunk number `first` on.
    chunks: VecDeque<Bytes>,
    first: usize,
    /// How many chunks start inside the prefix; they are held until a chunk past it is handed
    /// out, and every chunk past it is dropped the moment it is handed out.
    in_prefix: usize,
    filled: u64,
    failed: bool,
    /// The furthest byte any reader has taken.
    taken: u64,
    /// Live holds, readers included.
    holds: usize,
    /// The route will start no reader again: the body is abandoned once the last hold ends.
    settled: bool,
    /// Nobody will read the body again: nothing is held, and each later chunk is dropped as it
    /// arrives.
    abandoned: bool,
}

impl StreamState {
    /// Chunk number `index`, if it has arrived. A chunk past the prefix leaves the state as it
    /// is handed out, and the prefix leaves with the first one: no reader can replay the body
    /// from byte 0 after that.
    fn hand_out(&mut self, index: usize) -> Option<Bytes> {
        let offset = index.checked_sub(self.first)?;
        if offset >= self.chunks.len() {
            return None;
        }
        if index < self.in_prefix {
            return self.chunks.get(offset).cloned();
        }
        let held = self.chunks.drain(..offset).count();
        self.first += held + 1;
        self.chunks.pop_front()
    }

    /// Drops every held chunk and keeps none from now on; the chunks are returned so they are
    /// freed after the lock is released.
    fn abandon(&mut self) -> VecDeque<Bytes> {
        self.abandoned = true;
        std::mem::take(&mut self.chunks)
    }

    /// The bytes past the prefix that no reader has taken yet.
    fn unsent(&self, prefix: u64) -> u64 {
        self.filled.saturating_sub(self.taken.max(prefix))
    }
}

/// A body filled while it is being relayed; an attempt reads it from byte 0 while it is still
/// replayable.
pub struct StreamedBody {
    total: u64,
    prefix: u64,
    window: u64,
    state: Mutex<StreamState>,
    /// Wakes readers when a chunk arrives or the body fails.
    notify: Notify,
    /// Wakes the filler when a reader takes a chunk or a hold ends.
    room: Notify,
}

impl StreamedBody {
    pub fn new(len: u64) -> Arc<StreamedBody> {
        StreamedBody::with_limits(len, REPLAY_PREFIX, SEND_WINDOW)
    }

    /// A body whose filler waits once more than `window` bytes past the first `prefix` bytes
    /// are ahead of every reader.
    pub fn with_limits(len: u64, prefix: u64, window: u64) -> Arc<StreamedBody> {
        Arc::new(StreamedBody {
            total: len,
            prefix,
            window,
            state: Mutex::new(StreamState {
                chunks: VecDeque::new(),
                first: 0,
                in_prefix: 0,
                filled: 0,
                failed: false,
                taken: 0,
                holds: 0,
                settled: false,
                abandoned: false,
            }),
            notify: Notify::new(),
            room: Notify::new(),
        })
    }

    fn lock(&self) -> MutexGuard<'_, StreamState> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Adds the next chunk, then waits while the unsent bytes past the prefix exceed the
    /// window, so the client is read no faster than the upstream takes the body and TCP slows
    /// the client down. A body nobody holds never waits: no reader can come to make room. An
    /// abandoned body only counts the chunk and drops it.
    pub async fn push(&self, chunk: Bytes) {
        {
            let mut state = self.lock();
            if state.abandoned {
                state.filled += chunk.len() as u64;
                return;
            }
            if state.filled < self.prefix {
                state.in_prefix += 1;
            }
            state.filled += chunk.len() as u64;
            state.chunks.push_back(chunk);
        }
        self.notify.notify_waiters();
        loop {
            let mut room = std::pin::pin!(self.room.notified());
            {
                let state = self.lock();
                if state.failed
                    || state.abandoned
                    || state.holds == 0
                    || state.unsent(self.prefix) <= self.window
                {
                    return;
                }
                room.as_mut().enable();
            }
            room.await;
        }
    }

    /// Keeps `push` waiting for room while the body may still be read: the route takes one
    /// before any reader exists and drops it once it is done with the body.
    #[must_use = "the hold ends when it is dropped"]
    pub fn hold(self: &Arc<Self>) -> BodyHold {
        self.lock().holds += 1;
        BodyHold {
            stream: Arc::clone(self),
            settles: false,
        }
    }

    /// The route's hold on a relayed body. When it ends the route starts no reader again, so
    /// once the last reader also ends the body is abandoned. A body read whole never takes one:
    /// it still needs `take_whole`.
    #[must_use = "the hold ends when it is dropped"]
    pub fn route_hold(self: &Arc<Self>) -> BodyHold {
        self.lock().holds += 1;
        BodyHold {
            stream: Arc::clone(self),
            settles: true,
        }
    }

    /// One attempt's read of the body from byte 0; it holds the body while it lives.
    pub(crate) fn reader(self: &Arc<Self>) -> BodyReader {
        BodyReader {
            hold: self.hold(),
            index: 0,
            at: 0,
        }
    }

    fn release(&self, settles: bool) {
        let dropped = {
            let mut state = self.lock();
            state.holds = state.holds.saturating_sub(1);
            state.settled |= settles;
            if state.settled && state.holds == 0 && !state.abandoned {
                state.abandon()
            } else {
                VecDeque::new()
            }
        };
        drop(dropped);
        self.room.notify_waiters();
    }

    /// Nobody will read the body again: what it holds is freed, a waiting `push` returns, every
    /// later chunk is dropped as it arrives, and a reader gets `NotReplayable`.
    pub fn abandon(&self) {
        let dropped = self.lock().abandon();
        drop(dropped);
        self.notify.notify_waiters();
        self.room.notify_waiters();
    }

    /// The client stopped short; the body will never be whole.
    pub fn fail(&self) {
        self.lock().failed = true;
        self.notify.notify_waiters();
        self.room.notify_waiters();
    }

    pub fn client_failed(&self) -> bool {
        self.lock().failed
    }

    pub fn is_whole(&self) -> bool {
        self.filled() == self.total
    }

    /// True while every chunk is still held, so a new attempt can read the body from byte 0.
    pub fn replayable(&self) -> bool {
        let state = self.lock();
        state.first == 0 && !state.abandoned
    }

    pub fn filled(&self) -> u64 {
        self.lock().filled
    }

    pub fn len(&self) -> u64 {
        self.total
    }

    pub fn is_empty(&self) -> bool {
        self.total == 0
    }

    /// Moves the chunks out once the body is whole: one chunk is returned as it is, several are
    /// joined once, and the stream retains nothing afterwards. A body still filling, or one that
    /// dropped a chunk or was abandoned, stays as it is.
    pub fn take_whole(&self) -> Option<Bytes> {
        let mut state = self.lock();
        if state.filled != self.total || state.first != 0 || state.abandoned {
            return None;
        }
        let mut chunks = std::mem::take(&mut state.chunks);
        drop(state);
        if chunks.len() == 1 {
            return chunks.pop_back();
        }
        let mut whole = Vec::with_capacity(self.total as usize);
        for chunk in &chunks {
            whole.extend_from_slice(chunk);
        }
        Some(Bytes::from(whole))
    }

    /// The bytes the body holds right now. Read by the footprint tests only.
    #[doc(hidden)]
    pub fn retained_bytes(&self) -> u64 {
        self.lock()
            .chunks
            .iter()
            .map(|chunk| chunk.len() as u64)
            .sum()
    }

    /// The chunk at `index`, waiting while the body is still filling; `at` is the reader's byte
    /// position. Taking a chunk past every other reader wakes the filler. The lock is never held
    /// across the wait.
    async fn next_chunk(&self, index: &mut usize, at: &mut u64) -> NextChunk {
        loop {
            let mut wait = std::pin::pin!(self.notify.notified());
            {
                let mut state = self.lock();
                if *index < state.first || state.abandoned {
                    return NextChunk::NotReplayable;
                }
                if let Some(chunk) = state.hand_out(*index) {
                    *index += 1;
                    *at += chunk.len() as u64;
                    let ahead = *at > state.taken;
                    if ahead {
                        state.taken = *at;
                    }
                    drop(state);
                    if ahead {
                        self.room.notify_waiters();
                    }
                    return NextChunk::Chunk(chunk);
                }
                if state.filled == self.total {
                    return NextChunk::End;
                }
                if state.failed {
                    return NextChunk::Failed;
                }
                wait.as_mut().enable();
            }
            wait.await;
        }
    }
}

/// Keeps a streamed body's filler waiting for room while it lives.
pub struct BodyHold {
    stream: Arc<StreamedBody>,
    /// Ending this hold says the route starts no reader again.
    settles: bool,
}

impl Drop for BodyHold {
    fn drop(&mut self) {
        self.stream.release(self.settles);
    }
}

/// Reads a streamed body in order from byte 0.
pub(crate) struct BodyReader {
    hold: BodyHold,
    index: usize,
    at: u64,
}

impl BodyReader {
    /// The next chunk in order, waiting while the body is still filling.
    pub(crate) async fn next_chunk(&mut self) -> NextChunk {
        self.hold
            .stream
            .next_chunk(&mut self.index, &mut self.at)
            .await
    }
}

/// The body a request carries.
#[derive(Clone)]
pub enum RequestBody {
    Whole(Bytes),
    Streamed(Arc<StreamedBody>),
}

/// One request upstream.
pub struct UpstreamRequest {
    pub url: Uri,
    pub method: Method,
    pub headers: HeaderMap,
    pub body: RequestBody,
}

/// The client's side of one answer. Return `conn` to the pool with `finish` once the body ended.
pub struct UpstreamAnswer {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body: Incoming,
    pub conn: ConnGuard,
    /// The tunnel a `101 Switching Protocols` answer hands over; `None` for every other status.
    pub upgrade: Option<hyper::upgrade::OnUpgrade>,
    /// The peer and connection kind this answer rode.
    pub trace: Trace,
}

/// Set once the request's last byte was handed to the transport.
#[derive(Default)]
struct Handed {
    flag: std::sync::atomic::AtomicBool,
    notify: Notify,
}

impl Handed {
    fn mark(&self) {
        self.flag.store(true, std::sync::atomic::Ordering::Release);
        self.notify.notify_waiters();
    }

    fn is_marked(&self) -> bool {
        self.flag.load(std::sync::atomic::Ordering::Acquire)
    }

    /// Waits for `mark`, missing no wake that ran before the wait registered.
    async fn wait(&self) {
        if self.flag.load(std::sync::atomic::Ordering::Acquire) {
            return;
        }
        let mut notified = std::pin::pin!(self.notify.notified());
        notified.as_mut().enable();
        if self.flag.load(std::sync::atomic::Ordering::Acquire) {
            return;
        }
        notified.await;
    }
}

/// The error a body reports when its source stopped short.
#[derive(Debug)]
struct BodyDropped;

impl fmt::Display for BodyDropped {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("the body source stopped short")
    }
}

impl StdError for BodyDropped {}

/// The wire form of either request body: a pump feeds chunks in, and handing the last one out is
/// observable.
struct AttemptBody {
    rx: mpsc::Receiver<Result<Bytes, BodyDropped>>,
    sent: u64,
    total: u64,
    handed: Arc<Handed>,
}

impl Body for AttemptBody {
    type Data = Bytes;
    type Error = BodyDropped;

    fn poll_frame(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Self::Data>, Self::Error>>> {
        let this = self.get_mut();
        match this.rx.poll_recv(cx) {
            Poll::Ready(Some(Ok(chunk))) => {
                this.sent += chunk.len() as u64;
                if this.sent >= this.total {
                    this.handed.mark();
                }
                Poll::Ready(Some(Ok(Frame::data(chunk))))
            }
            Poll::Ready(Some(Err(err))) => Poll::Ready(Some(Err(err))),
            Poll::Ready(None) => Poll::Ready(None),
            Poll::Pending => Poll::Pending,
        }
    }

    fn size_hint(&self) -> SizeHint {
        let mut hint = SizeHint::default();
        hint.set_exact(self.total);
        hint
    }
}

/// A dialled socket, plain or TLS.
trait UpstreamIo: AsyncRead + AsyncWrite + Send + Unpin {}

impl UpstreamIo for TcpStream {}

impl UpstreamIo for TlsStream<TcpStream> {}

/// How a socket was dialled: the peer and the time each dial phase took.
#[derive(Debug, Clone, Copy, Default)]
struct Dial {
    peer: Option<SocketAddr>,
    dns_ms: Option<u64>,
    connect_ms: Option<u64>,
    tls_ms: Option<u64>,
}

impl Dial {
    /// The trace of an attempt on this socket that stands in `phase`; a pooled socket reports
    /// its peer and no dial times.
    fn trace(&self, reused: bool, phase: Phase) -> Trace {
        if reused {
            return Trace {
                phase,
                peer: self.peer,
                conn: Conn::Reused,
                dns_ms: None,
                connect_ms: None,
                tls_ms: None,
            };
        }
        Trace {
            phase,
            peer: self.peer,
            conn: Conn::Fresh,
            dns_ms: self.dns_ms,
            connect_ms: self.connect_ms,
            tls_ms: self.tls_ms,
        }
    }
}

/// A connected socket's sender, its pool permit and how it was dialled.
struct Live {
    sender: SendRequest<AttemptBody>,
    permit: OwnedSemaphorePermit,
    dial: Dial,
}

struct IdleConn {
    live: Live,
    returned_at: Instant,
}

/// A dial that failed: the error and where the attempt stood.
struct DialError {
    error: io::Error,
    trace: Trace,
}

struct HostPool {
    idle: Vec<IdleConn>,
    permits: Arc<Semaphore>,
}

impl HostPool {
    fn new() -> HostPool {
        HostPool {
            idle: Vec::new(),
            permits: Arc::new(Semaphore::new(MAX_CONNS_PER_HOST)),
        }
    }
}

fn lock_pools(
    pools: &Mutex<HashMap<String, HostPool>>,
) -> MutexGuard<'_, HashMap<String, HostPool>> {
    pools.lock().unwrap_or_else(PoisonError::into_inner)
}

/// Returns a connection to its host pool, dropping every host's idle connections past `ttl` and
/// the oldest ones beyond the limits.
fn push_idle(pools: &mut HashMap<String, HostPool>, key: String, conn: IdleConn, ttl: Duration) {
    for pool in pools.values_mut() {
        pool.idle.retain(|idle| idle.returned_at.elapsed() <= ttl);
    }
    let host = pools.entry(key).or_insert_with(HostPool::new);
    host.idle.push(conn);
    host.idle.sort_by_key(|conn| conn.returned_at);
    while host.idle.len() > MAX_IDLE_PER_HOST {
        host.idle.remove(0);
    }
    while idle_total(pools) > MAX_IDLE_TOTAL {
        let oldest = pools
            .iter()
            .filter(|(_, host)| !host.idle.is_empty())
            .min_by_key(|(_, host)| host.idle[0].returned_at)
            .map(|(key, _)| key.clone());
        let Some(oldest) = oldest else {
            break;
        };
        if let Some(host) = pools.get_mut(&oldest) {
            host.idle.remove(0);
        }
    }
}

fn idle_total(pools: &HashMap<String, HostPool>) -> usize {
    pools.values().map(|host| host.idle.len()).sum()
}

/// The pool key of a target: `<scheme>|<authority>|<route>`, the route the proxy's `host:port`
/// or `direct`, so connections through different proxies never mix.
fn pool_key(url: &Uri, proxy: Option<&ProxyTarget>) -> Option<String> {
    let scheme = url.scheme_str()?;
    if scheme != "http" && scheme != "https" {
        return None;
    }
    let authority = url.authority()?;
    let route = match proxy {
        Some(proxy) => format!("{}:{}", proxy.host, proxy.port),
        None => "direct".to_owned(),
    };
    Some(format!("{scheme}|{authority}|{route}"))
}

/// The target's `(host, port, is_tls)`; `http` and `https` only, with the default ports.
fn target_parts(url: &Uri) -> Option<(&str, u16, bool)> {
    match url.scheme_str()? {
        "http" => Some((url.host()?, url.port_u16().unwrap_or(80), false)),
        "https" => Some((url.host()?, url.port_u16().unwrap_or(443), true)),
        _ => None,
    }
}

/// The request line's origin form: the path and query of the target URL.
fn origin_form(url: &Uri) -> Result<Uri, FailureCode> {
    let path_and_query = url.path_and_query().map(|pq| pq.as_str()).unwrap_or("/");
    Uri::try_from(path_and_query.to_owned()).map_err(|_| FailureCode::EUpstream)
}

/// The proxy a request rides: the proxy's own address, plus the credentials the variable carried.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProxyTarget {
    /// The proxy's host, brackets stripped.
    pub host: String,
    pub port: u16,
    /// The percent-decoded user and password.
    pub user: Option<(String, String)>,
}

/// Reads one variable: the lowercase key first, then the uppercase one; the value is trimmed and
/// an empty one counts as unset.
fn env_value<'a>(env: &'a Env, name: &str) -> Option<&'a str> {
    [name.to_ascii_lowercase(), name.to_ascii_uppercase()]
        .into_iter()
        .find_map(|key| {
            env.get(&key)
                .map(str::trim)
                .filter(|value| !value.is_empty())
        })
}

/// True when the host is this machine: `localhost`, `::1`, or a literal address inside
/// `127.0.0.0/8`. A name that merely starts with `127.` is not.
pub fn is_loopback_host(host: &str) -> bool {
    let bare = host
        .strip_prefix('[')
        .and_then(|rest| rest.strip_suffix(']'))
        .unwrap_or(host);
    bare == "localhost" || bare.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

/// True when `NO_PROXY` exempts `host:port`. Entries split on commas and whitespace; `*` exempts
/// everything; an entry with a port applies to that port only; bare entries match the host or a
/// domain suffix (`corp`, `.corp`, `*.corp`); an IPv6 literal entry matches that literal and a
/// CIDR entry matches a literal IP target inside it.
pub fn no_proxy(host: &str, port: u16, env: &Env) -> bool {
    let Some(list) = env_value(env, "NO_PROXY") else {
        return false;
    };
    let lower = host.to_ascii_lowercase();
    for entry in list
        .split(|c: char| c == ',' || c.is_whitespace())
        .filter(|entry| !entry.is_empty())
    {
        if entry == "*" {
            return true;
        }
        if ipv6_entry_matches(entry, host, port) || cidr_entry_matches(entry, host) {
            return true;
        }
        let (name, only) = match entry.split_once(':') {
            Some((name, rest)) => (
                name,
                // The port is the entry's second segment; anything after another colon is not
                // part of it.
                Some(rest.split_once(':').map_or(rest, |(second, _)| second)),
            ),
            None => (entry, None),
        };
        if only.is_some_and(|only| only.parse::<u16>() != Ok(port)) {
            continue;
        }
        let starless = name.strip_prefix("*.").unwrap_or(name);
        let exempt = starless
            .strip_prefix('.')
            .unwrap_or(starless)
            .to_ascii_lowercase();
        if lower == exempt
            || lower
                .strip_suffix(&exempt)
                .is_some_and(|rest| !rest.is_empty() && rest.ends_with('.'))
        {
            return true;
        }
    }
    false
}

/// The host text and port scope of an entry that is an IPv6 literal: bare (`::1`) or bracketed
/// with an optional port (`[::1]`, `[::1]:443`).
fn ipv6_entry(entry: &str) -> Option<(&str, Option<&str>)> {
    if let Some(inner) = entry.strip_prefix('[') {
        let (host, rest) = inner.split_once(']')?;
        return Some((host, rest.strip_prefix(':')));
    }
    entry.parse::<Ipv6Addr>().ok().map(|_| (entry, None))
}

fn ipv6_entry_matches(entry: &str, host: &str, port: u16) -> bool {
    let Some((entry_host, only)) = ipv6_entry(entry) else {
        return false;
    };
    only.is_none_or(|only| only.parse::<u16>() == Ok(port)) && host.eq_ignore_ascii_case(entry_host)
}

/// A CIDR entry (`10.0.0.0/8`, `2001:db8::/32`) matches a target that is a literal IP inside the
/// range; a target that is not an IP literal never matches.
fn cidr_entry_matches(entry: &str, host: &str) -> bool {
    let Some((addr_text, len_text)) = entry.split_once('/') else {
        return false;
    };
    let Ok(target) = host.parse::<IpAddr>() else {
        return false;
    };
    let (Ok(entry), Ok(len)) = (addr_text.parse::<IpAddr>(), len_text.parse::<u8>()) else {
        return false;
    };
    cidr_contains(entry, len, target)
}

/// Byte comparison of the range's prefix: the whole bytes first, then the bits of the partial
/// byte under a mask.
fn cidr_contains(entry: IpAddr, len: u8, ip: IpAddr) -> bool {
    match (entry, ip) {
        (IpAddr::V4(entry), IpAddr::V4(ip)) => {
            cidr_contains_bytes(&entry.octets(), len, 32, &ip.octets())
        }
        (IpAddr::V6(entry), IpAddr::V6(ip)) => {
            cidr_contains_bytes(&entry.octets(), len, 128, &ip.octets())
        }
        _ => false,
    }
}

fn cidr_contains_bytes(entry: &[u8], len: u8, width: u8, ip: &[u8]) -> bool {
    if len > width {
        return false;
    }
    let whole = (len / 8) as usize;
    let rest = len % 8;
    entry[..whole] == ip[..whole]
        && (rest == 0 || entry[whole] & !(u8::MAX >> rest) == ip[whole] & !(u8::MAX >> rest))
}

/// The proxy a request to `target` rides: `HTTPS_PROXY` for an `https` target, `HTTP_PROXY`
/// otherwise, `ALL_PROXY` when the specific one is unset. `NO_PROXY` stops it, and a value naming
/// huddle-router is dropped — routing the router's own upstream through the router is a loop. A
/// value that does not parse is no proxy, never an error.
pub fn proxy_for(target: &Uri, env: &Env) -> Option<ProxyTarget> {
    let scheme = target.scheme_str()?;
    if scheme != "http" && scheme != "https" {
        return None;
    }
    let host = target.host()?;
    let port = target
        .port_u16()
        .unwrap_or(if scheme == "https" { 443 } else { 80 });
    if no_proxy(host, port, env) {
        return None;
    }
    let specific = if scheme == "https" {
        "HTTPS_PROXY"
    } else {
        "HTTP_PROXY"
    };
    let raw = env_value(env, specific).or_else(|| env_value(env, "ALL_PROXY"))?;
    if names_huddle_router(raw) {
        return None;
    }
    parse_proxy(raw)
}

/// True when the value names huddle-router's canonical address.
fn names_huddle_router(raw: &str) -> bool {
    let Some(target) = parse_proxy(raw) else {
        return false;
    };
    is_loopback_host(&target.host) && target.port == DEFAULT_ROUTER_PORT
}

/// Parses one proxy value: `http://` added when it has no scheme, port 80 when the authority has
/// none, credentials split off and percent-decoded.
fn parse_proxy(raw: &str) -> Option<ProxyTarget> {
    let with_scheme;
    let text = if raw.contains("://") {
        raw
    } else {
        with_scheme = format!("http://{raw}");
        &with_scheme
    };
    let url: Uri = text.parse().ok()?;
    let authority = url.authority()?;
    let host = authority.host();
    if host.is_empty() {
        return None;
    }
    let user = authority.as_str().split_once('@').map(|(userinfo, _)| {
        let (user, pass) = userinfo.split_once(':').unwrap_or((userinfo, ""));
        (percent_decode(user), percent_decode(pass))
    });
    Some(ProxyTarget {
        host: host
            .strip_prefix('[')
            .and_then(|rest| rest.strip_suffix(']'))
            .unwrap_or(host)
            .to_owned(),
        port: url.port_u16().unwrap_or(80),
        user,
    })
}

/// `%XX` becomes its byte; a sequence that is not two hex digits stays literal.
fn percent_decode(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut at = 0;
    while at < bytes.len() {
        let hex = if bytes[at] == b'%' {
            text.get(at + 1..at + 3)
        } else {
            None
        };
        match hex.and_then(|hex| u8::from_str_radix(hex, 16).ok()) {
            Some(byte) => {
                out.push(byte);
                at += 3;
            }
            None => {
                out.push(bytes[at]);
                at += 1;
            }
        }
    }
    String::from_utf8(out).unwrap_or_else(|_| text.to_owned())
}

/// The largest CONNECT answer head read before the tunnel is given up on.
const CONNECT_HEAD_MAX: usize = 64 * 1024;

/// Opens a CONNECT tunnel to `host:port` through `proxy`: TCP to the proxy, the CONNECT request
/// with Basic credentials when they are set, and the answer read one byte at a time up to its
/// `\r\n\r\n` terminator, so no byte of the carried traffic is consumed. Everything happens
/// within `deadline`; any status other than `200` is an error.
pub async fn dial_tunnel(
    proxy: &ProxyTarget,
    host: &str,
    port: u16,
    deadline: Duration,
) -> io::Result<TcpStream> {
    let authority = if host.contains(':') && !host.starts_with('[') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    };
    let mut head = format!("CONNECT {authority} HTTP/1.1\r\nHost: {authority}\r\n");
    if let Some((user, pass)) = &proxy.user {
        head.push_str(&format!(
            "Proxy-Authorization: Basic {}\r\n",
            basic(user, pass)
        ));
    }
    head.push_str("\r\n");
    let opened = tokio::time::timeout(deadline, async {
        let mut sock = TcpStream::connect((proxy.host.as_str(), proxy.port)).await?;
        sock.set_nodelay(true)?;
        sock.write_all(head.as_bytes()).await?;
        let mut read: Vec<u8> = Vec::new();
        let mut byte = [0u8; 1];
        while !read.ends_with(b"\r\n\r\n") {
            if read.len() >= CONNECT_HEAD_MAX {
                return Err(io::Error::other("the proxy's CONNECT answer has no end"));
            }
            sock.read_exact(&mut byte).await?;
            read.push(byte[0]);
        }
        let answer = String::from_utf8_lossy(&read).into_owned();
        let status = answer
            .split_whitespace()
            .nth(1)
            .and_then(|code| code.parse::<u16>().ok())
            .ok_or_else(|| io::Error::other("the proxy's CONNECT answer has no status"))?;
        if status != 200 {
            return Err(io::Error::other(format!(
                "proxy CONNECT {authority} answered {status}"
            )));
        }
        Ok(sock)
    })
    .await;
    opened.map_err(|_| io::Error::new(ErrorKind::TimedOut, "the proxy never answered CONNECT"))?
}

/// The `Proxy-Authorization` credentials for `user:pass`: base64, standard alphabet with padding.
fn basic(user: &str, pass: &str) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let data = format!("{user}:{pass}").into_bytes();
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let bytes = [
            chunk[0],
            chunk
                .first()
                .and_then(|_| chunk.get(1))
                .copied()
                .unwrap_or(0),
            chunk
                .first()
                .and_then(|_| chunk.get(2))
                .copied()
                .unwrap_or(0),
        ];
        let n = (u32::from(bytes[0]) << 16) | (u32::from(bytes[1]) << 8) | u32::from(bytes[2]);
        out.push(ALPHABET[((n >> 18) & 63) as usize] as char);
        out.push(ALPHABET[((n >> 12) & 63) as usize] as char);
        out.push(if chunk.len() > 1 {
            ALPHABET[((n >> 6) & 63) as usize] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            ALPHABET[(n & 63) as usize] as char
        } else {
            '='
        });
    }
    out
}

/// The upstream client's settings.
pub struct UpstreamConfig {
    pub env: Env,
    pub roots: RootCertStore,
    pub idle: Duration,
}

/// The keep-alive upstream client: one pool per client, one socket per in-flight request.
pub struct UpstreamClient {
    env: Env,
    tls: Option<TlsConnector>,
    idle: Duration,
    idle_ttl: Duration,
    pool: Arc<Mutex<HashMap<String, HostPool>>>,
}

impl UpstreamClient {
    pub fn new(cfg: UpstreamConfig) -> UpstreamClient {
        UpstreamClient {
            env: cfg.env,
            tls: tls_connector(cfg.roots),
            idle: cfg.idle,
            idle_ttl: IDLE_CONN_TTL,
            pool: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    /// Sends one request upstream: one attempt, then one retry on a fresh, unpooled connection
    /// when the failure says a reused socket died before the whole body was handed over and the
    /// body still replays from byte 0.
    pub async fn send(&self, req: UpstreamRequest) -> Result<UpstreamAnswer, Failure> {
        let first = match self.attempt(&req, false).await {
            Ok(answer) => return Ok(answer),
            Err(first) => first,
        };
        // The client stopped short: the body will never be whole, so the failure is final.
        if let RequestBody::Streamed(body) = &req.body
            && body.client_failed()
        {
            return Err(Failure {
                code: FailureCode::ECanceled,
                client_closed: true,
                trace: first.trace,
            });
        }
        if retryable(&first) {
            return self.attempt(&req, true).await.map_err(Failure::from);
        }
        Err(Failure::from(first))
    }

    /// One attempt on one connection: pooled when an idle socket is usable, else a fresh dial. A
    /// fresh retry dials a socket the pool never receives. Every failure carries its errno-style
    /// code plus the retry verdict.
    async fn attempt(
        &self,
        req: &UpstreamRequest,
        fresh: bool,
    ) -> Result<UpstreamAnswer, AttemptFailure> {
        let proxy = proxy_for(&req.url, &self.env);
        let Some(key) = pool_key(&req.url, proxy.as_ref()) else {
            return Err(dial_failure(FailureCode::EUpstream));
        };
        if !fresh && let Some(conn) = self.checkout(&key) {
            return self.drive(conn.live, true, true, &key, req).await;
        }
        let live = self.fresh(&key, &req.url, proxy.as_ref()).await?;
        self.drive(live, false, !fresh, &key, req).await
    }

    /// Pops the most recently returned idle connection that is within its idle time and ready to
    /// carry a request; a socket the upstream closed reads as not ready and is discarded here,
    /// without a byte sent.
    fn checkout(&self, key: &str) -> Option<IdleConn> {
        let mut pools = lock_pools(&self.pool);
        let host = pools.get_mut(key)?;
        while let Some(conn) = host.idle.pop() {
            if conn.returned_at.elapsed() <= self.idle_ttl && conn.live.sender.is_ready() {
                return Some(conn);
            }
        }
        None
    }

    /// The client's silence budget: the answer side applies the same budget after the head,
    /// reset on every chunk.
    pub fn idle(&self) -> Duration {
        self.idle
    }

    /// The idle sockets a request to `url` would ride, dropping the ones that read as expired or
    /// not ready, so a count of one is a socket the next send reuses and a count of zero is a
    /// dial.
    pub fn idle_sockets(&self, url: &Uri) -> usize {
        let Some(key) = pool_key(url, proxy_for(url, &self.env).as_ref()) else {
            return 0;
        };
        let mut pools = lock_pools(&self.pool);
        let Some(host) = pools.get_mut(&key) else {
            return 0;
        };
        host.idle.retain(|conn| {
            conn.returned_at.elapsed() <= self.idle_ttl && conn.live.sender.is_ready()
        });
        host.idle.len()
    }

    /// Dials a new connection, holding a pool permit for the socket's whole life.
    async fn fresh(
        &self,
        key: &str,
        url: &Uri,
        proxy: Option<&ProxyTarget>,
    ) -> Result<Live, AttemptFailure> {
        let permits = {
            let mut pools = lock_pools(&self.pool);
            pools
                .entry(key.to_owned())
                .or_insert_with(HostPool::new)
                .permits
                .clone()
        };
        let permit = permits
            .acquire_owned()
            .await
            .map_err(|_| dial_failure(FailureCode::EUpstream))?;
        let Some((host, port, is_tls)) = target_parts(url) else {
            return Err(dial_failure(FailureCode::EUpstream));
        };
        let (io, dial) = self
            .connect(proxy, host, port, is_tls)
            .await
            .map_err(|failed| AttemptFailure {
                code: classify_io(&failed.error),
                trace: failed.trace,
                ..dial_failure(FailureCode::EUpstream)
            })?;
        let (sender, conn) = handshake(TokioIo::new(io))
            .await
            .map_err(|err| AttemptFailure {
                trace: dial.trace(false, Phase::Send),
                ..dial_failure(classify(&err))
            })?;
        tokio::spawn(async move {
            let _ = conn.with_upgrades().await;
        });
        Ok(Live {
            sender,
            permit,
            dial,
        })
    }

    /// Resolves the host, dials each address in turn and, for https, completes the TLS
    /// handshake, timing each phase.
    async fn connect(
        &self,
        proxy: Option<&ProxyTarget>,
        host: &str,
        port: u16,
        is_tls: bool,
    ) -> Result<(Box<dyn UpstreamIo>, Dial), DialError> {
        let mut dial = Dial::default();
        let mut trace = Trace::default().at(Phase::Connect);
        let tcp = match proxy {
            Some(proxy) => {
                let started = Instant::now();
                let tcp = dial_tunnel(proxy, host, port, self.idle)
                    .await
                    .map_err(|error| DialError { error, trace })?;
                dial.connect_ms = Some(elapsed_ms(started));
                tcp
            }
            None => {
                let (tcp, reached) = self.dial_tcp(host, port).await?;
                dial = reached;
                tcp
            }
        };
        trace = dial.trace(false, Phase::Tls);
        if !is_tls {
            return Ok((Box::new(tcp), dial));
        }
        let Some(tls) = &self.tls else {
            return Err(DialError {
                error: io::Error::other("the TLS client config is unavailable"),
                trace,
            });
        };
        let name = ServerName::try_from(host.to_owned()).map_err(|_| DialError {
            error: io::Error::new(
                ErrorKind::InvalidInput,
                "the upstream host is not a TLS name",
            ),
            trace,
        })?;
        let started = Instant::now();
        let stream = tls
            .connect(name, tcp)
            .await
            .map_err(|error| DialError { error, trace })?;
        dial.tls_ms = Some(elapsed_ms(started));
        Ok((Box::new(stream), dial))
    }

    /// DNS, then one TCP dial per resolved address until one connects; the peer of a failure is
    /// the last address tried.
    async fn dial_tcp(&self, host: &str, port: u16) -> Result<(TcpStream, Dial), DialError> {
        let mut dial = Dial::default();
        let started = Instant::now();
        let addrs: Vec<SocketAddr> = lookup_host((host, port))
            .await
            .map_err(|error| DialError {
                error,
                trace: Trace::default().at(Phase::Dns),
            })?
            .collect();
        dial.dns_ms = Some(elapsed_ms(started));
        let mut last = DialError {
            error: io::Error::new(ErrorKind::NotFound, "the host has no address"),
            trace: dial.trace(false, Phase::Dns),
        };
        let started = Instant::now();
        for addr in addrs {
            dial.peer = Some(addr);
            let failed_at = dial.trace(false, Phase::Connect);
            match TcpStream::connect(addr)
                .await
                .and_then(|tcp| tcp.set_nodelay(true).map(|()| tcp))
            {
                Ok(tcp) => {
                    dial.connect_ms = Some(elapsed_ms(started));
                    return Ok((tcp, dial));
                }
                Err(error) => {
                    last = DialError {
                        error,
                        trace: failed_at,
                    }
                }
            }
        }
        Err(last)
    }

    /// Sends one request on one connection and hands back the answer with its connection guard.
    /// A failure reports whether the body's last byte was handed over and whether the socket went
    /// away, so `send` can weigh a retry.
    async fn drive(
        &self,
        live: Live,
        reused: bool,
        poolable: bool,
        key: &str,
        req: &UpstreamRequest,
    ) -> Result<UpstreamAnswer, AttemptFailure> {
        let Live {
            sender,
            permit,
            dial,
        } = live;
        let streamed = matches!(req.body, RequestBody::Streamed(_));
        let total = match &req.body {
            RequestBody::Whole(bytes) => bytes.len() as u64,
            RequestBody::Streamed(stream) => stream.len(),
        };
        let origin = origin_form(&req.url).map_err(dial_failure)?;
        let handed = Arc::new(Handed::default());
        let (frames, rx) = mpsc::channel(1);
        let wire = AttemptBody {
            rx,
            sent: 0,
            total,
            handed: Arc::clone(&handed),
        };
        if total == 0 {
            handed.mark();
        }
        let mut builder = Request::builder().method(req.method.clone()).uri(origin);
        let wire_request = match builder.headers_mut() {
            Some(slot) => {
                *slot = req.headers.clone();
                builder
                    .body(wire)
                    .map_err(|_| dial_failure(FailureCode::EUpstream))?
            }
            // A builder that already failed on the method or the uri has no header slot.
            None => return Err(dial_failure(FailureCode::EUpstream)),
        };
        // The reader exists before the task does: an early answer cannot abandon the body
        // before the pump holds it.
        let source = pump_source(&req.body);
        let pumping = tokio::spawn(pump(source, frames));
        let mut sender = sender;
        let exchange = async {
            if streamed {
                // The body fills while the request goes out: take an early head if one arrives, else
                // wait for the last byte, then start the idle budget; the head's arrival ends it.
                let mut head = std::pin::pin!(sender.send_request(wire_request));
                let first = tokio::select! {
                    answer = &mut head => Some(answer),
                    _ = handed.wait() => None,
                };
                match first {
                    Some(answer) => {
                        answer.map_err(|err| hyper_failure(&err, &dial, reused, &handed))
                    }
                    None => match tokio::time::timeout(self.idle, head.as_mut()).await {
                        Ok(Ok(answer)) => Ok(answer),
                        Ok(Err(err)) => Err(hyper_failure(&err, &dial, reused, &handed)),
                        Err(_) => Err(timeout_failure(&dial, reused, &handed)),
                    },
                }
            } else {
                match tokio::time::timeout(self.idle, sender.send_request(wire_request)).await {
                    Ok(Ok(answer)) => Ok(answer),
                    Ok(Err(err)) => Err(hyper_failure(&err, &dial, reused, &handed)),
                    Err(_) => Err(timeout_failure(&dial, reused, &handed)),
                }
            }
        };
        let mut answer = match exchange.await {
            Ok(answer) => answer,
            Err(mut failure) => {
                // A pump left reading would take the first chunk past the prefix when it
                // arrives and drop the prefix under a retry: it ends before the body is weighed.
                pumping.abort();
                let _ = pumping.await;
                failure.replayable = replayable(&req.body);
                return Err(failure);
            }
        };
        // A `101` answer hands the connection over: the tunnel is taken before the answer is
        // split, so the relay that follows owns both ends of it.
        let upgrade = if answer.status() == StatusCode::SWITCHING_PROTOCOLS {
            Some(hyper::upgrade::on(&mut answer))
        } else {
            None
        };
        let (parts, body) = answer.into_parts();
        Ok(UpstreamAnswer {
            status: parts.status,
            headers: parts.headers,
            body,
            upgrade,
            trace: dial.trace(reused, Phase::Headers),
            conn: ConnGuard {
                dial,
                sender: Some(sender),
                pool: Arc::clone(&self.pool),
                idle_ttl: self.idle_ttl,
                key: key.to_owned(),
                permit: Some(permit),
                reused,
                poolable,
            },
        })
    }
}

/// The client's side of an answer: the pooled connection waits in here until `finish` returns it.
pub struct ConnGuard {
    dial: Dial,
    sender: Option<SendRequest<AttemptBody>>,
    pool: Arc<Mutex<HashMap<String, HostPool>>>,
    idle_ttl: Duration,
    key: String,
    permit: Option<OwnedSemaphorePermit>,
    /// Whether the request ran on a socket checked out of the pool.
    pub reused: bool,
    /// Whether `finish` returns the socket to the pool; a fresh retry socket never pools.
    poolable: bool,
}

impl ConnGuard {
    /// Returns the connection to the pool; call only after the body reached its end. A socket
    /// the upstream closed, and a fresh retry socket, never pool: their sender and permit drop,
    /// and their socket closes. A socket hyper has not marked ready yet still pools; the pool's
    /// readers discard it when it reads as not ready.
    pub fn finish(self) {
        let ConnGuard {
            dial,
            sender,
            permit,
            pool,
            idle_ttl,
            key,
            poolable,
            ..
        } = self;
        let (Some(sender), Some(permit)) = (sender, permit) else {
            return;
        };
        if !poolable {
            return;
        }
        let mut pools = lock_pools(&pool);
        push_idle(
            &mut pools,
            key,
            IdleConn {
                live: Live {
                    sender,
                    permit,
                    dial,
                },
                returned_at: Instant::now(),
            },
            idle_ttl,
        );
    }
}

/// What the pump reads: the whole bytes, or the reader of a streamed body.
enum PumpSource {
    Whole(Bytes),
    Streamed(BodyReader),
}

/// Takes the pump's source on the caller's task, so a streamed body is held before the pump runs.
fn pump_source(body: &RequestBody) -> PumpSource {
    match body {
        RequestBody::Whole(bytes) => PumpSource::Whole(bytes.clone()),
        RequestBody::Streamed(stream) => PumpSource::Streamed(stream.reader()),
    }
}

/// Feeds the wire body from the request body: a whole body in fixed slices without copying, a
/// streamed body in the order it fills. Channel capacity 1 keeps the pace the socket sets.
async fn pump(source: PumpSource, frames: mpsc::Sender<Result<Bytes, BodyDropped>>) {
    match source {
        PumpSource::Whole(bytes) => {
            let mut offset = 0;
            while offset < bytes.len() {
                let end = (offset + FRAME).min(bytes.len());
                if frames.send(Ok(bytes.slice(offset..end))).await.is_err() {
                    return;
                }
                offset = end;
            }
        }
        PumpSource::Streamed(mut reader) => loop {
            match reader.next_chunk().await {
                NextChunk::Chunk(chunk) => {
                    if frames.send(Ok(chunk)).await.is_err() {
                        return;
                    }
                }
                NextChunk::End => return,
                NextChunk::Failed | NextChunk::NotReplayable => {
                    let _ = frames.send(Err(BodyDropped)).await;
                    return;
                }
            }
        },
    }
}

fn tls_connector(roots: RootCertStore) -> Option<TlsConnector> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let builder = ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .ok()?;
    Some(TlsConnector::from(Arc::new(
        builder.with_root_certificates(roots).with_no_client_auth(),
    )))
}

/// The errno-style code for a send failure: whatever the error chain names, `EUPSTREAM` otherwise
/// (an incomplete message included).
fn classify(err: &HyperError) -> FailureCode {
    let mut source: Option<&dyn StdError> = Some(err);
    while let Some(current) = source {
        if let Some(code) = spot_code(current) {
            return code;
        }
        source = current.source();
    }
    FailureCode::EUpstream
}

fn spot_code(err: &(dyn StdError + 'static)) -> Option<FailureCode> {
    if let Some(io) = err.downcast_ref::<io::Error>()
        && let Some(code) = io_code(io)
    {
        return Some(code);
    }
    // A rustls error anywhere in the chain is a certificate failure: no errno applies.
    if err.downcast_ref::<rustls::Error>().is_some() {
        return Some(FailureCode::ECert);
    }
    None
}

fn io_code(err: &io::Error) -> Option<FailureCode> {
    match err.kind() {
        ErrorKind::ConnectionReset => return Some(FailureCode::EConnReset),
        ErrorKind::BrokenPipe => return Some(FailureCode::EPipe),
        ErrorKind::ConnectionRefused => return Some(FailureCode::EConnRefused),
        ErrorKind::TimedOut => return Some(FailureCode::ETimedOut),
        _ => {}
    }
    match err.raw_os_error() {
        Some(libc::ECONNRESET) => Some(FailureCode::EConnReset),
        Some(libc::EPIPE) => Some(FailureCode::EPipe),
        Some(libc::ECONNREFUSED) => Some(FailureCode::EConnRefused),
        // TLS handshake failures arrive as an io error wrapping the rustls error.
        _ => err
            .get_ref()
            .and_then(|inner| inner.downcast_ref::<rustls::Error>())
            .map(|_| FailureCode::ECert),
    }
}

/// A dial failure is classified the same way, straight from its io error.
fn classify_io(err: &io::Error) -> FailureCode {
    io_code(err).unwrap_or(FailureCode::EUpstream)
}

/// An attempt that ended before a byte of the body left: never handed over, never closed early.
/// Failures start non-replayable; `drive` is the only place that sets the flag.
fn dial_failure(code: FailureCode) -> AttemptFailure {
    AttemptFailure {
        code,
        trace: Trace::default(),
        reused: false,
        handed_over: false,
        closed_early: false,
        replayable: false,
    }
}

/// The attempt verdict for a transport error: the socket went away when hyper says the message
/// was incomplete or canceled, or the code is a reset or a broken pipe. A timeout, a refused
/// connection and a certificate failure are never a closed socket. the failure starts
/// non-replayable and `drive` sets `replayable` once the attempt's pump has ended.
fn hyper_failure(err: &HyperError, dial: &Dial, reused: bool, handed: &Handed) -> AttemptFailure {
    let code = classify(err);
    AttemptFailure {
        code,
        trace: dial.trace(reused, silent_phase(handed)),
        reused,
        handed_over: handed.is_marked(),
        closed_early: err.is_incomplete_message()
            || err.is_canceled()
            || code == FailureCode::EConnReset
            || code == FailureCode::EPipe,
        replayable: false,
    }
}

/// Silence is never a closed socket: no retry rides on a timeout. the failure starts
/// non-replayable and `drive` sets `replayable` once the attempt's pump has ended.
fn timeout_failure(dial: &Dial, reused: bool, handed: &Handed) -> AttemptFailure {
    AttemptFailure {
        code: FailureCode::ETimedOut,
        trace: dial.trace(reused, silent_phase(handed)),
        reused,
        handed_over: handed.is_marked(),
        closed_early: false,
        replayable: false,
    }
}

/// The phase an attempt that went wrong after the dial was in: still sending until the body's
/// last byte left, waiting for the head after.
fn silent_phase(handed: &Handed) -> Phase {
    if handed.is_marked() {
        Phase::Headers
    } else {
        Phase::Send
    }
}

/// Whether a new attempt can read the body from byte 0: a whole body always can.
fn replayable(body: &RequestBody) -> bool {
    match body {
        RequestBody::Whole(_) => true,
        RequestBody::Streamed(stream) => stream.replayable(),
    }
}

fn elapsed_ms(since: Instant) -> u64 {
    since.elapsed().as_millis() as u64
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn l1_take_whole_moves_the_chunks_out() {
        let stream = StreamedBody::new(9);
        for part in ["abc", "def", "ghi"] {
            stream.push(Bytes::from(part)).await;
        }
        assert_eq!(stream.take_whole(), Some(Bytes::from("abcdefghi")));
        assert_eq!(stream.retained_bytes(), 0);
    }

    #[tokio::test]
    async fn l1_one_chunk_take_whole_is_not_copied() {
        let stream = StreamedBody::new(4);
        let chunk = Bytes::from("body");
        let pointer = chunk.as_ptr();
        stream.push(chunk).await;
        let whole = stream.take_whole();
        assert_eq!(whole.as_ref().map(|bytes| bytes.as_ptr()), Some(pointer));
    }

    #[tokio::test]
    async fn l1_take_whole_of_an_unfinished_body_keeps_it() {
        let stream = StreamedBody::new(6);
        stream.push(Bytes::from("abc")).await;
        assert_eq!(stream.take_whole(), None);
        assert_eq!(stream.retained_bytes(), 3);
    }

    const KIB64: usize = 64 * 1024;

    fn chunk() -> Bytes {
        Bytes::from(vec![b'x'; KIB64])
    }

    /// One poll with a waker that does nothing: the test re-polls by hand.
    fn poll_once<F: Future>(future: Pin<&mut F>) -> Poll<F::Output> {
        future.poll(&mut Context::from_waker(std::task::Waker::noop()))
    }

    #[tokio::test]
    async fn l41_the_pump_source_holds_the_body_before_the_pump_runs() {
        let stream = StreamedBody::with_limits(8 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        let routing = stream.route_hold();
        let source = pump_source(&RequestBody::Streamed(Arc::clone(&stream)));
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        drop(routing);
        assert!(
            stream.retained_bytes() > 0,
            "a live reader still reads the body"
        );
        drop(source);
        assert_eq!(stream.retained_bytes(), 0);
    }

    #[test]
    fn l41_a_whole_body_source_takes_no_hold() {
        let bytes = Bytes::from_static(b"{}");
        let pointer = bytes.as_ptr();
        match pump_source(&RequestBody::Whole(bytes)) {
            PumpSource::Whole(same) => assert_eq!(same.as_ptr(), pointer),
            PumpSource::Streamed(_) => panic!("a whole body has no reader"),
        }
    }

    #[test]
    fn l41_a_dial_failure_is_not_replayable_until_drive_says_so() {
        assert!(!dial_failure(FailureCode::EUpstream).replayable);
    }

    #[tokio::test]
    async fn l3_push_waits_while_the_window_is_full() {
        let stream = StreamedBody::with_limits(4 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        let _routing = stream.hold();
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        let mut third = std::pin::pin!(stream.push(chunk()));
        assert!(poll_once(third.as_mut()).is_pending(), "the window is full");
        let mut reader = stream.reader();
        assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
        assert!(
            poll_once(third.as_mut()).is_pending(),
            "a chunk inside the prefix makes no room"
        );
        assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
        assert!(
            poll_once(third.as_mut()).is_ready(),
            "one chunk past the prefix was taken"
        );
    }

    #[tokio::test]
    async fn l3_a_dropped_reader_releases_a_waiting_push() {
        let stream = StreamedBody::with_limits(3 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        let routing = stream.hold();
        let reader = stream.reader();
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        let mut third = std::pin::pin!(stream.push(chunk()));
        assert!(poll_once(third.as_mut()).is_pending(), "the window is full");
        drop(routing);
        assert!(
            poll_once(third.as_mut()).is_pending(),
            "a live reader can still make room"
        );
        drop(reader);
        assert!(
            poll_once(third.as_mut()).is_ready(),
            "nobody can read the body any more"
        );
    }

    const MIB: u64 = 1024 * 1024;

    /// Pushes `chunks` 64 KiB chunks while `reader` takes whatever lets a waiting push go on,
    /// checking what the body holds after every step; returns how many chunks were read. A
    /// waiting push has already added its chunk, so the body holds one chunk more until it returns.
    async fn push_and_read(stream: &StreamedBody, reader: &mut BodyReader, chunks: usize) -> usize {
        let limit = stream.prefix + stream.window;
        let mut read = 0;
        for _ in 0..chunks {
            let mut push = std::pin::pin!(stream.push(chunk()));
            while poll_once(push.as_mut()).is_pending() {
                assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
                read += 1;
                let held = stream.retained_bytes();
                assert!(
                    held <= limit + KIB64 as u64,
                    "held {held} while a push waits"
                );
            }
            assert!(
                stream.retained_bytes() <= limit,
                "held {}",
                stream.retained_bytes()
            );
        }
        read
    }

    #[tokio::test]
    async fn l4_chunk_past_prefix_is_dropped() {
        let stream = StreamedBody::with_limits(3 * MIB, MIB, SEND_WINDOW);
        let mut reader = stream.reader();
        let mut read = push_and_read(&stream, &mut reader, 48).await;
        while let NextChunk::Chunk(_) = reader.next_chunk().await {
            read += 1;
            assert!(stream.retained_bytes() <= MIB + SEND_WINDOW);
        }
        assert_eq!(read, 48);
        assert!(
            stream.retained_bytes() <= MIB,
            "every chunk past the prefix is gone"
        );
    }

    #[tokio::test]
    async fn l4_body_turns_not_replayable_after_the_prefix() {
        let stream = StreamedBody::with_limits(3 * MIB, MIB, SEND_WINDOW);
        let mut reader = stream.reader();
        for _ in 0..20 {
            stream.push(chunk()).await;
        }
        for _ in 0..16 {
            assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
            assert!(stream.replayable(), "the prefix alone is kept");
        }
        assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
        assert!(!stream.replayable(), "a chunk past the prefix left");
        let mut again = stream.reader();
        assert!(matches!(again.next_chunk().await, NextChunk::NotReplayable));
        assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
    }

    #[tokio::test]
    async fn l4_a_body_within_the_prefix_stays_replayable() {
        let stream = StreamedBody::with_limits(8 * KIB64 as u64, MIB, SEND_WINDOW);
        let _routing = stream.hold();
        for n in 0..8u8 {
            stream.push(Bytes::from(vec![n; KIB64])).await;
        }
        let mut reads = Vec::new();
        for _ in 0..2 {
            let mut reader = stream.reader();
            let mut body = Vec::new();
            while let NextChunk::Chunk(chunk) = reader.next_chunk().await {
                body.extend_from_slice(&chunk);
            }
            reads.push(body);
        }
        assert_eq!(reads[0].len(), 8 * KIB64);
        assert_eq!(reads[0], reads[1]);
        assert!(stream.replayable());
    }

    #[tokio::test]
    async fn l5_fail_releases_a_waiting_push() {
        let stream = StreamedBody::with_limits(3 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        let _routing = stream.hold();
        let _reader = stream.reader();
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        let mut third = std::pin::pin!(stream.push(chunk()));
        assert!(poll_once(third.as_mut()).is_pending(), "the window is full");
        stream.fail();
        assert!(
            poll_once(third.as_mut()).is_ready(),
            "a failed body never waits"
        );
    }

    #[tokio::test]
    async fn l5_retry_from_zero_releases_only_past_the_furthest_byte() {
        // The prefix ends inside chunk 1, so a reader that took chunks 0 and 1 is past it and the
        // body still replays from byte 0.
        let stream =
            StreamedBody::with_limits(4 * KIB64 as u64, 3 * KIB64 as u64 / 2, KIB64 as u64);
        let _routing = stream.hold();
        let mut first = stream.reader();
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        assert!(matches!(first.next_chunk().await, NextChunk::Chunk(_)));
        assert!(matches!(first.next_chunk().await, NextChunk::Chunk(_)));
        stream.push(chunk()).await;
        let mut fourth = std::pin::pin!(stream.push(chunk()));
        assert!(
            poll_once(fourth.as_mut()).is_pending(),
            "the window is full"
        );
        drop(first);
        assert!(stream.replayable());
        let mut retry = stream.reader();
        for taken in 1..=2 {
            assert!(matches!(retry.next_chunk().await, NextChunk::Chunk(_)));
            assert!(
                poll_once(fourth.as_mut()).is_pending(),
                "chunk {taken} of the retry is not past the furthest byte"
            );
        }
        assert!(matches!(retry.next_chunk().await, NextChunk::Chunk(_)));
        assert!(
            poll_once(fourth.as_mut()).is_ready(),
            "the retry passed the furthest byte"
        );
    }

    #[tokio::test]
    async fn l36_an_abandoned_body_keeps_nothing() {
        let stream = StreamedBody::new(20 * MIB);
        for _ in 0..16 {
            stream.push(chunk()).await;
        }
        assert_eq!(stream.retained_bytes(), MIB);
        stream.abandon();
        assert_eq!(stream.retained_bytes(), 0, "the abandon frees what is held");
        for n in 0..19 * 16 {
            let mut push = std::pin::pin!(stream.push(chunk()));
            assert!(poll_once(push.as_mut()).is_ready(), "push {n} waits");
            assert_eq!(stream.retained_bytes(), 0, "push {n} kept its chunk");
        }
        assert!(stream.is_whole(), "the filler still sees the body end");
        assert_eq!(stream.take_whole(), None);
        assert!(!stream.replayable());
        let mut late = stream.reader();
        assert!(matches!(late.next_chunk().await, NextChunk::NotReplayable));
    }

    #[tokio::test]
    async fn l36_abandon_releases_a_waiting_push() {
        let stream = StreamedBody::with_limits(4 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        let _routing = stream.hold();
        let mut reader = stream.reader();
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        let mut third = std::pin::pin!(stream.push(chunk()));
        assert!(poll_once(third.as_mut()).is_pending(), "the window is full");
        stream.abandon();
        assert!(
            poll_once(third.as_mut()).is_ready(),
            "an abandoned body never waits"
        );
        assert_eq!(stream.retained_bytes(), 0);
        assert!(matches!(
            reader.next_chunk().await,
            NextChunk::NotReplayable
        ));
    }

    #[tokio::test]
    async fn l36_the_route_settling_abandons_once_the_last_reader_ends() {
        let stream = StreamedBody::with_limits(8 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        let routing = stream.route_hold();
        let mut reader = stream.reader();
        stream.push(chunk()).await;
        stream.push(chunk()).await;
        drop(routing);
        assert!(
            stream.retained_bytes() > 0,
            "a live reader still reads the body"
        );
        assert!(matches!(reader.next_chunk().await, NextChunk::Chunk(_)));
        drop(reader);
        assert_eq!(stream.retained_bytes(), 0, "nobody reads the body again");
        stream.push(chunk()).await;
        assert_eq!(stream.retained_bytes(), 0);
    }

    #[tokio::test]
    async fn l36_a_body_without_a_route_hold_is_kept_whole() {
        let stream = StreamedBody::with_limits(4 * KIB64 as u64, KIB64 as u64, KIB64 as u64);
        drop(stream.reader());
        for _ in 0..4 {
            stream.push(chunk()).await;
        }
        assert_eq!(
            stream.take_whole().map(|whole| whole.len()),
            Some(4 * KIB64)
        );
    }
}

#[cfg(test)]
mod pool_tests {
    use super::*;
    use huddle_config::env::Env;
    use huddle_testkit::fake_upstream::{FakeUpstream, Step};

    fn client(idle_ttl: Duration) -> UpstreamClient {
        let mut client = UpstreamClient::new(UpstreamConfig {
            env: Env::with(&[]),
            roots: RootCertStore::empty(),
            idle: IDLE_TIMEOUT,
        });
        client.idle_ttl = idle_ttl;
        client
    }

    async fn answer(client: &UpstreamClient, url: &str) -> UpstreamAnswer {
        let uri: Uri = url.parse().expect("uri");
        let request = UpstreamRequest {
            method: Method::POST,
            headers: forward_headers(&HeaderMap::new(), &uri, 2),
            url: uri,
            body: RequestBody::Whole(Bytes::from_static(b"{}")),
        };
        client.send(request).await.expect("send")
    }

    async fn drain(answer: UpstreamAnswer) -> ConnGuard {
        use http_body_util::BodyExt;
        let UpstreamAnswer { conn, body, .. } = answer;
        let _ = body.collect().await.expect("body");
        conn
    }

    fn pooled(client: &UpstreamClient) -> usize {
        idle_total(&lock_pools(&client.pool))
    }

    #[tokio::test]
    async fn l10_an_expired_idle_socket_is_dropped_when_another_returns() {
        let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
        let client = client(Duration::from_millis(50));
        let url = format!("{}/v1/messages", upstream.url());
        let first = drain(answer(&client, &url).await).await;
        let second = drain(answer(&client, &url).await).await;
        first.finish();
        assert_eq!(pooled(&client), 1);
        tokio::time::sleep(Duration::from_millis(120)).await;
        let mark = std::time::Instant::now();
        second.finish();
        assert_eq!(pooled(&client), 1, "the expired socket left the pool");
        assert!(
            lock_pools(&client.pool)
                .values()
                .flat_map(|host| host.idle.iter())
                .all(|idle| idle.returned_at >= mark),
            "the socket that stays is the later one"
        );
    }

    #[tokio::test]
    async fn l41_a_return_sweeps_every_host() {
        let quiet = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
        let busy = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
        let client = client(Duration::from_millis(50));
        let quiet_conn =
            drain(answer(&client, &format!("{}/v1/messages", quiet.url())).await).await;
        let busy_conn = drain(answer(&client, &format!("{}/v1/messages", busy.url())).await).await;
        quiet_conn.finish();
        tokio::time::sleep(Duration::from_millis(120)).await;
        busy_conn.finish();
        assert_eq!(
            pooled(&client),
            1,
            "the quiet host's expired socket left the pool"
        );
    }

    #[tokio::test]
    async fn l10_a_host_keeps_at_most_16_idle_sockets() {
        let upstream = FakeUpstream::start(|_, _| Step::answer(200, "ok")).await;
        let client = client(IDLE_CONN_TTL);
        let url = format!("{}/v1/messages", upstream.url());
        let mut held = Vec::new();
        for _ in 0..40 {
            held.push(drain(answer(&client, &url).await).await);
        }
        let mut mark = None;
        for (index, conn) in held.into_iter().enumerate() {
            if index == 24 {
                mark = Some(std::time::Instant::now());
            }
            conn.finish();
            tokio::time::sleep(Duration::from_millis(2)).await;
        }
        let mark = mark.expect("mark");
        let pools = lock_pools(&client.pool);
        assert_eq!(idle_total(&pools), 16);
        assert!(
            pools
                .values()
                .flat_map(|host| host.idle.iter())
                .all(|idle| idle.returned_at >= mark),
            "the freshest 16 stay"
        );
    }
}
