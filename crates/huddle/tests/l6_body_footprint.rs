//! L6: eight parallel 20 MiB bodies through CONNECT grow the router worker's memory footprint by
//! under 32 MiB. The router streams a body to the upstream with a bounded window, so the
//! footprint does not follow the size of the uploads.

mod e2e_support;

use e2e_support::Daemon;
use huddle_pki::ca::{ca_cert_path, load_or_create};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use std::fs::{self, File};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::thread;
use std::time::{Duration, SystemTime};

const BODY_BYTES: usize = 20 * 1024 * 1024;
const PARALLEL: usize = 8;
const LIMIT: u64 = 32 * 1024 * 1024;

/// The worker's footprint in bytes: `ri_phys_footprint` on macOS, the resident set on Linux.
#[cfg(target_os = "macos")]
#[allow(deprecated)]
fn footprint(pid: i32) -> Option<u64> {
    let mut info = std::mem::MaybeUninit::<libc::rusage_info_v2>::uninit();
    // SAFETY: the buffer is a `rusage_info_v2`, the layout flavor 2 fills.
    let status =
        unsafe { libc::proc_pid_rusage(pid, libc::RUSAGE_INFO_V2, info.as_mut_ptr().cast()) };
    if status != 0 {
        return None;
    }
    // SAFETY: the call succeeded, so the kernel filled the whole struct.
    Some(unsafe { info.assume_init() }.ri_phys_footprint)
}

#[cfg(target_os = "linux")]
fn footprint(pid: i32) -> Option<u64> {
    let status = fs::read_to_string(format!("/proc/{pid}/status")).ok()?;
    let kib = |key: &str| {
        status
            .lines()
            .find_map(|line| line.strip_prefix(key))
            .and_then(|rest| {
                rest.trim()
                    .trim_end_matches("kB")
                    .trim()
                    .parse::<u64>()
                    .ok()
            })
    };
    // The high-water mark keeps a peak between two samples.
    Some(kib("VmHWM:")?.max(kib("VmRSS:")?) * 1024)
}

fn worker_pid(daemon: &Daemon) -> i32 {
    let text = fs::read_to_string(daemon.home.root().join("r/huddle/router.toml"))
        .expect("the router record");
    let doc: toml::Table = text.parse().expect("the record is toml");
    let number = |key: &str| doc.get(key).and_then(toml::Value::as_integer).unwrap_or(0);
    let worker = number("worker");
    i32::try_from(if worker > 0 { worker } else { number("pid") }).expect("a pid")
}

/// Samples the footprint every 10 ms until dropped; `peak` is the highest reading.
struct Sampler {
    stop: Arc<AtomicBool>,
    peak: Arc<AtomicU64>,
    thread: Option<thread::JoinHandle<()>>,
}

impl Sampler {
    fn start(pid: i32) -> Sampler {
        let stop = Arc::new(AtomicBool::new(false));
        let peak = Arc::new(AtomicU64::new(footprint(pid).unwrap_or(0)));
        let thread = {
            let (stop, peak) = (Arc::clone(&stop), Arc::clone(&peak));
            thread::spawn(move || {
                while !stop.load(Ordering::SeqCst) {
                    if let Some(now) = footprint(pid) {
                        peak.fetch_max(now, Ordering::SeqCst);
                    }
                    thread::sleep(Duration::from_millis(10));
                }
            })
        };
        Sampler {
            stop,
            peak,
            thread: Some(thread),
        }
    }

    fn finish(mut self) -> u64 {
        self.stop.store(true, Ordering::SeqCst);
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
        self.peak.load(Ordering::SeqCst)
    }
}

/// A JSON request body of `BODY_BYTES` bytes whose model leaves for Anthropic.
fn write_body(dir: &Path) -> PathBuf {
    let path = dir.join("body.json");
    let head = br#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":""#;
    let tail = br#""}]}"#;
    let mut file = File::create(&path).expect("create the body file");
    file.write_all(head).expect("write head");
    let filler = vec![b'x'; 64 * 1024];
    let mut left = BODY_BYTES - head.len() - tail.len();
    while left > 0 {
        let take = left.min(filler.len());
        file.write_all(&filler[..take]).expect("write filler");
        left -= take;
    }
    file.write_all(tail).expect("write tail");
    path
}

struct Fixture {
    upstream: FakeUpstream,
    daemon: Daemon,
    body: PathBuf,
    ca: PathBuf,
}

fn fixture(plan: impl Fn(usize, usize) -> Step + Send + Sync + 'static) -> Fixture {
    let upstream = tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(plan));
    let daemon = Daemon::start(&[], &[("HUDDLE_ROUTER_ANTHROPIC_URL", upstream.url())]);
    let ca_dir = daemon.home.root().join(".local/share/huddle/ca");
    load_or_create(&ca_dir, SystemTime::now()).expect("create CA");
    let body = write_body(daemon.home.root());
    Fixture {
        upstream,
        daemon,
        body,
        ca: ca_cert_path(&ca_dir),
    }
}

/// `curl` posting `file` through the router's CONNECT; answers the status code it saw.
fn post_through_proxy(port: u16, ca: &Path, file: &Path) -> std::process::Child {
    Command::new("/usr/bin/curl")
        .args([
            "-sS",
            "--max-time",
            "60",
            "--proxy",
            &format!("http://127.0.0.1:{port}"),
            "--cacert",
            ca.to_str().expect("utf8 path"),
            "-H",
            "content-type: application/json",
            "-H",
            "x-api-key: client-key",
            "-H",
            "Expect:",
            "--data-binary",
            &format!("@{}", file.to_str().expect("utf8 path")),
            "-o",
            "/dev/null",
            "-w",
            "%{http_code}",
            "https://api.anthropic.com/v1/messages",
        ])
        .env_clear()
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("run curl")
}

/// Runs `PARALLEL` posts at once and answers each one's status code text and stderr.
fn post_in_parallel(f: &Fixture) -> Vec<(String, String)> {
    let children: Vec<_> = (0..PARALLEL)
        .map(|_| post_through_proxy(f.daemon.port, &f.ca, &f.body))
        .collect();
    children
        .into_iter()
        .map(|child| {
            let out = child.wait_with_output().expect("curl output");
            (
                String::from_utf8_lossy(&out.stdout).into_owned(),
                String::from_utf8_lossy(&out.stderr).into_owned(),
            )
        })
        .collect()
}

/// One small post: it brings the TLS state, the upstream pool and the allocator to the level an
/// idle router that has served a request sits at.
fn warm_up(f: &Fixture) {
    let small = f.daemon.home.root().join("small.json");
    fs::write(
        &small,
        br#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}"#,
    )
    .expect("write the small body");
    let child = post_through_proxy(f.daemon.port, &f.ca, &small);
    let out = child.wait_with_output().expect("curl output");
    assert_eq!(String::from_utf8_lossy(&out.stdout), "200", "warm-up");
}

fn usage_stream() -> Step {
    Step::sse(
        vec![
            "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n".to_string(),
            "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":3}}\n\n".to_string(),
            "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n".to_string(),
        ],
        Duration::from_millis(0),
    )
}

fn mib(bytes: u64) -> f64 {
    bytes as f64 / (1024.0 * 1024.0)
}

#[test]
fn l6_eight_parallel_20mb_bodies_grow_the_footprint_under_32_mib() {
    let f = fixture(|_, _| Step::drain_then(usage_stream()));
    warm_up(&f);
    let pid = worker_pid(&f.daemon);
    let idle = footprint(pid).expect("read the idle footprint");
    let sampler = Sampler::start(pid);

    let replies = post_in_parallel(&f);

    let peak = sampler.finish();
    for (status, stderr) in &replies {
        assert_eq!(status, "200", "stderr: {stderr}");
    }
    let growth = peak.saturating_sub(idle);
    eprintln!(
        "l6 footprint: idle {:.1} MiB, peak {:.1} MiB, growth {:.1} MiB (limit {:.0} MiB)",
        mib(idle),
        mib(peak),
        mib(growth),
        mib(LIMIT)
    );
    assert!(
        f.upstream.received_bytes() >= PARALLEL * BODY_BYTES,
        "the upstream drained {} bytes",
        f.upstream.received_bytes()
    );
    assert!(
        growth < LIMIT,
        "the worker grew by {:.1} MiB (limit {:.0} MiB)",
        mib(growth),
        mib(LIMIT)
    );
}

#[test]
fn l6_an_upstream_that_answers_early_and_ignores_the_body_ends_every_upload() {
    let seen = AtomicUsize::new(0);
    let f = fixture(move |_, _| {
        if seen.fetch_add(1, Ordering::SeqCst) == 0 {
            Step::drain_then(usage_stream())
        } else {
            Step::answer_then_ignore(
                400,
                br#"{"type":"error","error":{"type":"invalid_request_error","message":"no"}}"#,
            )
        }
    });
    warm_up(&f);
    let pid = worker_pid(&f.daemon);
    let idle = footprint(pid).expect("read the idle footprint");
    let sampler = Sampler::start(pid);

    let replies = post_in_parallel(&f);

    let peak = sampler.finish();
    for (status, stderr) in &replies {
        assert_eq!(status, "400", "stderr: {stderr}");
    }
    let growth = peak.saturating_sub(idle);
    eprintln!("l6 early-answer growth {:.1} MiB", mib(growth));
    assert!(
        growth < LIMIT,
        "the worker grew by {:.1} MiB (limit {:.0} MiB)",
        mib(growth),
        mib(LIMIT)
    );
}
