//! huddle-daemon: output. Whatever the daemon prints on stdout or stderr (a library or the
//! runtime) is read from a pipe on its own thread and filed as `output` events: each line is
//! redacted, cut at 1 KiB and admitted at 20 lines per second, and the excess is counted.

use std::fs::File;
use std::io::{ErrorKind, Read};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::sync::{Arc, OnceLock};
use std::time::{Duration, Instant};

use huddle_router::crashlog::Logger;

/// Where output lines go once the router's logger exists.
pub type Sink = Arc<OnceLock<Logger>>;

/// The most bytes of one output line that are kept.
pub const LINE_MAX: usize = 1024;

/// The most output lines filed per window.
pub const LINES_PER_WINDOW: u32 = 20;

/// The length of one rate window.
const WINDOW: Duration = Duration::from_secs(1);

const MASK: &str = "[redacted]";

/// Hides the secrets a line can carry: the value after `Bearer` or `x-api-key`, and any
/// `sk-` key.
pub fn redact(line: &str) -> String {
    mask_sk_keys(&mask_header_values(line))
}

/// Masks the token after `Bearer` and the value of an `x-api-key` header, in `name: value`,
/// `name=value`, `"name":"value"` and `name value` shapes.
fn mask_header_values(line: &str) -> String {
    let mut masking = false;
    let tokens: Vec<String> = line
        .split(' ')
        .map(|token| {
            let lower = token.to_ascii_lowercase();
            if masking {
                if token.chars().all(is_separator) {
                    return token.to_string();
                }
                masking = false;
                return MASK.to_string();
            }
            if let Some(at) = lower.find("x-api-key") {
                let name_end = at + "x-api-key".len();
                let value_at = token[name_end..]
                    .find(|c| !is_separator(c))
                    .map(|offset| name_end + offset);
                return match value_at {
                    Some(value_at) => format!("{}{MASK}", &token[..value_at]),
                    None => {
                        masking = true;
                        token.to_string()
                    }
                };
            }
            if lower.trim_matches(|c: char| !c.is_ascii_alphabetic()) == "bearer" {
                masking = true;
            }
            token.to_string()
        })
        .collect();
    tokens.join(" ")
}

fn is_separator(c: char) -> bool {
    matches!(c, ':' | '=' | '"' | '\'')
}

/// Masks each `sk-` key that starts a word: `sk-` and the run of key characters after it.
fn mask_sk_keys(line: &str) -> String {
    let mut out = String::with_capacity(line.len());
    let mut rest = line;
    let mut before: Option<char> = None;
    while let Some(at) = rest.find("sk-") {
        let previous = rest[..at].chars().next_back().or(before);
        out.push_str(&rest[..at]);
        let after = &rest[at + 3..];
        if previous.is_some_and(|c| c.is_ascii_alphanumeric()) {
            out.push_str("sk-");
        } else {
            let run = after
                .find(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '-'))
                .unwrap_or(after.len());
            out.push_str("sk-");
            out.push_str(MASK);
            before = after[..run].chars().next_back().or(Some('-'));
            rest = &after[run..];
            continue;
        }
        before = Some('-');
        rest = after;
    }
    out.push_str(rest);
    out
}

/// The cap and the rate window. The core takes every time as a parameter.
struct Limiter {
    window_start: Option<Instant>,
    admitted: u32,
    dropped: u32,
}

impl Limiter {
    fn new() -> Self {
        Limiter {
            window_start: None,
            admitted: 0,
            dropped: 0,
        }
    }

    /// Whether a line arriving at `now` is filed. A window that has run out is closed first.
    fn admit(&mut self, now: Instant) -> bool {
        self.close_if_over(now);
        if self.window_start.is_none() {
            self.window_start = Some(now);
        }
        if self.admitted < LINES_PER_WINDOW {
            self.admitted += 1;
            true
        } else {
            self.dropped += 1;
            false
        }
    }

    /// Closes the window at `now` when it is over; returns the lines it dropped.
    fn close_if_over(&mut self, now: Instant) -> u32 {
        match self.window_start {
            Some(start) if now.duration_since(start) >= WINDOW => self.close(),
            _ => 0,
        }
    }

    /// Ends the window; returns the lines it dropped.
    fn close(&mut self) -> u32 {
        self.window_start = None;
        self.admitted = 0;
        std::mem::take(&mut self.dropped)
    }
}

/// The streams as they were before a redirect, so the word's own last message can reach the
/// caller instead of the pipe.
#[derive(Clone)]
pub struct Saved(Arc<Vec<(RawFd, OwnedFd)>>);

impl Saved {
    /// Nothing saved: `restore` does nothing.
    pub fn none() -> Self {
        Saved(Arc::default())
    }

    /// Points each redirected descriptor back at its original stream.
    pub fn restore(&self) {
        for (fd, original) in self.0.iter() {
            // SAFETY: both descriptors are open; dup2 replaces `fd` with the original.
            unsafe { libc::dup2(original.as_raw_fd(), *fd) };
        }
    }
}

/// Points each of `fds` at a new pipe and returns the pipe's read end and the originals.
pub fn redirect_onto(fds: &[RawFd]) -> std::io::Result<(OwnedFd, Saved)> {
    let mut ends = [0 as RawFd; 2];
    // SAFETY: `ends` has room for the two descriptors pipe fills in.
    if unsafe { libc::pipe(ends.as_mut_ptr()) } != 0 {
        return Err(std::io::Error::last_os_error());
    }
    // SAFETY: pipe returned two fresh descriptors that nothing else owns.
    let (reader, writer) =
        unsafe { (OwnedFd::from_raw_fd(ends[0]), OwnedFd::from_raw_fd(ends[1])) };
    let mut saved = Vec::new();
    for fd in fds {
        // SAFETY: `fd` is open; dup returns a new descriptor that is owned here.
        let original = unsafe { libc::dup(*fd) };
        if original < 0 {
            return Err(std::io::Error::last_os_error());
        }
        // SAFETY: dup returned a fresh descriptor.
        saved.push((*fd, unsafe { OwnedFd::from_raw_fd(original) }));
        // SAFETY: both descriptors are open; dup2 replaces `fd` with the pipe's write end.
        if unsafe { libc::dup2(writer.as_raw_fd(), *fd) } < 0 {
            return Err(std::io::Error::last_os_error());
        }
    }
    Ok((reader, Saved(Arc::new(saved))))
}

/// Redirects stdout and stderr into a pipe and reads it on a thread of its own, unless one of
/// them is a terminal: a router run by hand keeps its output on screen. Lines go to `sink` once
/// it is set, and to `fallback` before. The result is `None` when nothing was redirected.
pub fn capture(fallback: Logger, sink: Sink) -> std::io::Result<Option<Saved>> {
    // SAFETY: isatty only inspects the descriptor.
    if unsafe { libc::isatty(1) == 1 || libc::isatty(2) == 1 } {
        return Ok(None);
    }
    let (reader, saved) = redirect_onto(&[1, 2])?;
    let for_pump = saved.clone();
    std::thread::Builder::new()
        .name("huddle-output".into())
        .spawn(move || pump(reader, fallback, sink, for_pump))?;
    Ok(Some(saved))
}

/// Reads `reader` to its end and files each line through the sink, or `fallback` while the
/// sink is empty. A report of dropped lines follows the window that dropped them. When the
/// reading ends, the original streams are put back, so a later print never meets a closed pipe.
pub fn pump(reader: OwnedFd, fallback: Logger, sink: Sink, saved: Saved) {
    let fd = reader.as_raw_fd();
    let mut file = File::from(reader);
    let file_line = |text: String| match sink.get() {
        Some(log) => log(text),
        None => fallback(text),
    };
    drain(
        &mut file,
        &|| readable_within(fd, WINDOW),
        &file_line,
        &saved,
    );
}

/// The read loop behind `pump`. `ready` says whether `reader` has something to read within a
/// window. An interrupted read is retried; the end of input or any other error ends the loop.
fn drain(
    reader: &mut dyn Read,
    ready: &dyn Fn() -> bool,
    file_line: &dyn Fn(String),
    saved: &Saved,
) {
    let mut limiter = Limiter::new();
    let mut line: Vec<u8> = Vec::new();
    let mut buffer = [0u8; 4096];
    loop {
        if !ready() {
            report(limiter.close_if_over(Instant::now()), file_line);
            continue;
        }
        let read = match reader.read(&mut buffer) {
            Err(error) if error.kind() == ErrorKind::Interrupted => continue,
            Ok(0) | Err(_) => break,
            Ok(read) => read,
        };
        for &byte in &buffer[..read] {
            if byte != b'\n' {
                if line.len() < LINE_MAX {
                    line.push(byte);
                }
                continue;
            }
            file_one(&mut line, &mut limiter, file_line);
        }
    }
    saved.restore();
    file_one(&mut line, &mut limiter, file_line);
    report(limiter.close(), file_line);
}

/// Files the finished `line` when the limiter admits it, and empties it.
fn file_one(line: &mut Vec<u8>, limiter: &mut Limiter, file_line: &dyn Fn(String)) {
    let text = String::from_utf8_lossy(line)
        .trim_end_matches('\r')
        .to_string();
    line.clear();
    if text.trim().is_empty() {
        return;
    }
    let now = Instant::now();
    report(limiter.close_if_over(now), file_line);
    if limiter.admit(now) {
        file_line(format!("huddle-router: output: {}", cut(&redact(&text))));
    }
}

fn report(dropped: u32, file_line: &dyn Fn(String)) {
    if dropped > 0 {
        file_line(format!("huddle-router: output: {dropped} lines dropped"));
    }
}

/// `text` cut at `LINE_MAX` bytes on a character boundary.
fn cut(text: &str) -> &str {
    let mut end = text.len().min(LINE_MAX);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    &text[..end]
}

/// Whether `fd` has data or a hangup within `wait`.
fn readable_within(fd: RawFd, wait: Duration) -> bool {
    let mut poll = libc::pollfd {
        fd,
        events: libc::POLLIN,
        revents: 0,
    };
    // SAFETY: `poll` is one valid pollfd and the count says so.
    let ready = unsafe { libc::poll(&mut poll, 1, wait.as_millis() as libc::c_int) };
    ready != 0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_limiter_admits_twenty_per_window_and_counts_the_rest() {
        let start = Instant::now();
        let mut limiter = Limiter::new();
        let admitted = (0..30).filter(|_| limiter.admit(start)).count();
        assert_eq!(admitted, 20);
        assert_eq!(limiter.close_if_over(start + Duration::from_millis(999)), 0);
        assert_eq!(limiter.close_if_over(start + WINDOW), 10);
        assert!(limiter.admit(start + WINDOW));
    }

    #[test]
    fn b3_an_interrupted_read_keeps_reading() {
        struct Flaky(u8);
        impl Read for Flaky {
            fn read(&mut self, buffer: &mut [u8]) -> std::io::Result<usize> {
                self.0 += 1;
                match self.0 {
                    1 => Err(ErrorKind::Interrupted.into()),
                    2 => {
                        buffer[..6].copy_from_slice(b"after\n");
                        Ok(6)
                    }
                    _ => Ok(0),
                }
            }
        }
        let lines = std::sync::Mutex::new(Vec::new());
        let file_line = |text: String| lines.lock().expect("lines").push(text);
        drain(&mut Flaky(0), &|| true, &file_line, &Saved::none());
        assert_eq!(
            *lines.lock().expect("lines"),
            ["huddle-router: output: after"]
        );
    }
}
