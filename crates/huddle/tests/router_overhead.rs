//! The router's latency gate on the release build. Numbers are medians of paired deltas in one run.

use huddle_pki::ca;
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use rustls::pki_types::{CertificateDer, ServerName};
use rustls::server::{ClientHello, ResolvesServerCert};
use rustls::sign::CertifiedKey;
use rustls::{
    ClientConfig, ClientConnection, RootCertStore, ServerConfig, ServerConnection, StreamOwned,
};
use std::{
    env, fs,
    io::{self, BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::Arc,
    thread,
    time::{Duration, Instant, SystemTime},
};

const FIRST_BYTE_BUDGET: Duration = Duration::from_millis(5);
const CHUNK_BUDGET: Duration = Duration::from_millis(1);
const CHUNKS: usize = 200;
const GAP: Duration = Duration::from_millis(10);
const RUNS: usize = 20;
const SIZES: [usize; 3] = [0, 100 * 1024, 2 * 1024 * 1024];
const REFERENCE_UPSTREAM: &str = "OVERHEAD_REFERENCE_UPSTREAM";
const REFERENCE_CA_DIR: &str = "OVERHEAD_REFERENCE_CA_DIR";

trait Conn: Read + Write + Send {}
impl<T: Read + Write + Send> Conn for T {}

#[derive(Default, Clone, Copy)]
struct Timing {
    first_byte: Duration,
    chunk: Duration,
}

/// Streams one Anthropic-shaped answer per connection, CHUNKS deltas GAP apart, each carrying `@<ns>@`.
fn start_fake(base: Instant) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let addr = listener.local_addr().expect("addr").to_string();
    thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(stream) = stream else { return };
            thread::spawn(move || serve_one(stream, base));
        }
    });
    addr
}

fn serve_one(stream: TcpStream, base: Instant) {
    let mut reader = BufReader::new(stream);
    let mut length = 0usize;
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line).unwrap_or(0) == 0 {
            return;
        }
        if line == "\r\n" {
            break;
        }
        if let Some(rest) = line.to_ascii_lowercase().strip_prefix("content-length:") {
            length = rest.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0u8; length];
    if reader.read_exact(&mut body).is_err() {
        return;
    }
    let mut stream = reader.into_inner();
    let head = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n";
    if stream.write_all(head.as_bytes()).is_err() {
        return;
    }
    let start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n";
    if stream.write_all(start.as_bytes()).is_err() {
        return;
    }
    for _ in 0..CHUNKS {
        thread::sleep(GAP);
        let at = base.elapsed().as_nanos();
        let delta = format!(
            "event: content_block_delta\ndata: {{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{{\"type\":\"text_delta\",\"text\":\"@{at}@\"}}}}\n\n"
        );
        if stream.write_all(delta.as_bytes()).is_err() {
            return;
        }
    }
    let _ = stream.write_all(b"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n");
}

fn tls_client_config(ca_der: &[u8]) -> Arc<ClientConfig> {
    let mut roots = RootCertStore::empty();
    roots
        .add(CertificateDer::from(ca_der.to_vec()))
        .expect("trust the ca");
    Arc::new(
        ClientConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_safe_default_protocol_versions()
            .expect("versions")
            .with_root_certificates(roots)
            .with_no_client_auth(),
    )
}

#[derive(Debug)]
struct FixedCert(Arc<CertifiedKey>);
impl ResolvesServerCert for FixedCert {
    fn resolve(&self, _hello: ClientHello<'_>) -> Option<Arc<CertifiedKey>> {
        Some(Arc::clone(&self.0))
    }
}

fn tls_connect(tcp: TcpStream, config: &Arc<ClientConfig>) -> Box<dyn Conn> {
    let server = ServerName::try_from("api.anthropic.com")
        .expect("name")
        .to_owned();
    let conn = ClientConnection::new(Arc::clone(config), server).expect("client");
    Box::new(StreamOwned::new(conn, tcp))
}

/// One POST and its timed stream, over any connection.
fn read_stream(
    mut conn: Box<dyn Conn>,
    host: &str,
    path: &str,
    model: &str,
    filler: usize,
    base: Instant,
    started: Instant,
) -> io::Result<Timing> {
    let body = format!(
        "{{\"model\":\"{model}\",\"max_tokens\":16,\"stream\":true,\"messages\":[{{\"role\":\"user\",\"content\":\"hi{}\"}}]}}",
        "x".repeat(filler)
    );
    let head = format!(
        "POST {path} HTTP/1.1\r\nHost: {host}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    conn.write_all(head.as_bytes())?;
    conn.write_all(body.as_bytes())?;
    conn.flush()?;
    let mut reader = BufReader::new(conn);
    let mut timing = Timing::default();
    let mut trips: Vec<Duration> = Vec::with_capacity(CHUNKS);
    loop {
        let mut line = String::new();
        let read = reader.read_line(&mut line)?;
        if read == 0 {
            break;
        }
        if timing.first_byte.is_zero() {
            timing.first_byte = started.elapsed();
        }
        if let Some(at) = line.find("\"text\":\"@") {
            let rest = &line[at + "\"text\":\"@".len()..];
            let Some(end) = rest.find('@') else { continue };
            let sent: u128 = rest[..end].parse().unwrap_or(0);
            let arrived = base.elapsed().as_nanos();
            trips.push(Duration::from_nanos((arrived - sent) as u64));
        }
        if line.contains("message_stop") {
            break;
        }
    }
    trips.sort();
    if !trips.is_empty() {
        timing.chunk = trips[trips.len() / 2];
    }
    Ok(timing)
}

fn median(mut values: Vec<f64>) -> f64 {
    values.sort_by(|a, b| a.partial_cmp(b).expect("no nan"));
    values[values.len() / 2]
}

fn delta(a: Duration, b: Duration) -> f64 {
    a.as_secs_f64() * 1000.0 - b.as_secs_f64() * 1000.0
}

struct Reference {
    child: Child,
    relay: String,
    twin: String,
}

impl Drop for Reference {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn start_reference(upstream: &str, ca_dir: &std::path::Path) -> Reference {
    let mut child = Command::new(env::current_exe().expect("exe"))
        .args([
            "--exact",
            "overhead_reference_helper",
            "--ignored",
            "--nocapture",
        ])
        .env(REFERENCE_UPSTREAM, upstream)
        .env(REFERENCE_CA_DIR, ca_dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit())
        .spawn()
        .expect("reference");
    let stdout = child.stdout.take().expect("stdout");
    let mut reader = BufReader::new(stdout);
    let mut answer = None;
    for line in reader.by_ref().lines() {
        let line = line.expect("line");
        let fields: Vec<&str> = line.split_whitespace().collect();
        if fields.len() == 4 && fields[0] == "ready" {
            answer = Some((fields[2].to_string(), fields[3].to_string()));
            break;
        }
    }
    let Some((relay, twin)) = answer else {
        panic!("the reference process never said it was ready");
    };
    // The child's harness reports a test that runs past one minute; that report goes to this pipe.
    // Left unread, the write breaks the pipe and the child dies mid-run, dropping every socket.
    thread::spawn(move || {
        let mut sink = [0u8; 512];
        while let Ok(n) = reader.read(&mut sink) {
            if n == 0 {
                break;
            }
        }
    });
    Reference { child, relay, twin }
}

/// The re-executed reference: a plain relay and a TLS twin to the fake, in a process of its own.
#[test]
#[ignore = "helper process for the overhead gate"]
fn overhead_reference_helper() {
    let Ok(upstream) = env::var(REFERENCE_UPSTREAM) else {
        return;
    };
    let ca_dir = PathBuf::from(env::var(REFERENCE_CA_DIR).expect("ca dir"));
    let relay = TcpListener::bind("127.0.0.1:0").expect("relay");
    let twin = TcpListener::bind("127.0.0.1:0").expect("twin");
    let ca = ca::load_or_create(&ca_dir, SystemTime::now()).expect("ca");
    let leaf = ca
        .leaf("api.anthropic.com", SystemTime::now())
        .expect("leaf");
    let config = Arc::new(
        ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_safe_default_protocol_versions()
            .expect("versions")
            .with_no_client_auth()
            .with_cert_resolver(Arc::new(FixedCert(leaf.certified.clone()))),
    );
    let relay_addr = relay.local_addr().expect("addr").to_string();
    let twin_addr = twin.local_addr().expect("addr").to_string();
    {
        let upstream = upstream.clone();
        thread::spawn(move || accept_relay(relay, upstream));
    }
    thread::spawn(move || accept_twin(twin, config, upstream));
    println!("ready {} {relay_addr} {twin_addr}", std::process::id());
    io::stdout().flush().expect("flush");
    let mut sink = [0u8; 16];
    while io::stdin().read(&mut sink).unwrap_or(0) > 0 {}
}

fn accept_relay(listener: TcpListener, upstream: String) {
    for client in listener.incoming() {
        let Ok(client) = client else { return };
        let upstream = upstream.clone();
        thread::spawn(move || {
            let Ok(server) = TcpStream::connect(&upstream) else {
                return;
            };
            copy_both(Box::new(client), Box::new(server));
        });
    }
}

fn accept_twin(listener: TcpListener, config: Arc<ServerConfig>, upstream: String) {
    for client in listener.incoming() {
        let Ok(tcp) = client else { return };
        let config = Arc::clone(&config);
        let upstream = upstream.clone();
        thread::spawn(move || {
            let Ok(server) = TcpStream::connect(&upstream) else {
                return;
            };
            let conn = ServerConnection::new(config).expect("server");
            let tls = StreamOwned::new(conn, tcp);
            copy_both(Box::new(tls), Box::new(server));
        });
    }
}

fn copy_both(mut a: Box<dyn Conn>, mut b: Box<dyn Conn>) {
    // One thread each way would need a split; a sequential request then response is what these connections do.
    let _ = forward_request(&mut a, &mut b);
    let _ = io::copy(&mut b, &mut a);
}

fn forward_request(a: &mut Box<dyn Conn>, b: &mut Box<dyn Conn>) -> io::Result<()> {
    let mut reader = BufReader::new(a);
    let mut head = Vec::new();
    let mut length = 0usize;
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line)? == 0 {
            return Ok(());
        }
        head.extend_from_slice(line.as_bytes());
        if line == "\r\n" {
            break;
        }
        if let Some(rest) = line.to_ascii_lowercase().strip_prefix("content-length:") {
            length = rest.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0u8; length];
    reader.read_exact(&mut body)?;
    b.write_all(&head)?;
    b.write_all(&body)?;
    b.flush()
}

fn start_router(home: &TempHome, port: u16, upstream: &str) {
    let dir = home.root().join(".config/huddle");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::write(dir.join("providers.toml"), "providers = [\"zai\"]\n").expect("write");
    let started = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "start"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .env("ZAI_API_KEY", "test-key")
        .env("ZAI_ROUTER_URL", format!("http://{upstream}"))
        .env("HUDDLE_ROUTER_ANTHROPIC_URL", format!("http://{upstream}"))
        .output()
        .expect("router start");
    assert_eq!(started.status.code(), Some(0), "{started:?}");
}

fn stop_router(home: &TempHome, port: u16) {
    let _ = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "stop"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .output();
}

fn read_connect(
    port: u16,
    config: &Arc<ClientConfig>,
    model: &str,
    filler: usize,
    base: Instant,
) -> io::Result<Timing> {
    let started = Instant::now();
    let mut tcp = TcpStream::connect(("127.0.0.1", port))?;
    tcp.write_all(
        b"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n",
    )?;
    let mut reader = BufReader::new(tcp);
    let mut status = String::new();
    reader.read_line(&mut status)?;
    assert!(status.contains("200"), "{status}");
    let mut blank = String::new();
    reader.read_line(&mut blank)?;
    let tcp = reader.into_inner();
    read_stream(
        tls_connect(tcp, config),
        "api.anthropic.com",
        "/v1/messages",
        model,
        filler,
        base,
        started,
    )
}

#[test]
#[cfg_attr(debug_assertions, ignore = "the budget is for the release build")]
fn the_router_stays_under_its_latency_budget() {
    let home = TempHome::new();
    let ca_dir = home.root().join(".local/share/huddle/ca");
    let ca = ca::load_or_create(&ca_dir, SystemTime::now()).expect("ca");
    let router_tls = tls_client_config(ca.cert_der());
    let base = Instant::now();
    let fake = start_fake(base);
    let port = free_port();
    start_router(&home, port, &fake);
    let reference = start_reference(&fake, &ca_dir);
    let twin_tls = Arc::clone(&router_tls);

    for size in SIZES {
        let mut lane_first = Vec::with_capacity(RUNS);
        let mut lane_chunk = Vec::with_capacity(RUNS);
        let mut lane_hop = Vec::with_capacity(RUNS);
        let mut connect_first = Vec::with_capacity(RUNS);
        let mut connect_chunk = Vec::with_capacity(RUNS);
        for run in 0..RUNS {
            let fake_addr = fake.clone();
            let relay_addr = reference.relay.clone();
            let twin_addr = reference.twin.clone();
            let router_tls = Arc::clone(&router_tls);
            let twin_tls = Arc::clone(&twin_tls);
            thread::scope(|scope| {
                let direct = scope.spawn(|| {
                    let started = Instant::now();
                    let tcp = TcpStream::connect(&fake_addr).expect("direct");
                    read_stream(
                        Box::new(tcp),
                        &fake_addr,
                        "/v1/messages",
                        "direct",
                        size,
                        base,
                        started,
                    )
                });
                let relay = scope.spawn(|| {
                    let started = Instant::now();
                    let tcp = TcpStream::connect(&relay_addr).expect("relay");
                    read_stream(
                        Box::new(tcp),
                        &relay_addr,
                        "/v1/messages",
                        "relay",
                        size,
                        base,
                        started,
                    )
                });
                let lane = scope.spawn(|| {
                    let started = Instant::now();
                    let tcp = TcpStream::connect(("127.0.0.1", port)).expect("lane");
                    read_stream(
                        Box::new(tcp),
                        &format!("127.0.0.1:{port}"),
                        "/v1/messages",
                        "glm-5.3",
                        size,
                        base,
                        started,
                    )
                });
                let twin = scope.spawn(|| {
                    let started = Instant::now();
                    let tcp = TcpStream::connect(&twin_addr).expect("twin");
                    read_stream(
                        tls_connect(tcp, &twin_tls),
                        "api.anthropic.com",
                        "/v1/messages",
                        "twin",
                        size,
                        base,
                        started,
                    )
                });
                let connect =
                    scope.spawn(|| read_connect(port, &router_tls, "claude-opus-5-5", size, base));
                let direct = direct.join().expect("join").expect("direct path");
                let relay = relay.join().expect("join").expect("relay path");
                let lane = lane.join().expect("join").expect("lane path");
                let twin = twin.join().expect("join").expect("twin path");
                let connect = connect.join().expect("join").expect("connect path");
                lane_first.push(delta(lane.first_byte, relay.first_byte));
                lane_chunk.push(delta(lane.chunk, relay.chunk));
                lane_hop.push(delta(lane.first_byte, direct.first_byte));
                connect_first.push(delta(connect.first_byte, twin.first_byte));
                connect_chunk.push(delta(connect.chunk, twin.chunk));
            });
            eprintln!("size {size} run {run} done");
        }
        let lane_first = median(lane_first);
        let lane_chunk = median(lane_chunk);
        let connect_first = median(connect_first);
        let connect_chunk = median(connect_chunk);
        eprintln!(
            "size {size}: first byte lane-relay {lane_first:.2} ms (direct delta {:.2} ms), connect-twin {connect_first:.2} ms; chunk lane-relay {lane_chunk:.3} ms, connect-twin {connect_chunk:.3} ms",
            median(lane_hop)
        );
        assert!(
            lane_first < FIRST_BYTE_BUDGET.as_secs_f64() * 1000.0,
            "lane first byte {lane_first:.2} ms over {size} bytes"
        );
        assert!(
            connect_first < FIRST_BYTE_BUDGET.as_secs_f64() * 1000.0,
            "connect first byte {connect_first:.2} ms over {size} bytes"
        );
        assert!(
            lane_chunk < CHUNK_BUDGET.as_secs_f64() * 1000.0,
            "lane chunk {lane_chunk:.3} ms over {size} bytes"
        );
        assert!(
            connect_chunk < CHUNK_BUDGET.as_secs_f64() * 1000.0,
            "connect chunk {connect_chunk:.3} ms over {size} bytes"
        );
    }
    assert_router_us(&home);
    drop(reference);
    stop_router(&home, port);
}

/// R-057: every routed event carries the router's own time, at least one microsecond.
fn assert_router_us(home: &TempHome) {
    let dir = home.root().join(".local/state/huddle/events");
    let mut routed = 0usize;
    for entry in fs::read_dir(&dir).expect("events dir") {
        let text = fs::read_to_string(entry.expect("entry").path()).expect("read");
        for line in text.lines() {
            let event: serde_json::Value = serde_json::from_str(line).expect("event json");
            if event["route"] == "provider" || event["route"] == "anthropic" {
                routed += 1;
                let router_us = event["router_us"].as_u64().unwrap_or(0);
                assert!(router_us >= 1, "{line}");
            }
        }
    }
    assert!(routed > 0, "no routed events");
}
