use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use rustls::pki_types::{CertificateDer, ServerName};

fn run<F: Future>(future: F) -> F::Output {
    // The fake's `start` futures never await anything, so one poll finishes them.
    let waker = std::task::Waker::noop();
    let mut context = std::task::Context::from_waker(waker);
    let mut future = std::pin::pin!(future);
    match future.as_mut().poll(&mut context) {
        std::task::Poll::Ready(output) => output,
        std::task::Poll::Pending => panic!("a fake upstream start future never pends"),
    }
}

fn send(fake: &FakeUpstream, request: &str) -> String {
    let mut stream = TcpStream::connect(fake.addr()).unwrap();
    stream.write_all(request.as_bytes()).unwrap();
    let mut answer = String::new();
    let _ = stream.read_to_string(&mut answer);
    answer
}

fn post(fake: &FakeUpstream, body: &str) -> String {
    send(
        fake,
        &format!(
            "POST /v1/messages HTTP/1.1\r\nhost: x\r\nconnection: close\r\ncontent-length: {}\r\n\r\n{body}",
            body.len()
        ),
    )
}

#[test]
fn it_records_a_request_and_answers_from_the_plan() {
    let fake = run(FakeUpstream::start(|_, _| Step::answer(200, "{\"id\":1}")));
    let answer = post(&fake, "hello");
    assert!(answer.starts_with("HTTP/1.1 200"), "{answer}");
    assert!(answer.ends_with("{\"id\":1}"), "{answer}");
    let requests = fake.requests();
    assert_eq!(requests.len(), 1);
    assert_eq!(requests[0].method, "POST");
    assert_eq!(requests[0].path, "/v1/messages");
    assert_eq!(requests[0].header("Host"), Some("x"));
    assert_eq!(requests[0].body, b"hello");
    assert!(requests[0].raw.ends_with(b"hello"));
    assert!(requests[0].complete);
    assert_eq!(fake.complete_requests(), 1);
    assert_eq!(fake.received_bytes(), 5);
}

#[test]
fn the_plan_sees_the_connection_and_request_numbers() {
    let fake = run(FakeUpstream::start(|conn, request| {
        Step::answer(200, format!("{conn}/{request}"))
    }));
    let mut stream = TcpStream::connect(fake.addr()).unwrap();
    for number in 1..=2 {
        write!(stream, "GET /{number} HTTP/1.1\r\nhost: x\r\n\r\n").unwrap();
        let mut text = String::new();
        while !text.ends_with(&format!("1/{number}")) {
            let mut buffer = [0u8; 256];
            let count = stream.read(&mut buffer).unwrap();
            assert!(count > 0, "the connection closed early: {text}");
            text.push_str(&String::from_utf8_lossy(&buffer[..count]));
        }
    }
    assert_eq!(fake.connections(), 1);
    assert_eq!(fake.requests()[1].connection, 1);
}

#[test]
fn sse_streams_and_a_cut_ends_without_the_terminator() {
    let chunks = vec!["data: 1\n\n".to_string(), "data: 2\n\n".to_string()];
    let (plain, cut) = (chunks.clone(), chunks);
    let fake = run(FakeUpstream::start(move |conn, _| {
        if conn == 1 {
            Step::sse(plain.clone(), Duration::from_millis(1))
        } else {
            Step::sse_then_cut(cut.clone(), Duration::from_millis(1))
        }
    }));
    let whole = post(&fake, "x");
    assert!(whole.contains("content-type: text/event-stream"), "{whole}");
    assert!(whole.ends_with("0\r\n\r\n"), "{whole}");
    let cut_answer = post(&fake, "y");
    assert!(cut_answer.contains("data: 2"), "{cut_answer}");
    assert!(!cut_answer.ends_with("0\r\n\r\n"), "{cut_answer}");
}

#[test]
fn json_then_cut_declares_more_than_it_sends() {
    let fake = run(FakeUpstream::start(|_, _| {
        Step::json_then_cut(200, "{\"partial\":", 30)
    }));
    let answer = post(&fake, "x");
    assert!(answer.contains("content-length: 30"), "{answer}");
    assert!(answer.ends_with("{\"partial\":"), "{answer}");
}

#[test]
fn read_all_then_close_reads_the_whole_body_and_writes_nothing() {
    let fake = run(FakeUpstream::start(|_, _| Step::ReadAllThenClose));
    assert_eq!(post(&fake, "abc"), "");
    let requests = fake.requests();
    assert_eq!(requests[0].body, b"abc");
    assert!(requests[0].complete);
}

#[test]
fn read_then_reset_stops_after_the_given_bytes_and_records_an_incomplete_request() {
    let fake = run(FakeUpstream::start(|_, _| Step::ReadThenReset(3)));
    let mut stream = TcpStream::connect(fake.addr()).unwrap();
    stream
        .write_all(b"POST / HTTP/1.1\r\ncontent-length: 10\r\n\r\nabc")
        .unwrap();
    let mut answer = String::new();
    let _ = stream.read_to_string(&mut answer);
    let requests = fake.requests();
    assert_eq!(requests[0].body, b"abc");
    assert!(!requests[0].complete);
}

#[test]
fn read_all_then_hold_keeps_the_socket_open_without_answering() {
    let fake = run(FakeUpstream::start(|_, _| Step::ReadAllThenHold));
    let mut stream = TcpStream::connect(fake.addr()).unwrap();
    stream
        .write_all(b"POST / HTTP/1.1\r\ncontent-length: 2\r\n\r\nab")
        .unwrap();
    stream
        .set_read_timeout(Some(Duration::from_millis(100)))
        .unwrap();
    let mut buffer = [0u8; 16];
    let started = Instant::now();
    assert!(stream.read(&mut buffer).is_err());
    assert!(started.elapsed() >= Duration::from_millis(50));
    thread::sleep(Duration::from_millis(20));
    assert_eq!(fake.requests()[0].body, b"ab");
}

#[test]
fn upgrade_echo_answers_101_and_echoes_bytes() {
    let fake = run(FakeUpstream::start(|_, _| Step::upgrade_echo()));
    let mut stream = TcpStream::connect(fake.addr()).unwrap();
    stream
        .write_all(b"GET / HTTP/1.1\r\nconnection: upgrade\r\n\r\n")
        .unwrap();
    let mut buffer = [0u8; 256];
    let count = stream.read(&mut buffer).unwrap();
    assert!(String::from_utf8_lossy(&buffer[..count]).starts_with("HTTP/1.1 101"));
    stream.write_all(b"ping").unwrap();
    let count = stream.read(&mut buffer).unwrap();
    assert_eq!(&buffer[..count], b"ping");
}

#[test]
fn a_chunked_request_body_is_decoded_and_counted() {
    let fake = run(FakeUpstream::start(|_, _| Step::answer(200, "ok")));
    send(
        &fake,
        "POST / HTTP/1.1\r\ntransfer-encoding: chunked\r\nconnection: close\r\n\r\n3\r\nabc\r\n2\r\nde\r\n0\r\n\r\n",
    );
    assert_eq!(fake.requests()[0].body, b"abcde");
    assert_eq!(fake.received_bytes(), 5);
}

#[test]
fn a_tls_upstream_serves_a_client_that_trusts_its_certificate() {
    let fake = run(FakeUpstream::start_tls(|_, _| Step::answer(200, "tls ok")));
    let mut roots = rustls::RootCertStore::empty();
    roots.add(CertificateDer::from(fake.ca_der())).unwrap();
    let config = rustls::ClientConfig::builder_with_provider(Arc::new(
        rustls::crypto::ring::default_provider(),
    ))
    .with_safe_default_protocol_versions()
    .unwrap()
    .with_root_certificates(roots)
    .with_no_client_auth();
    let name = ServerName::try_from("localhost").unwrap();
    let conn = rustls::ClientConnection::new(Arc::new(config), name).unwrap();
    let tcp = TcpStream::connect(fake.addr()).unwrap();
    let mut tls = rustls::StreamOwned::new(conn, tcp);
    tls.write_all(b"GET /health HTTP/1.1\r\nconnection: close\r\n\r\n")
        .unwrap();
    let mut answer = Vec::new();
    let _ = tls.read_to_end(&mut answer);
    assert!(String::from_utf8_lossy(&answer).ends_with("tls ok"));
    assert_eq!(fake.requests()[0].path, "/health");
    assert!(fake.url().starts_with("https://localhost:"));
}
