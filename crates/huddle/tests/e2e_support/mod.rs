#![allow(dead_code)]

use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

pub struct Daemon {
    pub home: TempHome,
    pub port: u16,
    pub config_dir: PathBuf,
    child: Child,
}

impl Drop for Daemon {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

impl Daemon {
    /// `huddle router serve` for `providers`, plus any extra environment. Retries on a fresh
    /// port: between the port lease and the child's bind, a foreign process can take the port
    /// and the child then exits naming it.
    pub fn start(providers: &[&str], extra: &[(&str, String)]) -> Daemon {
        let mut last = String::from("no attempt made");
        for _ in 0..3 {
            match Self::try_start(providers, extra) {
                Ok(daemon) => return daemon,
                Err(error) => last = error,
            }
        }
        panic!("the router did not start after 3 attempts: {last}");
    }

    fn try_start(providers: &[&str], extra: &[(&str, String)]) -> Result<Daemon, String> {
        let home = TempHome::new();
        let port = free_port();
        let config_dir = home.root().join(".config/huddle");
        fs::create_dir_all(&config_dir).expect("mkdir router home");
        let list: Vec<String> = providers.iter().map(|p| format!("\"{p}\"")).collect();
        fs::write(
            config_dir.join("providers.toml"),
            format!("providers = [{}]\n", list.join(", ")),
        )
        .expect("write providers");

        let mut cmd = Command::new(env!("CARGO_BIN_EXE_huddle"));
        cmd.args(["router", "serve"])
            .env_clear()
            .envs(home.env())
            .env("HUDDLE_ROUTER_PORT", port.to_string())
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped());
        for (k, v) in extra {
            cmd.env(k, v);
        }
        let mut child = cmd.spawn().expect("spawn huddle router serve");
        let stderr = child.stderr.take().expect("stderr");
        let stderr_text = Arc::new(Mutex::new(Vec::new()));
        let sink = Arc::clone(&stderr_text);
        thread::spawn(move || {
            let mut stderr = stderr;
            let mut chunk = [0u8; 4096];
            while let Ok(n) = stderr.read(&mut chunk) {
                if n == 0 {
                    break;
                }
                sink.lock()
                    .expect("stderr lock")
                    .extend_from_slice(&chunk[..n]);
            }
        });

        let deadline = Instant::now() + Duration::from_secs(30);
        while Instant::now() < deadline {
            if let Some(reply) = try_request(port, "GET", "/huddle-router/health", &[], b"")
                && reply.status == 200
            {
                return Ok(Daemon {
                    home,
                    port,
                    config_dir,
                    child,
                });
            }
            if let Some(status) = child.try_wait().expect("try wait") {
                let bytes = stderr_text.lock().expect("stderr lock");
                let _ = child.wait();
                return Err(format!(
                    "the router exited with {status} before answering health on port {port}: {}",
                    String::from_utf8_lossy(&bytes)
                ));
            }
            thread::sleep(Duration::from_millis(50));
        }
        let status = child.try_wait().expect("try wait");
        let bytes = stderr_text.lock().expect("stderr lock");
        let _ = child.kill();
        let _ = child.wait();
        Err(format!(
            "the router did not answer health on port {port} in 30 s (exit status: {status:?}): {}",
            String::from_utf8_lossy(&bytes)
        ))
    }
}

/// A fake upstream answering every request `200` with the JSON `answer`.
pub fn upstream(answer: &'static str) -> FakeUpstream {
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |_, _| {
            Step::answer_with_headers(
                200,
                vec![("content-type".to_string(), "application/json".to_string())],
                answer,
            )
        }))
}

#[derive(Debug)]
pub struct Reply {
    pub status: u16,
    pub headers: Vec<(String, String)>,
    pub body: String,
}

impl Reply {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(name))
            .map(|(_, v)| v.as_str())
    }
}

/// One HTTP/1.1 request to the router; None when the connection itself failed.
pub fn try_request(
    port: u16,
    method: &str,
    path: &str,
    headers: &[(&str, &str)],
    body: &[u8],
) -> Option<Reply> {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).ok()?;
    stream
        .set_read_timeout(Some(Duration::from_secs(20)))
        .ok()?;
    let mut text = format!(
        "{method} {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\nContent-Length: {}\r\n",
        body.len()
    );
    for (k, v) in headers {
        text.push_str(&format!("{k}: {v}\r\n"));
    }
    text.push_str("\r\n");
    stream.write_all(text.as_bytes()).ok()?;
    stream.write_all(body).ok()?;
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).ok()?;
    parse_reply(&raw)
}

pub fn request(
    port: u16,
    method: &str,
    path: &str,
    headers: &[(&str, &str)],
    body: &[u8],
) -> Reply {
    try_request(port, method, path, headers, body).expect("the router answered")
}

fn parse_reply(raw: &[u8]) -> Option<Reply> {
    let text = String::from_utf8_lossy(raw);
    let (head, body) = text.split_once("\r\n\r\n")?;
    let mut lines = head.split("\r\n");
    let status_line = lines.next()?;
    let status: u16 = status_line.split_whitespace().nth(1)?.parse().ok()?;
    let headers = lines
        .filter_map(|line| line.split_once(": "))
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    Some(Reply {
        status,
        headers,
        body: body.to_string(),
    })
}

/// A raw request/response exchange, for CONNECT and for malformed requests.
pub fn raw_exchange(port: u16, request: &str) -> String {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).expect("connect");
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .expect("timeout");
    stream.write_all(request.as_bytes()).expect("write");
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    String::from_utf8_lossy(&raw).into_owned()
}

/// Waits until every job driver under the home stops writing: a driver removes its own record
/// last, so once no driver.toml names a live process, nothing writes into the home again and
/// TempHome's drop cannot race a late record into existence.
pub fn wait_drivers_done(home_root: &std::path::Path) {
    // A detached driver writes its record within moments of the word that spawned it.
    std::thread::sleep(std::time::Duration::from_millis(300));
    let jobs = home_root.join(".local/state/huddle/jobs");
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
    while std::time::Instant::now() < deadline {
        let mut live = 0;
        for slug in std::fs::read_dir(&jobs).into_iter().flatten().flatten() {
            for dir in std::fs::read_dir(slug.path())
                .into_iter()
                .flatten()
                .flatten()
            {
                if let Some(record) = huddle_jobs::liveness::read_record(
                    &huddle_jobs::liveness::driver_record_path(&dir.path()),
                ) && huddle_procs::live::process_alive(record.pid, &record.start_token)
                {
                    live += 1;
                }
            }
        }
        if live == 0 {
            return;
        }
        std::thread::sleep(std::time::Duration::from_millis(50));
    }
}
