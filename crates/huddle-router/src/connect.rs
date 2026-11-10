//! huddle-router: connect.
//!
//! A CONNECT names a host:port in its authority. One is refused when it names this machine or is
//! not a host:port at all. `api.anthropic.com:443` is answered with
//! `HTTP/1.1 200 Connection established` and decrypted with the local CA leaf, and the requests
//! inside are served over HTTP/1.1 by the interceptor. Every other target is dialled first,
//! answered with the 200, and piped blind both ways — the bytes are never read here, so a
//! client's TLS inside the tunnel is with its target. While the CA cannot load, the Anthropic
//! target is a blind tunnel too, so the client keeps working. Every tunnel is counted while it
//! is open, so a drain can wait for them or close them.

use std::convert::Infallible;
use std::io;
use std::net::IpAddr;
use std::pin::Pin;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use hyper::Uri;
use hyper::ext::ReasonPhrase;
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper::upgrade::Upgraded;
use hyper::{Request, Response, StatusCode};
use hyper_util::rt::{TokioIo, TokioTimer};
use rustls::server::{ClientHello, ResolvesServerCert};
use rustls::sign::CertifiedKey;
use tokio::net::TcpStream;
use tokio::sync::watch;
use tokio_rustls::TlsAcceptor;

use huddle_config::env::Env;
use huddle_pki::ca::ROUTER_HOST;
use huddle_pki::leaf::LeafCache;

use crate::server::{Handler, RespBody, ServerLimits, anthropic_error, empty_body};

/// A direct dial to a name that resolves nowhere gives up after this long.
const LOCAL_DIAL_DEADLINE: Duration = Duration::from_secs(10);

/// A blind tunnel through a proxy gives the proxy this long to answer CONNECT.
const PROXY_DIAL_DEADLINE: Duration = Duration::from_secs(600);

pub type DialFuture = Pin<Box<dyn Future<Output = io::Result<TcpStream>> + Send>>;
pub type DialFn = Arc<dyn Fn(String) -> DialFuture + Send + Sync>;

/// Serves the requests inside a decrypted conversation.
#[derive(Clone)]
pub struct Decrypt {
    /// The local CA's cache: it hands out the leaf the handshake presents.
    pub leaf: Arc<LeafCache>,
    /// Serves each request inside the decrypted conversation: the model route or the Anthropic
    /// pass-through.
    pub intercept: Handler,
    /// The read limits the decrypted conversation serves under.
    pub limits: ServerLimits,
}

/// One CONNECT handler's collaborators.
pub struct ConnectCtx {
    /// Proxy environment the blind tunnel's dial honours.
    pub env: Env,
    /// Test seam: when set, it dials every blind tunnel instead of the proxy or direct path.
    pub dial: Option<DialFn>,
    /// Counts every CONNECT, for the update drain.
    pub tunnels: Arc<TunnelTracker>,
    /// Decrypts `api.anthropic.com:443` with the local CA when set; `None` tunnels every
    /// CONNECT blind.
    pub decrypt: Option<Arc<Decrypt>>,
}

/// Serves a CONNECT. Called only for CONNECT. A refusal or a failed dial answers before any 200,
/// so the client reads why its tunnel never started instead of a 200 its TLS then fails on.
pub fn connect_handler(ctx: Arc<ConnectCtx>) -> Handler {
    Arc::new(move |req: Request<hyper::body::Incoming>| {
        let ctx = ctx.clone();
        Box::pin(async move { connect(req, ctx).await })
    })
}

async fn connect(
    mut req: Request<hyper::body::Incoming>,
    ctx: Arc<ConnectCtx>,
) -> Response<RespBody> {
    let Some(authority) = req.uri().authority() else {
        return refused("a CONNECT names host:port");
    };
    // Authority::host keeps the brackets of an IPv6 literal; the checks below compare the bare
    // address.
    let host = authority
        .host()
        .strip_prefix('[')
        .and_then(|rest| rest.strip_suffix(']'))
        .unwrap_or_else(|| authority.host());
    let Some(port) = authority.port_u16().filter(|&port| port != 0) else {
        return refused("a CONNECT names host:port");
    };
    if names_this_machine(host) {
        return refused("a CONNECT may not name this machine");
    }
    let response = established();
    let decrypt = decrypted(host, port, ctx.decrypt.as_ref());
    if let Some(decrypt) = decrypt {
        // A decrypted conversation has no target to dial: the router is the target.
        let upgraded = hyper::upgrade::on(&mut req);
        // Counted before the upgrade: a drain that starts once the server stops accepting finds
        // it.
        let guard = ctx.tunnels.track();
        let phase_rx = ctx.tunnels.phase_signal();
        tokio::spawn(async move {
            let _guard = guard;
            let Ok(upgraded) = upgraded.await else {
                return;
            };
            serve_decrypted(upgraded, decrypt, phase_rx).await;
        });
    } else {
        let mut target = match dial(host, port, &ctx).await {
            Ok(target) => target,
            Err(DialFailure::Local) => return refused("a CONNECT may not name this machine"),
            Err(DialFailure::Other) => return dial_failed(),
        };
        let upgraded = hyper::upgrade::on(&mut req);
        // Counted before the upgrade: a drain that starts once the server stops accepting finds
        // it.
        let guard = ctx.tunnels.track();
        let mut phase_rx = ctx.tunnels.phase_signal();
        tokio::spawn(async move {
            let _guard = guard;
            let Ok(upgraded) = upgraded.await else {
                return;
            };
            let mut client = TokioIo::new(upgraded);
            tokio::select! {
                _ = tokio::io::copy_bidirectional(&mut client, &mut target) => {}
                _ = phase_rx.wait_for(|phase| *phase == Phase::Closing) => {}
            }
        });
    }
    response
}

/// `HTTP/1.1 200 Connection established`, the answer every opened tunnel starts with.
fn established() -> Response<RespBody> {
    let mut response = Response::new(empty_body());
    *response.status_mut() = StatusCode::OK;
    response
        .extensions_mut()
        .insert(ReasonPhrase::from_static(b"Connection established"));
    response
}

/// The decrypted conversation for the API host on its real port, when the CA holds a leaf.
/// Every other target stays a blind tunnel, and so does the API host while the CA cannot load —
/// the cache already logged that failure once.
fn decrypted(host: &str, port: u16, decrypt: Option<&Arc<Decrypt>>) -> Option<Arc<Decrypt>> {
    let decrypt = decrypt?;
    if port != 443 || !host.eq_ignore_ascii_case(ROUTER_HOST) {
        return None;
    }
    decrypt.leaf.get(SystemTime::now()).ok()?;
    Some(decrypt.clone())
}

/// TLS-accepts the upgraded tunnel with the local CA's leaf and serves the requests inside over
/// HTTP/1.1. A handshake that fails ends the conversation. On a drain the answer in flight
/// finishes and an idle conversation closes at once; on a close the conversation is dropped.
async fn serve_decrypted(
    upgraded: Upgraded,
    decrypt: Arc<Decrypt>,
    mut phase_rx: watch::Receiver<Phase>,
) {
    let config = match decrypted_config(decrypt.leaf.clone()) {
        Ok(config) => config,
        Err(_) => return,
    };
    let tls = match TlsAcceptor::from(Arc::new(config))
        .accept(TokioIo::new(upgraded))
        .await
    {
        Ok(tls) => tls,
        // The client walked away mid-handshake or sent no TLS: end the conversation.
        Err(_) => return,
    };
    let intercept = decrypt.intercept.clone();
    let mut conn = http1::Builder::new()
        .timer(TokioTimer::new())
        .header_read_timeout(decrypt.limits.header_read)
        .keep_alive(true)
        .serve_connection(
            TokioIo::new(tls),
            service_fn(move |req| {
                let intercept = intercept.clone();
                async move { Ok::<_, Infallible>(intercept(req).await) }
            }),
        )
        .with_upgrades();
    // The phase comes out owned: the watch borrow must not reach into the await below, its
    // guard is not Send.
    let stop = tokio::select! {
        _ = &mut conn => None,
        phase = phase_rx.wait_for(|phase| *phase == Phase::Draining || *phase == Phase::Closing) => {
            phase.ok().map(|phase| *phase)
        }
    };
    // A drain lets the answer in flight finish; an idle conversation closes at once. A close
    // drops the conversation now.
    if stop == Some(Phase::Draining) {
        Pin::new(&mut conn).graceful_shutdown();
        let _ = (&mut conn).await;
    }
}

/// The TLS server config of a decrypted conversation: TLS 1.2 and 1.3, no client certificates,
/// the local CA's leaf, HTTP/1.1 over ALPN.
fn decrypted_config(leaf: Arc<LeafCache>) -> Result<rustls::ServerConfig, rustls::Error> {
    let mut config = rustls::ServerConfig::builder_with_provider(Arc::new(
        rustls::crypto::ring::default_provider(),
    ))
    .with_safe_default_protocol_versions()?
    .with_no_client_auth()
    .with_cert_resolver(Arc::new(LeafResolver { leaf }));
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(config)
}

/// Presents the local CA's current leaf for every handshake: the cache re-signs the leaf only
/// once little of its life is left.
struct LeafResolver {
    leaf: Arc<LeafCache>,
}

impl std::fmt::Debug for LeafResolver {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("LeafResolver").finish_non_exhaustive()
    }
}

impl ResolvesServerCert for LeafResolver {
    fn resolve(&self, _hello: ClientHello<'_>) -> Option<Arc<CertifiedKey>> {
        self.leaf
            .get(SystemTime::now())
            .ok()
            .map(|leaf| leaf.certified.clone())
    }
}

/// True for an address on this machine or its link: loopback, unspecified or link-local. An
/// IPv4-mapped IPv6 address is judged as the IPv4 address it carries.
fn is_local_ip(ip: IpAddr) -> bool {
    let ip = match ip {
        IpAddr::V6(v6) => v6.to_ipv4_mapped().map_or(IpAddr::V6(v6), IpAddr::V4),
        v4 => v4,
    };
    match ip {
        IpAddr::V4(v4) => v4.is_loopback() || v4.is_unspecified() || v4.octets()[..2] == [169, 254],
        IpAddr::V6(v6) => {
            let octets = v6.octets();
            v6.is_loopback()
                || v6.is_unspecified()
                || (octets[0] == 0xfe && octets[1] & 0xc0 == 0x80)
        }
    }
}

/// True for a host that names this machine: a local address, or the `localhost` names.
fn names_this_machine(host: &str) -> bool {
    if let Ok(ip) = host.parse::<IpAddr>() {
        return is_local_ip(ip);
    }
    let lower = host.to_ascii_lowercase();
    lower == "localhost" || lower.ends_with(".localhost")
}

/// Why a target could not be opened: it names this machine, or it is merely unreachable.
enum DialFailure {
    Local,
    Other,
}

/// Opens the blind tunnel's target: the dial seam when one is set, else the user's own proxy when
/// the environment applies one to the target, else a direct dial. A name can resolve to this
/// machine as well as a literal can name it, so the local check runs again on every resolved
/// address.
async fn dial(host: &str, port: u16, ctx: &ConnectCtx) -> Result<TcpStream, DialFailure> {
    if let Some(dial) = &ctx.dial {
        return dial(format!("{host}:{port}"))
            .await
            .map_err(|_| DialFailure::Other);
    }
    let proxy =
        https_uri(host, port).and_then(|target| crate::upstream::proxy_for(&target, &ctx.env));
    if let Some(proxy) = proxy {
        return crate::upstream::dial_tunnel(&proxy, host, port, PROXY_DIAL_DEADLINE)
            .await
            .map_err(|_| DialFailure::Other);
    }
    match tokio::time::timeout(LOCAL_DIAL_DEADLINE, lookup_and_connect(host, port)).await {
        Ok(opened) => opened,
        Err(_) => Err(DialFailure::Other),
    }
}

/// Resolves `host:port` and connects to its first non-local address. Every address local answers
/// Local; no success at all answers Other.
async fn lookup_and_connect(host: &str, port: u16) -> Result<TcpStream, DialFailure> {
    let addresses = tokio::net::lookup_host((host, port))
        .await
        .map_err(|_| DialFailure::Other)?;
    let mut all_local = true;
    let mut non_local = Vec::new();
    for address in addresses {
        if is_local_ip(address.ip()) {
            continue;
        }
        all_local = false;
        non_local.push(address);
    }
    if all_local {
        return Err(DialFailure::Local);
    }
    for address in non_local {
        if let Ok(stream) = TcpStream::connect(address).await {
            return Ok(stream);
        }
    }
    Err(DialFailure::Other)
}

/// The proxy lookup's target URI: `https://{host}:{port}/`, IPv6 hosts re-bracketed. A host that
/// cannot form a URI is no proxy target.
fn https_uri(host: &str, port: u16) -> Option<Uri> {
    let host = if host.contains(':') && !host.starts_with('[') {
        format!("[{host}]")
    } else {
        host.to_owned()
    };
    format!("https://{host}:{port}/").parse().ok()
}

/// A CONNECT that must not be opened, answered with the Anthropic error shape.
fn refused(why: &str) -> Response<RespBody> {
    anthropic_error(
        StatusCode::FORBIDDEN,
        "permission_error",
        &format!("huddle-router router: CONNECT refused ({why})"),
    )
}

/// A CONNECT whose target could not be opened, answered with the Anthropic error shape.
fn dial_failed() -> Response<RespBody> {
    anthropic_error(
        StatusCode::BAD_GATEWAY,
        "api_error",
        "huddle-router router: CONNECT failed (the target cannot be reached)",
    )
}

/// How a drain ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DrainOutcome {
    Finished,
    TimedOut,
}

/// What a tunnel task does while its tunnel is open.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Phase {
    Serving,
    Draining,
    Closing,
}

/// Counts every CONNECT while it is open, and carries the phase a tunnel task watches: Closing
/// closes every tunnel still open.
pub struct TunnelTracker {
    phase: watch::Sender<Phase>,
    count: watch::Sender<usize>,
}

impl TunnelTracker {
    pub fn new() -> Arc<TunnelTracker> {
        Arc::new(Self {
            phase: watch::Sender::new(Phase::Serving),
            count: watch::Sender::new(0),
        })
    }

    /// Counts one CONNECT from here until its guard drops.
    pub fn track(self: &Arc<Self>) -> TunnelGuard {
        self.count.send_modify(|count| *count += 1);
        TunnelGuard {
            tracker: self.clone(),
        }
    }

    /// Non-zero while a CONNECT is live.
    pub fn count(&self) -> usize {
        *self.count.borrow()
    }

    pub fn phase_signal(&self) -> watch::Receiver<Phase> {
        self.phase.subscribe()
    }

    /// Sets Draining, waits for the count to reach zero or `deadline`. On deadline sets Closing,
    /// which closes every tunnel still open, and answers TimedOut.
    pub async fn drain(&self, deadline: Duration) -> DrainOutcome {
        self.phase.send_replace(Phase::Draining);
        let mut count = self.count.subscribe();
        let finished = tokio::time::timeout(deadline, async {
            loop {
                if *count.borrow_and_update() == 0 {
                    return;
                }
                // A dropped sender leaves the count fixed, so there is nothing more to wait for.
                if count.changed().await.is_err() {
                    return;
                }
            }
        })
        .await
        .is_ok();
        if finished {
            DrainOutcome::Finished
        } else {
            self.phase.send_replace(Phase::Closing);
            DrainOutcome::TimedOut
        }
    }
}

/// One counted CONNECT: its drop takes the tunnel from the count.
pub struct TunnelGuard {
    tracker: Arc<TunnelTracker>,
}

impl Drop for TunnelGuard {
    fn drop(&mut self) {
        self.tracker.count.send_modify(|count| *count -= 1);
    }
}
