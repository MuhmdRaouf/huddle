//! A scripted HTTP and TLS upstream that records what it receives.

use std::io::{self, Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::os::fd::{AsRawFd, RawFd};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, PoisonError};
use std::thread;
use std::time::{Duration, Instant};

use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use rustls::{ServerConfig, ServerConnection, StreamOwned};

/// What the fake does with one request, chosen by the plan closure.
#[derive(Clone, Debug)]
pub enum Step {
    Answer {
        status: u16,
        headers: Vec<(String, String)>,
        body: Vec<u8>,
        close: bool,
    },
    Sse {
        chunks: Vec<String>,
        gap: Duration,
        cut: bool,
    },
    SseHeadThenSilence,
    JsonThenCut {
        status: u16,
        partial: String,
        declared_len: usize,
    },
    UpgradeEcho,
    /// Reads the whole body, then holds the socket open without answering.
    ReadAllThenHold,
    /// Reads the whole body, then closes without writing a byte.
    ReadAllThenClose,
    /// Reads this many body bytes, then resets the connection (SO_LINGER 0).
    ReadThenReset(usize),
}

impl Step {
    pub fn answer(status: u16, body: impl AsRef<[u8]>) -> Step {
        Step::Answer {
            status,
            headers: Vec::new(),
            body: body.as_ref().to_vec(),
            close: false,
        }
    }

    pub fn answer_with_headers(
        status: u16,
        headers: Vec<(String, String)>,
        body: impl AsRef<[u8]>,
    ) -> Step {
        Step::Answer {
            status,
            headers,
            body: body.as_ref().to_vec(),
            close: false,
        }
    }

    /// Answers with `connection: close` and closes the socket.
    pub fn answer_and_close(status: u16, body: impl AsRef<[u8]>) -> Step {
        Step::Answer {
            status,
            headers: vec![("connection".to_string(), "close".to_string())],
            body: body.as_ref().to_vec(),
            close: true,
        }
    }

    pub fn raw(status: u16, headers: Vec<(String, String)>, body: Vec<u8>) -> Step {
        Step::Answer {
            status,
            headers,
            body,
            close: false,
        }
    }

    /// `text/event-stream`, one chunk per string, `gap` between chunks, normal end.
    pub fn sse(chunks: Vec<String>, gap: Duration) -> Step {
        Step::Sse {
            chunks,
            gap,
            cut: false,
        }
    }

    /// Like `sse`, then closes mid-stream (no terminating chunk).
    pub fn sse_then_cut(chunks: Vec<String>, gap: Duration) -> Step {
        Step::Sse {
            chunks,
            gap,
            cut: true,
        }
    }

    pub fn sse_head_then_silence() -> Step {
        Step::SseHeadThenSilence
    }

    /// Declares `declared_len` body bytes, writes `partial`, closes.
    pub fn json_then_cut(status: u16, partial: &str, declared_len: usize) -> Step {
        Step::JsonThenCut {
            status,
            partial: partial.to_string(),
            declared_len,
        }
    }

    /// Answers `101 Switching Protocols`, then echoes every byte.
    pub fn upgrade_echo() -> Step {
        Step::UpgradeEcho
    }
}

/// One request as the fake received it.
#[derive(Clone, Debug)]
pub struct Recorded {
    pub method: String,
    pub path: String,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
    pub raw: Vec<u8>,
    /// The whole announced body arrived.
    pub complete: bool,
    /// The peer closed while the fake was still writing its answer.
    pub aborted: bool,
    pub received_at: Instant,
    pub first_body_byte_at: Option<Instant>,
    /// 1-based connection number, in accept order.
    pub connection: usize,
}

impl Recorded {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

type Plan = dyn Fn(usize, usize) -> Step + Send + Sync;

struct Inner {
    addr: SocketAddr,
    plan: Box<Plan>,
    requests: Mutex<Vec<Recorded>>,
    connections: AtomicUsize,
    received_bytes: AtomicUsize,
    stop: AtomicBool,
    ca_der: Vec<u8>,
    scheme: &'static str,
    host: &'static str,
}

impl Drop for Inner {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        let _ = TcpStream::connect(self.addr);
    }
}

/// The server. It listens on a port the kernel picks and stops when the last clone drops.
#[derive(Clone)]
pub struct FakeUpstream {
    inner: Arc<Inner>,
}

impl FakeUpstream {
    /// A plain HTTP server. `plan(conn, request)` is called once per request, both 1-based.
    pub async fn start<F>(plan: F) -> FakeUpstream
    where
        F: Fn(usize, usize) -> Step + Send + Sync + 'static,
    {
        start_server(Box::new(plan), None)
    }

    /// A TLS server with a self-signed certificate for `localhost` and `127.0.0.1`.
    pub async fn start_tls<F>(plan: F) -> FakeUpstream
    where
        F: Fn(usize, usize) -> Step + Send + Sync + 'static,
    {
        let (config, der) = match tls_config() {
            Ok(pair) => pair,
            Err(err) => panic!("fake tls upstream: {err}"),
        };
        start_server(Box::new(plan), Some((config, der)))
    }

    pub fn addr(&self) -> SocketAddr {
        self.inner.addr
    }

    pub fn port(&self) -> u16 {
        self.inner.addr.port()
    }

    /// `http://127.0.0.1:<port>` or `https://localhost:<port>`.
    pub fn url(&self) -> String {
        format!(
            "{}://{}:{}",
            self.inner.scheme,
            self.inner.host,
            self.port()
        )
    }

    /// The DER of the certificate a client must trust (empty for a plain server).
    pub fn ca_der(&self) -> Vec<u8> {
        self.inner.ca_der.clone()
    }

    pub fn requests(&self) -> Vec<Recorded> {
        self.inner
            .requests
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .clone()
    }

    pub fn complete_requests(&self) -> usize {
        self.requests().iter().filter(|r| r.complete).count()
    }

    pub fn connections(&self) -> usize {
        self.inner.connections.load(Ordering::SeqCst)
    }

    /// Body bytes received so far, across every request.
    pub fn received_bytes(&self) -> usize {
        self.inner.received_bytes.load(Ordering::SeqCst)
    }
}

type TlsSetup = (Arc<ServerConfig>, Vec<u8>);

fn tls_config() -> io::Result<TlsSetup> {
    let certified =
        rcgen::generate_simple_self_signed(vec!["localhost".to_string(), "127.0.0.1".to_string()])
            .map_err(io::Error::other)?;
    let der = certified.cert.der().to_vec();
    let chain = vec![CertificateDer::from(der.clone())];
    let key = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(
        certified.signing_key.serialize_der(),
    ));
    let config =
        ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_safe_default_protocol_versions()
            .map_err(io::Error::other)?
            .with_no_client_auth()
            .with_single_cert(chain, key)
            .map_err(io::Error::other)?;
    Ok((Arc::new(config), der))
}

fn start_server(plan: Box<Plan>, tls: Option<TlsSetup>) -> FakeUpstream {
    let listener = match TcpListener::bind("127.0.0.1:0") {
        Ok(listener) => listener,
        Err(err) => panic!("fake upstream bind: {err}"),
    };
    let addr = match listener.local_addr() {
        Ok(addr) => addr,
        Err(err) => panic!("fake upstream addr: {err}"),
    };
    let (config, ca_der) = match tls {
        Some((config, der)) => (Some(config), der),
        None => (None, Vec::new()),
    };
    let (scheme, host) = if config.is_some() {
        ("https", "localhost")
    } else {
        ("http", "127.0.0.1")
    };
    let inner = Arc::new(Inner {
        addr,
        plan,
        requests: Mutex::new(Vec::new()),
        connections: AtomicUsize::new(0),
        received_bytes: AtomicUsize::new(0),
        stop: AtomicBool::new(false),
        ca_der,
        scheme,
        host,
    });
    let accepting = Arc::downgrade(&inner);
    thread::spawn(move || {
        for stream in listener.incoming() {
            let Some(inner) = accepting.upgrade() else {
                return;
            };
            if inner.stop.load(Ordering::SeqCst) {
                return;
            }
            let Ok(stream) = stream else { continue };
            let _ = stream.set_read_timeout(Some(Duration::from_secs(30)));
            let conn = inner.connections.fetch_add(1, Ordering::SeqCst) + 1;
            let tls = config.clone();
            thread::spawn(move || serve_connection(stream, inner, conn, tls));
        }
    });
    FakeUpstream { inner }
}

fn serve_connection(
    stream: TcpStream,
    inner: Arc<Inner>,
    conn: usize,
    tls: Option<Arc<ServerConfig>>,
) {
    let fd = stream.as_raw_fd();
    match tls {
        Some(config) => {
            if let Ok(server) = ServerConnection::new(config) {
                serve(StreamOwned::new(server, stream), &inner, conn, fd);
            }
        }
        None => serve(stream, &inner, conn, fd),
    }
}

fn serve<S: Read + Write>(mut stream: S, inner: &Inner, conn: usize, fd: RawFd) {
    let mut pending = Vec::new();
    let mut request_number = 0;
    loop {
        let Ok(Some((mut request, head_end))) = read_head(&mut stream, &mut pending) else {
            return;
        };
        request_number += 1;
        request.connection = conn;
        let step = (inner.plan)(conn, request_number);
        let limit = match step {
            Step::ReadThenReset(limit) => Some(limit),
            _ => None,
        };
        read_body(
            &mut stream,
            &mut pending,
            &mut request,
            head_end,
            limit,
            inner,
        );
        let client_close = request
            .header("connection")
            .is_some_and(|value| value.eq_ignore_ascii_case("close"));
        let index = {
            let mut requests = inner
                .requests
                .lock()
                .unwrap_or_else(PoisonError::into_inner);
            requests.push(request);
            requests.len() - 1
        };
        let keep_open = match run_step(&mut stream, &step, fd) {
            Ok(keep_open) => keep_open,
            Err(_) => {
                inner
                    .requests
                    .lock()
                    .unwrap_or_else(PoisonError::into_inner)[index]
                    .aborted = true;
                false
            }
        };
        if !keep_open || client_close {
            return;
        }
    }
}

fn read_head<S: Read>(
    stream: &mut S,
    pending: &mut Vec<u8>,
) -> io::Result<Option<(Recorded, usize)>> {
    let end = loop {
        if let Some(at) = find(pending, b"\r\n\r\n") {
            break at + 4;
        }
        let mut chunk = [0u8; 8192];
        let count = stream.read(&mut chunk)?;
        if count == 0 {
            return Ok(None);
        }
        pending.extend_from_slice(&chunk[..count]);
    };
    let text = String::from_utf8_lossy(&pending[..end]).to_string();
    let mut lines = text.split("\r\n");
    let mut first = lines.next().unwrap_or("").split(' ');
    let method = first.next().unwrap_or("").to_string();
    let path = first.next().unwrap_or("").to_string();
    let headers = lines
        .filter_map(|line| {
            let (name, value) = line.split_once(':')?;
            Some((name.trim().to_string(), value.trim().to_string()))
        })
        .collect();
    Ok(Some((
        Recorded {
            method,
            path,
            headers,
            body: Vec::new(),
            raw: Vec::new(),
            complete: true,
            aborted: false,
            received_at: Instant::now(),
            first_body_byte_at: None,
            connection: 0,
        },
        end,
    )))
}

fn read_body<S: Read>(
    stream: &mut S,
    pending: &mut Vec<u8>,
    request: &mut Recorded,
    head_end: usize,
    limit: Option<usize>,
    inner: &Inner,
) {
    let chunked = request
        .header("transfer-encoding")
        .is_some_and(|value| value.eq_ignore_ascii_case("chunked"));
    let end = if chunked {
        decode_chunked(stream, pending, head_end, request, inner).unwrap_or_else(|| {
            request.complete = false;
            pending.len()
        })
    } else {
        let announced = request
            .header("content-length")
            .and_then(|value| value.parse::<usize>().ok())
            .unwrap_or(0);
        let wanted = limit.map_or(announced, |limit| limit.min(announced));
        if wanted > 0 && pending.len() > head_end {
            request.first_body_byte_at = Some(Instant::now());
        }
        inner.received_bytes.fetch_add(
            pending.len().saturating_sub(head_end).min(wanted),
            Ordering::SeqCst,
        );
        while pending.len() < head_end + wanted {
            let mut chunk = [0u8; 65536];
            match stream.read(&mut chunk) {
                Ok(0) | Err(_) => break,
                Ok(count) => {
                    request.first_body_byte_at.get_or_insert_with(Instant::now);
                    pending.extend_from_slice(&chunk[..count]);
                    inner.received_bytes.fetch_add(count, Ordering::SeqCst);
                }
            }
        }
        let end = (head_end + wanted).min(pending.len());
        request.body = pending[head_end..end].to_vec();
        request.complete = end == head_end + announced;
        end
    };
    request.raw = pending.drain(..end).collect();
}

// Decodes a chunked body in `pending` from `start`; answers the offset just past it.
fn decode_chunked<S: Read>(
    stream: &mut S,
    pending: &mut Vec<u8>,
    start: usize,
    request: &mut Recorded,
    inner: &Inner,
) -> Option<usize> {
    let mut at = start;
    loop {
        let line_end = loop {
            if let Some(found) = find(&pending[at..], b"\r\n") {
                break at + found;
            }
            more(stream, pending)?;
        };
        let size_text = String::from_utf8_lossy(&pending[at..line_end]).to_string();
        let size = usize::from_str_radix(size_text.split(';').next()?.trim(), 16).ok()?;
        at = line_end + 2;
        if size == 0 {
            while find(&pending[at..], b"\r\n").is_none() {
                more(stream, pending)?;
            }
            return Some(at + 2);
        }
        request.first_body_byte_at.get_or_insert_with(Instant::now);
        while pending.len() < at + size + 2 {
            more(stream, pending)?;
        }
        request.body.extend_from_slice(&pending[at..at + size]);
        inner.received_bytes.fetch_add(size, Ordering::SeqCst);
        at += size + 2;
    }
}

fn more<S: Read>(stream: &mut S, pending: &mut Vec<u8>) -> Option<()> {
    let mut chunk = [0u8; 65536];
    match stream.read(&mut chunk) {
        Ok(0) | Err(_) => None,
        Ok(count) => {
            pending.extend_from_slice(&chunk[..count]);
            Some(())
        }
    }
}

// Answers whether the connection stays open for the next request; an Err means the peer closed
// while the fake was writing.
fn run_step<S: Read + Write>(stream: &mut S, step: &Step, fd: RawFd) -> io::Result<bool> {
    match step {
        Step::Answer {
            status,
            headers,
            body,
            close,
        } => {
            write_head(stream, *status, headers, Some(body.len()))?;
            stream.write_all(body)?;
            stream.flush()?;
            Ok(!close)
        }
        Step::Sse { chunks, gap, cut } => {
            write_head(stream, 200, &sse_headers(), None)?;
            for chunk in chunks {
                write_chunk(stream, chunk.as_bytes())?;
                thread::sleep(*gap);
            }
            if *cut {
                return Ok(false);
            }
            stream.write_all(b"0\r\n\r\n")?;
            stream.flush()?;
            Ok(true)
        }
        Step::SseHeadThenSilence => {
            write_head(stream, 200, &sse_headers(), None)?;
            hold(stream);
            Ok(false)
        }
        Step::JsonThenCut {
            status,
            partial,
            declared_len,
        } => {
            write_head(stream, *status, &[], Some(*declared_len))?;
            stream.write_all(partial.as_bytes())?;
            stream.flush()?;
            Ok(false)
        }
        Step::UpgradeEcho => {
            stream.write_all(
                b"HTTP/1.1 101 Switching Protocols\r\nconnection: upgrade\r\nupgrade: echo\r\n\r\n",
            )?;
            stream.flush()?;
            let mut chunk = [0u8; 8192];
            loop {
                let count = stream.read(&mut chunk)?;
                if count == 0 {
                    return Ok(false);
                }
                stream.write_all(&chunk[..count])?;
                stream.flush()?;
            }
        }
        Step::ReadAllThenHold => {
            hold(stream);
            Ok(false)
        }
        Step::ReadAllThenClose => Ok(false),
        Step::ReadThenReset(_) => {
            reset(fd);
            Ok(false)
        }
    }
}

fn sse_headers() -> Vec<(String, String)> {
    vec![("content-type".to_string(), "text/event-stream".to_string())]
}

// Keeps the socket open until the peer closes it or the read timeout passes.
fn hold<S: Read>(stream: &mut S) {
    let mut chunk = [0u8; 4096];
    while matches!(stream.read(&mut chunk), Ok(count) if count > 0) {}
}

fn reset(fd: RawFd) {
    let linger = libc::linger {
        l_onoff: 1,
        l_linger: 0,
    };
    // SAFETY: `fd` is the open socket of this connection and `linger` is a valid option value.
    unsafe {
        libc::setsockopt(
            fd,
            libc::SOL_SOCKET,
            libc::SO_LINGER,
            (&raw const linger).cast(),
            std::mem::size_of::<libc::linger>() as libc::socklen_t,
        );
    }
}

fn write_head<S: Write>(
    stream: &mut S,
    status: u16,
    headers: &[(String, String)],
    length: Option<usize>,
) -> io::Result<()> {
    let mut head = format!("HTTP/1.1 {status} X\r\n");
    for (name, value) in headers {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    match length {
        Some(length) => head.push_str(&format!("content-length: {length}\r\n\r\n")),
        None => head.push_str("transfer-encoding: chunked\r\n\r\n"),
    }
    stream.write_all(head.as_bytes())?;
    stream.flush()
}

fn write_chunk<S: Write>(stream: &mut S, chunk: &[u8]) -> io::Result<()> {
    stream.write_all(format!("{:x}\r\n", chunk.len()).as_bytes())?;
    stream.write_all(chunk)?;
    stream.write_all(b"\r\n")?;
    stream.flush()
}

fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}
