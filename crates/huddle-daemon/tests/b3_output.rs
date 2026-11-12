//! B3: the daemon's stdout and stderr reach the events log and the dashboard's events.

use std::fs;
use std::io::Write;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::sync::{Arc, Mutex, OnceLock};

use huddle_daemon::output::{Saved, Sink, pump, redact, redirect_onto};
use huddle_router::crashlog::{Logger, router_logs};
use huddle_testkit::temp_home::TempHome;

static FDS: Mutex<()> = Mutex::new(());

fn write_fd(fd: i32, bytes: &[u8]) {
    // SAFETY: `bytes` is a valid buffer of its length.
    let written = unsafe { libc::write(fd, bytes.as_ptr().cast(), bytes.len()) };
    assert_eq!(written, bytes.len() as isize);
}

fn pipe() -> (OwnedFd, fs::File) {
    let mut fds = [0; 2];
    // SAFETY: `fds` holds room for the two descriptors pipe fills in.
    assert_eq!(unsafe { libc::pipe(fds.as_mut_ptr()) }, 0);
    // SAFETY: both descriptors are fresh and owned here.
    unsafe { (OwnedFd::from_raw_fd(fds[0]), fs::File::from_raw_fd(fds[1])) }
}

fn collecting() -> (Logger, Arc<Mutex<Vec<String>>>) {
    let lines: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = lines.clone();
    let log: Logger = Arc::new(move |line| sink.lock().expect("lines").push(line));
    (log, lines)
}

fn run_pump(input: &[u8]) -> Vec<String> {
    let (reader, mut writer) = pipe();
    let (log, lines) = collecting();
    let sink: Sink = Arc::new(OnceLock::new());
    let handle = std::thread::spawn(move || pump(reader, log, sink, Saved::none()));
    writer.write_all(input).expect("write");
    drop(writer);
    handle.join().expect("pump");
    let guard = lines.lock().expect("lines");
    guard.clone()
}

#[test]
fn b3_a_line_on_stderr_lands_in_the_events_log() {
    let _fds = FDS.lock().unwrap_or_else(|e| e.into_inner());
    let home = TempHome::new();
    let log = router_logs(home.root());
    let (reader, mut writer) = pipe();
    let sink: Sink = Arc::new(OnceLock::new());
    let handle = {
        let sink = sink.clone();
        std::thread::spawn(move || pump(reader, log, sink, Saved::none()))
    };
    writer
        .write_all(b"thread 'x' panicked at y\n")
        .expect("write");
    drop(writer);
    handle.join().expect("pump");
    let events = fs::read_to_string(home.root().join("router-events.log")).unwrap_or_default();
    assert!(
        events.contains("huddle-router: output: thread 'x' panicked at y"),
        "{events}"
    );
    let all = fs::read_to_string(home.root().join("router.log")).unwrap_or_default();
    assert!(all.contains("output: thread 'x' panicked at y"), "{all}");
}

#[test]
fn b3_redirected_descriptors_feed_the_pipe() {
    let _fds = FDS.lock().unwrap_or_else(|e| e.into_inner());
    let spare_out = unsafe { libc::dup(2) };
    let spare_err = unsafe { libc::dup(2) };
    assert!(spare_out >= 0 && spare_err >= 0);
    let (reader, saved) = redirect_onto(&[spare_out, spare_err]).expect("redirect");
    write_fd(spare_out, b"from stdout\n");
    write_fd(spare_err, b"from stderr\n");
    // Closing the redirected descriptors ends the pipe; the pump then restores them.
    unsafe {
        libc::close(spare_out);
        libc::close(spare_err);
    }
    let (log, lines) = collecting();
    pump(reader, log, Arc::new(OnceLock::new()), saved);
    unsafe {
        libc::close(spare_out);
        libc::close(spare_err);
    }
    let lines = lines.lock().expect("lines").join("\n");
    assert!(lines.contains("output: from stdout"), "{lines}");
    assert!(lines.contains("output: from stderr"), "{lines}");
}

#[test]
fn b3_output_lines_are_capped_and_rate_limited() {
    let _fds = FDS.lock().unwrap_or_else(|e| e.into_inner());
    let long = "x".repeat(5000);
    let mut input = format!("{long}\n");
    for number in 0..100 {
        input.push_str(&format!("line {number}\n"));
    }
    let lines = run_pump(input.as_bytes());
    let output: Vec<_> = lines
        .iter()
        .filter(|l| l.starts_with("huddle-router: output: "))
        .collect();
    let dropped: Vec<_> = output
        .iter()
        .filter(|l| l.ends_with(" lines dropped"))
        .collect();
    assert_eq!(dropped.len(), 1, "{lines:?}");
    assert!(
        dropped[0].ends_with("output: 81 lines dropped"),
        "{dropped:?}"
    );
    assert_eq!(output.len() - 1, 20, "{lines:?}");
    let first = lines[0]
        .strip_prefix("huddle-router: output: ")
        .expect("prefix");
    assert_eq!(first.len(), 1024, "the first line is cut at 1 KiB");
}

#[test]
fn b3_a_token_in_output_is_redacted() {
    let _fds = FDS.lock().unwrap_or_else(|e| e.into_inner());
    for (line, secret) in [
        (
            "sent Authorization: Bearer abc123SECRET to upstream",
            "abc123SECRET",
        ),
        ("header x-api-key: topsecretvalue set", "topsecretvalue"),
        ("header X-Api-Key=topsecretvalue set", "topsecretvalue"),
        ("key sk-ant-api03-AbCdEf_12345 rejected", "AbCdEf_12345"),
        (r#"{"key":"sk-proj-ZZZ999"}"#, "ZZZ999"),
    ] {
        let redacted = redact(line);
        assert!(!redacted.contains(secret), "{line} -> {redacted}");
    }
    assert_eq!(
        redact("plain message, risk-free task"),
        "plain message, risk-free task"
    );
    let lines = run_pump(b"token Bearer SECRETVALUE9 end\n");
    assert!(!lines.join("\n").contains("SECRETVALUE9"), "{lines:?}");
}

#[test]
fn b3_a_pump_that_stops_gives_the_streams_back() {
    let _fds = FDS.lock().unwrap_or_else(|e| e.into_inner());
    let (target_reader, target_writer) = pipe();
    let spare = target_writer.as_raw_fd();
    let (reader, saved) = redirect_onto(&[spare]).expect("redirect");
    let (log, _lines) = collecting();
    let handle = std::thread::spawn(move || pump(reader, log, Arc::new(OnceLock::new()), saved));
    // SAFETY: closing the redirected descriptor ends the pipe's last write end, so the pump
    // sees the end of its input; the file is forgotten so it does not close it twice.
    unsafe { libc::close(spare) };
    std::mem::forget(target_writer);
    handle.join().expect("pump");
    // SAFETY: the pump's restore made `spare` the original write end again.
    let mut again = unsafe { fs::File::from_raw_fd(spare) };
    again
        .write_all(b"after\n")
        .expect("write after the pump stopped");
    drop(again);
    let mut text = String::new();
    std::io::Read::read_to_string(&mut fs::File::from(target_reader), &mut text).expect("read");
    assert_eq!(text, "after\n");
}
