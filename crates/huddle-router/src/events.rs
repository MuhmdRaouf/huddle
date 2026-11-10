//! huddle-router: events.

use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, SyncSender, TrySendError, sync_channel};
use std::time::{SystemTime, UNIX_EPOCH};

/// How many events wait for the writer before `send` drops instead.
const QUEUE_CAPACITY: usize = 4096;

/// One forwarded request, appended after it completes. Optional fields drop out of the line.
#[derive(Debug, Clone, serde::Serialize, PartialEq)]
pub struct RouteEvent {
    /// When the request completed, as `iso_utc_ms` prints it.
    pub ts: String,
    pub plugin: String,
    pub model: String,
    /// `provider`, `anthropic`, `refused` or `anthropic-pass`.
    pub route: String,
    /// The upstream's status; 499 when the client left before the answer.
    pub status: u16,
    pub latency_ms: u64,
    /// The answer's token counts, when the answer reported them.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<crate::sse::Usage>,
    /// One line on why the upstream answered an error, when it did.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    /// How long the router held the request before its first upstream attempt, microseconds.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub router_us: Option<u64>,
}

/// A process-wide writer: one sender cloned per request, one writer thread.
#[derive(Clone)]
pub struct EventSink {
    sender: SyncSender<RouteEvent>,
    dropped: Arc<AtomicU64>,
}

impl EventSink {
    /// `dir` is `<router_dir>/events`. Spawns the writer thread.
    pub fn start(dir: PathBuf) -> EventSink {
        let (sender, receiver) = sync_channel(QUEUE_CAPACITY);
        let dropped = Arc::new(AtomicU64::new(0));
        let spawned = std::thread::Builder::new()
            .name("route-events".into())
            .spawn(move || write_loop(dir, receiver));
        if let Err(err) = spawned {
            eprintln!("route events: {err}");
        }
        EventSink { sender, dropped }
    }

    /// Never blocks: a full queue drops the event and writes one stderr line per 1000 dropped.
    pub fn send(&self, event: RouteEvent) {
        match self.sender.try_send(event) {
            Ok(()) => {}
            Err(TrySendError::Full(_)) => {
                let dropped = self.dropped.fetch_add(1, Ordering::Relaxed) + 1;
                if dropped.is_multiple_of(1000) {
                    eprintln!("route events: {dropped} dropped");
                }
            }
            Err(TrySendError::Disconnected(_)) => {}
        }
    }
}

/// One line per event; a failure costs one stderr line and nothing else.
fn write_loop(dir: PathBuf, receiver: Receiver<RouteEvent>) {
    for event in receiver {
        if let Err(err) = append(&dir, &event) {
            eprintln!("route events: {err}");
        }
    }
}

/// The line lands on the day its own `ts` names, so a late write still lands on its day.
fn append(dir: &Path, event: &RouteEvent) -> std::io::Result<()> {
    let path = dir.join(format!("{}.jsonl", day_of(&event.ts)));
    dir_0700(dir)?;
    let mut file = std::fs::OpenOptions::new()
        .append(true)
        .create(true)
        .mode(0o600)
        .open(&path)?;
    let mut line =
        serde_json::to_string(event).map_err(|err| std::io::Error::other(err.to_string()))?;
    line.push('\n');
    file.write_all(line.as_bytes())
}

/// Creates the directory 0700; an existing one stays.
fn dir_0700(dir: &Path) -> std::io::Result<()> {
    std::fs::DirBuilder::new()
        .mode(0o700)
        .create(dir)
        .or_else(|err| match err.kind() {
            std::io::ErrorKind::AlreadyExists => Ok(()),
            _ => Err(err),
        })
}

/// The ten date characters of `ts`; today's date when `ts` is not that shape.
fn day_of(ts: &str) -> String {
    ts.get(..10)
        .map(str::to_string)
        .unwrap_or_else(|| utc_date(now_ms()))
}

/// `2026-11-09T10:15:30.123Z` from epoch milliseconds, UTC.
pub fn iso_utc_ms(epoch_ms: i64) -> String {
    let secs = epoch_ms.div_euclid(1_000);
    let (year, month, day) = civil_from_days(secs.div_euclid(86_400));
    let clock = secs.rem_euclid(86_400);
    let (hour, min, sec) = (clock / 3_600, (clock % 3_600) / 60, clock % 60);
    let ms = epoch_ms.rem_euclid(1_000);
    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{min:02}:{sec:02}.{ms:03}Z")
}

/// `2026-11-09` from epoch milliseconds, UTC.
pub fn utc_date(epoch_ms: i64) -> String {
    let secs = epoch_ms.div_euclid(1_000);
    let (year, month, day) = civil_from_days(secs.div_euclid(86_400));
    format!("{year:04}-{month:02}-{day:02}")
}

/// Epoch milliseconds now.
pub fn now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|since| since.as_millis() as i64)
        .unwrap_or(0)
}

/// `(year, month, day)` for a day count from 1970-01-01.
fn civil_from_days(days: i64) -> (i64, u32, u32) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365;
    let year = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let month = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (year + i64::from(month <= 2), month, day)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sse::Usage;
    use huddle_testkit::temp_home::TempHome;
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        thread,
        time::{Duration, Instant},
    };

    fn event(ts: &str) -> RouteEvent {
        RouteEvent {
            ts: ts.to_string(),
            plugin: "zai".into(),
            model: "glm-5.3".into(),
            route: "provider".into(),
            status: 200,
            latency_ms: 123,
            usage: Some(Usage {
                input_tokens: Some(12),
                output_tokens: Some(78),
                ..Usage::default()
            }),
            error: None,
            router_us: Some(1180),
        }
    }

    fn wait_lines(path: &std::path::Path, n: usize) -> String {
        let deadline = Instant::now() + Duration::from_secs(3);
        loop {
            let text = fs::read_to_string(path).unwrap_or_default();
            if text.lines().count() >= n {
                return text;
            }
            assert!(
                Instant::now() < deadline,
                "only {} lines of {n}",
                text.lines().count()
            );
            thread::sleep(Duration::from_millis(10));
        }
    }

    #[test]
    fn one_line_per_event_in_a_private_dated_file() {
        let home = TempHome::new();
        let dir = home.root().join("events");
        let sink = EventSink::start(dir.clone());
        sink.send(event("2026-11-09T10:15:30.123Z"));
        let path = dir.join("2026-11-09.jsonl");
        let text = wait_lines(&path, 1);
        assert_eq!(
            text,
            "{\"ts\":\"2026-11-09T10:15:30.123Z\",\"plugin\":\"zai\",\"model\":\"glm-5.3\",\"route\":\"provider\",\"status\":200,\"latency_ms\":123,\"usage\":{\"input_tokens\":12,\"output_tokens\":78},\"router_us\":1180}\n"
        );
        assert_eq!(
            fs::metadata(&path).expect("stat").permissions().mode() & 0o777,
            0o600
        );
        assert_eq!(
            fs::metadata(&dir).expect("stat").permissions().mode() & 0o777,
            0o700
        );
    }

    #[test]
    fn events_of_two_days_land_in_two_files() {
        let home = TempHome::new();
        let dir = home.root().join("events");
        let sink = EventSink::start(dir.clone());
        sink.send(event("2026-11-08T23:59:59.999Z"));
        sink.send(event("2026-11-09T00:00:00.000Z"));
        wait_lines(&dir.join("2026-11-09.jsonl"), 1);
        wait_lines(&dir.join("2026-11-08.jsonl"), 1);
    }

    #[test]
    fn optional_fields_are_omitted() {
        let event = RouteEvent {
            ts: "t".into(),
            plugin: "p".into(),
            model: "m".into(),
            route: "refused".into(),
            status: 400,
            latency_ms: 1,
            usage: None,
            error: Some("no key".into()),
            router_us: None,
        };
        assert_eq!(
            serde_json::to_string(&event).expect("json"),
            "{\"ts\":\"t\",\"plugin\":\"p\",\"model\":\"m\",\"route\":\"refused\",\"status\":400,\"latency_ms\":1,\"error\":\"no key\"}"
        );
    }

    #[test]
    fn dates_print_like_iso_utc() {
        assert_eq!(iso_utc_ms(1_794_220_530_123), "2026-11-09T10:35:30.123Z");
        assert_eq!(utc_date(1_794_220_530_123), "2026-11-09");
        assert_eq!(iso_utc_ms(0), "1970-01-01T00:00:00.000Z");
        assert_eq!(iso_utc_ms(951_782_400_000), "2000-02-29T00:00:00.000Z");
    }

    #[test]
    fn a_removed_directory_is_recreated_on_the_next_event() {
        let home = TempHome::new();
        let dir = home.root().join("events");
        let sink = EventSink::start(dir.clone());
        sink.send(event("2026-11-09T10:15:30.123Z"));
        let path = dir.join("2026-11-09.jsonl");
        wait_lines(&path, 1);
        fs::remove_dir_all(&dir).expect("rm");
        sink.send(event("2026-11-09T10:15:31.123Z"));
        wait_lines(&path, 1);
    }

    #[test]
    fn a_failed_write_never_panics() {
        let home = TempHome::new();
        let blocker = home.root().join("blocker");
        fs::write(&blocker, "file").expect("write");
        let sink = EventSink::start(blocker.join("events"));
        sink.send(event("2026-11-09T10:15:30.123Z"));
        thread::sleep(Duration::from_millis(30));
    }

    #[test]
    fn sending_never_blocks_even_if_the_queue_is_full() {
        let home = TempHome::new();
        let dir = home.root().join("events");
        let sink = EventSink::start(dir.clone());
        for _ in 0..50_000 {
            sink.send(event("2026-11-09T10:15:30.123Z"));
        }
        // The marker carries its own day, so its file exists only once the writer has taken
        // every event this test sent; nothing is sent after it, so nothing writes into the
        // home once the file is there and TempHome's drop cannot race the writer.
        let marker = dir.join("2030-01-01.jsonl");
        let deadline = Instant::now() + Duration::from_secs(30);
        while !marker.exists() && Instant::now() < deadline {
            sink.send(event("2030-01-01T00:00:00.000Z"));
            thread::sleep(Duration::from_millis(50));
        }
        assert!(marker.exists(), "the writer never drained the queue");
    }
}
