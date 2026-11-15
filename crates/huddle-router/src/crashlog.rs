//! huddle-router: crashlog.

use std::any::Any;
use std::fs;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, SyncSender, TrySendError, sync_channel};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use crate::clock;

/// The largest `router.log` in bytes before the next line rotates it to `router.log.1`.
pub const ROUTER_LOG_LIMIT: u64 = 4 * 1024 * 1024;

/// The largest `router-events.log` in bytes before the next line rotates it.
pub const EVENTS_LOG_LIMIT: u64 = 1024 * 1024;

/// Rotated files kept beside each log: `.1` is the newest, the one past `.5` is deleted.
pub const LOG_KEEP: usize = 5;

/// The prefix of every event line; a request line starts with its HTTP method.
const EVENT_PREFIX: &str = "huddle-router:";

/// One log line, handed to the sink the caller chose.
pub type Logger = Arc<dyn Fn(String) + Send + Sync>;

/// The router's two logs in `state_dir`: every line goes to `router.log`, and each event line
/// (start, exit, handover, path failing, probe, recovered, reload, restart reason, crash) also
/// goes to `router-events.log`, which request lines never fill.
///
/// A request line is queued and written later. An event line returns once both files hold it
/// (or after `FLUSH_WAIT`), so the exit and crash lines reach the disk before the process ends.
/// Dropping the logger writes what is queued.
pub fn router_logs(state_dir: &Path) -> Logger {
    let all = LineWriter::spawn(&state_dir.join("router.log"), ROUTER_LOG_LIMIT);
    let events = LineWriter::spawn(&state_dir.join("router-events.log"), EVENTS_LOG_LIMIT);
    Arc::new(move |line: String| {
        if is_event(&line) {
            events.line(line.clone());
            all.line(line);
            events.flush();
            all.flush();
        } else {
            all.line(line);
        }
    })
}

/// Whether `line` (without its timestamp) is an event line.
pub fn is_event(line: &str) -> bool {
    line.starts_with(EVENT_PREFIX)
}

/// Appends `<ISO ts> <line>\n` to `path` (0644) from one writer thread. Before the append that
/// would pass `limit` the writer rotates `path` to `<path>.1`, shifting older files up and
/// deleting the one past `LOG_KEEP`. The size is read from the disk before a rotation and the
/// rotation runs under an exclusive lock on `<path>.lock`, so two routers writing the same
/// file during a handover rotate it once and lose no line. Never fails: a full disk costs the
/// line. Does not recreate a removed directory. Dropping the logger writes what is queued.
pub fn rotating_log(path: &Path, limit: u64) -> Logger {
    let writer = LineWriter::spawn(path, limit);
    Arc::new(move |line: String| writer.line(line))
}

/// Lines waiting for the writer thread; past this a line is dropped and counted.
const QUEUE: usize = 4096;

/// The longest an event line waits for the writer to catch up.
const FLUSH_WAIT: Duration = Duration::from_secs(2);

enum Message {
    Line(String),
    Flush(SyncSender<()>),
}

/// One log file and the thread that appends to it.
struct LineWriter {
    tx: Option<SyncSender<Message>>,
    dropped: Arc<AtomicU64>,
    thread: Option<JoinHandle<()>>,
}

impl LineWriter {
    fn spawn(path: &Path, limit: u64) -> LineWriter {
        Self::start(path, limit, QUEUE, None)
    }

    /// A writer that waits for `gate` to fire or drop before it handles its first message.
    #[cfg(test)]
    fn spawn_gated(path: &Path, limit: u64, queue: usize, gate: Receiver<()>) -> LineWriter {
        Self::start(path, limit, queue, Some(gate))
    }

    fn start(path: &Path, limit: u64, queue: usize, gate: Option<Receiver<()>>) -> LineWriter {
        let (tx, rx) = sync_channel(queue);
        let dropped = Arc::new(AtomicU64::new(0));
        let lost = Arc::clone(&dropped);
        let path = path.to_path_buf();
        let thread = thread::Builder::new()
            .name("huddle-log".to_string())
            .spawn(move || {
                if let Some(gate) = gate {
                    let _ = gate.recv();
                }
                run(&mut Sink::new(path, limit), &rx, &lost);
            })
            .ok();
        LineWriter {
            tx: Some(tx),
            dropped,
            thread,
        }
    }

    /// Queues a line without blocking. A full queue drops the line and counts it.
    fn line(&self, line: String) {
        let sent = match &self.tx {
            Some(tx) => tx.try_send(Message::Line(line)).is_ok(),
            None => false,
        };
        if !sent {
            self.dropped.fetch_add(1, Ordering::Relaxed);
        }
    }

    /// Waits until every line queued so far is written, for at most `FLUSH_WAIT`.
    fn flush(&self) {
        let Some(tx) = &self.tx else {
            return;
        };
        let (ack, done) = sync_channel(1);
        let deadline = Instant::now() + FLUSH_WAIT;
        let mut message = Message::Flush(ack);
        loop {
            match tx.try_send(message) {
                Ok(()) => {
                    let _ = done.recv_timeout(deadline.saturating_duration_since(Instant::now()));
                    return;
                }
                Err(TrySendError::Full(back)) if Instant::now() < deadline => {
                    message = back;
                    thread::sleep(Duration::from_millis(1));
                }
                Err(_) => return,
            }
        }
    }

    #[cfg(test)]
    fn dropped(&self) -> u64 {
        self.dropped.load(Ordering::Relaxed)
    }
}

impl Drop for LineWriter {
    /// Writes what is queued and waits for the thread.
    fn drop(&mut self) {
        self.tx = None;
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

/// The writer thread: appends each line, and says how many lines the queue dropped once it
/// has caught up.
fn run(sink: &mut Sink, rx: &Receiver<Message>, lost: &AtomicU64) {
    loop {
        let next = match rx.try_recv() {
            Ok(message) => Some(message),
            Err(_) => {
                let count = lost.swap(0, Ordering::Relaxed);
                if count > 0 {
                    sink.append(&format!("huddle-router: log: dropped {count} lines"));
                }
                rx.recv().ok()
            }
        };
        match next {
            Some(Message::Line(line)) => sink.append(&line),
            Some(Message::Flush(ack)) => {
                let _ = ack.send(());
            }
            None => return,
        }
    }
}

/// The open log file and its byte count.
struct Sink {
    path: PathBuf,
    limit: u64,
    file: Option<fs::File>,
    size: u64,
}

impl Sink {
    fn new(path: PathBuf, limit: u64) -> Sink {
        Sink {
            path,
            limit,
            file: None,
            size: 0,
        }
    }

    fn append(&mut self, line: &str) {
        let text = format!("{} {}\n", clock::iso_utc_ms(clock::now_ms()), line);
        let incoming = text.len() as u64;
        if self.file.is_some() && self.size + incoming > self.limit {
            rotate(&self.path, self.limit, incoming);
            self.file = None;
        }
        if self.file.is_none() {
            self.file = open_append(&self.path);
            self.size = size_of(&self.path);
            if self.file.is_some() && self.size + incoming > self.limit {
                rotate(&self.path, self.limit, incoming);
                self.file = open_append(&self.path);
                self.size = size_of(&self.path);
            }
        }
        if let Some(file) = &mut self.file
            && file.write_all(text.as_bytes()).is_ok()
        {
            self.size += incoming;
        }
    }
}

fn open_append(path: &Path) -> Option<fs::File> {
    fs::OpenOptions::new()
        .append(true)
        .create(true)
        .mode(huddle_config::files::FILE_MODE)
        .open(path)
        .ok()
}

fn size_of(path: &Path) -> u64 {
    fs::metadata(path).map(|meta| meta.len()).unwrap_or(0)
}

/// Rotates `path` unless a writer holding the lock before us already did.
fn rotate(path: &Path, limit: u64, incoming: u64) {
    let lock = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .mode(huddle_config::files::FILE_MODE)
        .open(sibling(path, ".lock"));
    let Ok(lock) = lock else {
        return;
    };
    if lock.lock().is_err() {
        return;
    }
    if size_of(path) + incoming > limit {
        let _ = fs::remove_file(rolled_path(path, LOG_KEEP));
        for number in (1..LOG_KEEP).rev() {
            let _ = fs::rename(rolled_path(path, number), rolled_path(path, number + 1));
        }
        let _ = fs::rename(path, rolled_path(path, 1));
    }
}

/// `router.log.<number>` beside `path`.
fn rolled_path(path: &Path, number: usize) -> PathBuf {
    sibling(path, &format!(".{number}"))
}

fn sibling(path: &Path, suffix: &str) -> PathBuf {
    let mut name = path.as_os_str().to_os_string();
    name.push(suffix);
    PathBuf::from(name)
}

/// Installs a panic hook that writes one crash line through `log`, then returns; the process
/// ends on its own.
pub fn install_crash_hook(log: &Logger) {
    let log = Arc::clone(log);
    std::panic::set_hook(Box::new(move |info| {
        let (file, line) = match info.location() {
            Some(place) => (place.file().to_string(), place.line()),
            None => ("<unknown>".to_string(), 0),
        };
        log(crash_line(&payload_message(info.payload()), &file, line));
    }));
}

/// The crash line for a message and a location.
pub fn crash_line(message: &str, file: &str, line: u32) -> String {
    format!("huddle-router: crash: {message} at {file}:{line}")
}

/// The panic's message: a `&str` or `String` payload as itself, anything else unnamed.
fn payload_message(payload: &(dyn Any + Send)) -> String {
    if let Some(text) = payload.downcast_ref::<&str>() {
        (*text).to_string()
    } else if let Some(text) = payload.downcast_ref::<String>() {
        text.clone()
    } else {
        "<unknown>".to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::{fs, os::unix::fs::PermissionsExt};

    #[test]
    fn a_line_lands_with_an_iso_timestamp_and_a_private_mode() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, ROUTER_LOG_LIMIT);
        log("hello".to_string());
        drop(log);
        let text = fs::read_to_string(&path).expect("read");
        assert!(text.ends_with(" hello\n"), "{text}");
        assert_eq!(
            text.split(' ').next().expect("ts").len(),
            "2026-11-09T10:15:30.123Z".len()
        );
        assert_eq!(
            fs::metadata(&path).expect("stat").permissions().mode() & 0o777,
            0o644
        );
    }

    #[test]
    fn a_line_past_the_limit_rotates_first() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, 100);
        log("a".repeat(50));
        log("b".repeat(50));
        drop(log);
        let rolled = fs::read_to_string(home.root().join("router.log.1")).expect("rolled");
        assert!(rolled.contains(&"a".repeat(50)));
        let current = fs::read_to_string(&path).expect("current");
        assert!(current.contains(&"b".repeat(50)));
        assert!(!current.contains(&"a".repeat(50)));
    }

    #[test]
    fn a_full_disk_costs_the_line_only() {
        let home = TempHome::new();
        let dir = home.root().join("logdir");
        fs::create_dir(&dir).expect("mkdir");
        let log = rotating_log(&dir, 100);
        log("line".to_string());
    }

    #[test]
    fn the_crash_line_names_message_and_place() {
        assert_eq!(
            crash_line("boom", "src/x.rs", 7),
            "huddle-router: crash: boom at src/x.rs:7"
        );
    }

    #[test]
    fn the_hook_writes_a_crash_line_before_the_panic_unwinds() {
        let lines = Arc::new(std::sync::Mutex::new(Vec::new()));
        let sink = Arc::clone(&lines);
        let log: Logger = Arc::new(move |line: String| sink.lock().expect("lines").push(line));
        let previous = std::panic::take_hook();
        install_crash_hook(&log);
        let result = std::panic::catch_unwind(|| panic!("boom"));
        std::panic::set_hook(previous);
        assert!(result.is_err());
        let text = lines.lock().expect("lines").join("\n");
        assert!(text.contains("huddle-router: crash: boom at "), "{text}");
        assert!(text.contains("crashlog.rs:"), "{text}");
    }

    #[test]
    fn k17_event_lines_go_to_the_events_log_and_requests_do_not() {
        let home = TempHome::new();
        let log = router_logs(home.root());
        log("POST /v1/messages model=- -> anthropic 200".to_string());
        log("huddle-router: Anthropic path failing (ETIMEDOUT)".to_string());
        drop(log);
        let events = fs::read_to_string(home.root().join("router-events.log")).unwrap_or_default();
        assert!(events.contains("Anthropic path failing"), "{events}");
        assert!(!events.contains("POST /v1/messages"), "{events}");
        let requests = fs::read_to_string(home.root().join("router.log")).expect("router.log");
        assert!(requests.contains("POST /v1/messages"), "{requests}");
        assert!(requests.contains("Anthropic path failing"), "{requests}");
    }

    #[test]
    fn k17_router_log_keeps_five_old_files() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, 100);
        for round in 0..9 {
            log(format!("{round}{}", "x".repeat(60)));
        }
        drop(log);
        let first_of = |name: &str| {
            fs::read_to_string(home.root().join(name))
                .unwrap_or_default()
                .split(' ')
                .nth(1)
                .unwrap_or("")
                .chars()
                .next()
        };
        assert_eq!(first_of("router.log"), Some('8'));
        assert_eq!(first_of("router.log.1"), Some('7'));
        assert_eq!(first_of("router.log.5"), Some('3'));
        assert!(!home.root().join("router.log.6").exists());
    }

    /// A writer that starts only when the returned sender fires or drops.
    fn held_back(
        path: &Path,
        limit: u64,
        queue: usize,
    ) -> (LineWriter, std::sync::mpsc::Sender<()>) {
        let (release, gate) = std::sync::mpsc::channel();
        (LineWriter::spawn_gated(path, limit, queue, gate), release)
    }

    #[test]
    fn l12_a_log_call_does_not_touch_the_file_on_the_calling_thread() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let (writer, release) = held_back(&path, ROUTER_LOG_LIMIT, 16);
        writer.line("held".to_string());
        assert!(!path.exists(), "the caller wrote the file");
        release.send(()).expect("release");
        drop(writer);
        let text = fs::read_to_string(&path).expect("read");
        assert!(text.ends_with(" held\n"), "{text}");
    }

    #[test]
    fn l12_the_last_lines_are_flushed_on_drop() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let log = rotating_log(&path, ROUTER_LOG_LIMIT);
        for round in 0..200 {
            log(format!("line {round}"));
        }
        drop(log);
        let text = fs::read_to_string(&path).expect("read");
        assert_eq!(text.lines().count(), 200);
        assert!(text.ends_with(" line 199\n"), "{text}");
    }

    #[test]
    fn l12_an_event_line_is_on_disk_when_the_call_returns() {
        let home = TempHome::new();
        let log = router_logs(home.root());
        log("huddle-router: exit: signal SIGTERM".to_string());
        for name in ["router.log", "router-events.log"] {
            let text = fs::read_to_string(home.root().join(name)).expect(name);
            assert!(text.contains("exit: signal SIGTERM"), "{name}: {text}");
        }
    }

    #[test]
    fn l12_two_writers_on_one_file_lose_no_line_across_a_rotation() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let logs: Vec<_> = ["a", "b"]
            .into_iter()
            .map(|tag| (tag, rotating_log(&path, 2000)))
            .collect();
        let threads: Vec<_> = logs
            .into_iter()
            .map(|(tag, log)| {
                std::thread::spawn(move || {
                    for round in 0..40 {
                        log(format!("{tag}{round:02}"));
                    }
                })
            })
            .collect();
        for thread in threads {
            thread.join().expect("writer");
        }
        let mut all = String::new();
        for name in [
            "router.log",
            "router.log.1",
            "router.log.2",
            "router.log.3",
            "router.log.4",
            "router.log.5",
        ] {
            all.push_str(&fs::read_to_string(home.root().join(name)).unwrap_or_default());
        }
        for tag in ["a", "b"] {
            for round in 0..40 {
                let want = format!(" {tag}{round:02}\n");
                assert_eq!(all.matches(&want).count(), 1, "{want} in {all}");
            }
        }
    }

    #[test]
    fn k17_two_writers_never_lose_a_line_across_a_rotation() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let logs: Vec<_> = ["a", "b"]
            .into_iter()
            .map(|tag| (tag, rotating_log(&path, 2000)))
            .collect();
        let threads: Vec<_> = logs
            .into_iter()
            .map(|(tag, log)| {
                std::thread::spawn(move || {
                    for round in 0..40 {
                        log(format!("{tag}{round:02}"));
                    }
                })
            })
            .collect();
        for thread in threads {
            thread.join().expect("writer");
        }
        let mut all = String::new();
        for name in [
            "router.log",
            "router.log.1",
            "router.log.2",
            "router.log.3",
            "router.log.4",
            "router.log.5",
        ] {
            all.push_str(&fs::read_to_string(home.root().join(name)).unwrap_or_default());
        }
        for tag in ["a", "b"] {
            for round in 0..40 {
                let want = format!(" {tag}{round:02}\n");
                assert_eq!(all.matches(&want).count(), 1, "{want} in {all}");
            }
        }
    }

    #[test]
    fn l12_a_removed_directory_is_not_recreated() {
        let home = TempHome::new();
        let dir = home.root().join("gone");
        let log = rotating_log(&dir.join("router.log"), 100);
        for round in 0..10 {
            log(format!("line {round}"));
        }
        drop(log);
        assert!(!dir.exists());
    }

    #[test]
    fn l12_a_full_queue_drops_lines_and_the_writer_says_how_many() {
        let home = TempHome::new();
        let path = home.root().join("router.log");
        let (writer, release) = held_back(&path, ROUTER_LOG_LIMIT, 2);
        for round in 0..5 {
            writer.line(format!("line {round}"));
        }
        assert_eq!(writer.dropped(), 3);
        release.send(()).expect("release");
        drop(writer);
        let text = fs::read_to_string(&path).expect("read");
        assert!(
            text.contains(" line 0\n") && text.contains(" line 1\n"),
            "{text}"
        );
        assert!(!text.contains(" line 2\n"), "{text}");
        assert!(
            text.contains("huddle-router: log: dropped 3 lines"),
            "{text}"
        );
    }
}
