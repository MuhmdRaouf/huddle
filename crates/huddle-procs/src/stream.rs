//! One streamed child process: it leads its own process group, its stdout arrives as lines over
//! an unbounded queue and is teed raw to a log, the end of its stderr is kept for the crash
//! report, stdin can be fed line by line, and the exit says how it ended.

use std::fs::{File, OpenOptions};
use std::io::{self, Write};
use std::os::fd::{AsRawFd, RawFd};
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStderr, ChildStdin, ChildStdout, Command, ExitStatus, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Condvar, Mutex, MutexGuard};
use std::thread;
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use crate::group::{signal_group, signal_name, terminate_group};
use huddle_config::files::{DIR_MODE, create_dir};

/// The default line budget: one MiB, a worker's longest single frame. A stderr line past it is
/// cut, never dropped from the stream.
pub const DEFAULT_LINE_CAP: u64 = 1_048_576;

/// The stderr tail keeps the last 4000 chars.
const STDERR_TAIL_CHARS: usize = 4000;

/// How long a pipe reader waits for bytes before it re-checks the drop flag.
const POLL_WAIT_MS: i32 = 100;

/// How long the supervisor waits after the leader exits before dropping the pipe ends a
/// grandchild may still hold.
const PIPE_DROP: Duration = Duration::from_millis(500);

/// How often a stop waiting on the soft stop re-checks the leader's exit.
const STOP_POLL: Duration = Duration::from_millis(10);

/// What stdin gets when the child starts.
pub enum Stdin {
    /// Stays open for write_line and end_input.
    Piped,
    /// Written by a helper thread, then closed: a prompt larger than the pipe buffer would
    /// otherwise block the start until the child reads.
    Text(String),
}

/// The first interrupt reason, when the run did not end on its own.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Forced {
    Timeout,
    Stopped,
}

impl Forced {
    /// "timeout" or "stopped".
    pub fn as_str(self) -> &'static str {
        match self {
            Forced::Timeout => "timeout",
            Forced::Stopped => "stopped",
        }
    }
}

/// The process's own stop request (an RPC engine's abort), raced against exit and the grace
/// window by the stop handling.
pub type SoftStop = Box<dyn Fn(&InputHandle) + Send + Sync>;

/// How a child is started.
pub struct Spec {
    pub program: PathBuf,
    pub args: Vec<String>,
    pub cwd: PathBuf,
    pub env: Vec<(String, String)>,
    pub stdin: Stdin,
    /// Raw stdout is appended here, byte for byte (0600, parent dir created 0700); an open
    /// failure is a start failure.
    pub stdout_log: Option<PathBuf>,
    /// Names startup stderr that is not trouble; matched lines stay out of the tail.
    pub benign_stderr: Option<fn(&str) -> bool>,
    /// Terminate the rest of the group once the leader exits on its own.
    pub reap_group: bool,
    pub soft_stop: Option<SoftStop>,
}

/// The stop settings a streamed child runs under.
pub struct Limits {
    pub grace: Duration,
    pub timeout: Option<Duration>,
    pub output_cap: u64,
}

/// How the run ended.
#[derive(Clone, Debug, PartialEq)]
pub struct ProcExit {
    pub code: Option<i32>,
    /// The signal's Node spelling; none when the process exited on its own.
    pub signal: Option<String>,
    /// The last stderr lines, benign startup notices left out.
    pub stderr_tail: String,
    /// The first interrupt reason; none when the run ended on its own.
    pub forced: Option<Forced>,
}

/// One answer from the stdout queue.
#[derive(Debug, PartialEq)]
pub enum NextLine {
    Line(String),
    Timeout,
    Eof,
}

/// A started child.
pub struct Streaming {
    pid: u32,
    shared: Arc<Shared>,
    lines: Receiver<String>,
}

/// The child's stdin, for a soft stop that feeds it.
#[derive(Clone)]
pub struct InputHandle {
    shared: Arc<Shared>,
}

/// The child's group, for a stop from outside the run.
#[derive(Clone)]
pub struct StopHandle {
    shared: Arc<Shared>,
}

/// What the handles and the supervisor thread share.
struct Shared {
    pid: u32,
    grace: Duration,
    stdin: Mutex<Option<ChildStdin>>,
    /// reaped: the leader has been waited for, its pid may be reused, so group calls stop;
    /// forced: the first interrupt reason.
    state: Mutex<ProcState>,
    exit_slot: Mutex<Option<ProcExit>>,
    exit_told: Condvar,
    /// The group terminate under way: the first caller starts it, later callers wait for it.
    terminate: Mutex<Termination>,
    terminate_done: Condvar,
    /// Set once the pipe ends are dropped, so readers held open by a grandchild stop polling.
    stdout_dropped: Arc<AtomicBool>,
    stderr_dropped: Arc<AtomicBool>,
    /// Terminate the rest of the group once the leader exits on its own.
    reap_group: bool,
    /// The child's own stop request, shared so a stop can run it on its own thread.
    soft_stop: Option<Arc<SoftStop>>,
}

/// The group terminate run once per child: the runner stays around for later callers, which
/// wait on `done` instead of starting a second terminate.
struct Termination {
    runner: Option<JoinHandle<()>>,
    done: bool,
}

struct ProcState {
    reaped: bool,
    forced: Option<Forced>,
}

/// Locks a mutex, keeping going after a panic in another holder: nothing here is worth stopping
/// the run over.
fn locked<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

/// Starts `cmd` in its own process group with piped stdio. Start failures (a missing binary, a
/// bad cwd, a log that cannot be opened) are an error value; nothing starts.
pub fn spawn_streaming(cmd: Spec, limits: Limits) -> io::Result<Streaming> {
    let Spec {
        program,
        args,
        cwd,
        env,
        stdin,
        stdout_log,
        benign_stderr,
        reap_group,
        soft_stop,
    } = cmd;
    let log = open_log(stdout_log.as_deref())?;
    let mut child = Command::new(&program)
        .args(&args)
        .current_dir(&cwd)
        .env_clear()
        .envs(env)
        .process_group(0)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;

    let pid = child.id();
    let stdin_file = match stdin {
        Stdin::Text(text) => {
            if let Some(mut writer) = child.stdin.take() {
                // The helper thread owns the write end and closes it once written: a child that
                // exits without reading fails the write with EPIPE, its exit tells the story.
                thread::spawn(move || {
                    let _ = writer.write_all(text.as_bytes());
                });
            }
            None
        }
        Stdin::Piped => child.stdin.take(),
    };

    let (tx, lines) = std::sync::mpsc::channel::<String>();
    let stdout_dropped = Arc::new(AtomicBool::new(false));
    let stderr_dropped = Arc::new(AtomicBool::new(false));
    let stdout = child.stdout.take().map(|pipe| {
        let tx = tx.clone();
        read_stdout(
            pipe,
            log,
            tx,
            Arc::clone(&stdout_dropped),
            pid,
            limits.output_cap,
        )
    });
    let stderr = child
        .stderr
        .take()
        .map(|pipe| read_stderr(pipe, benign_stderr, Arc::clone(&stderr_dropped)));
    drop(tx);

    let shared = Arc::new(Shared {
        pid,
        grace: limits.grace,
        stdin: Mutex::new(stdin_file),
        state: Mutex::new(ProcState {
            reaped: false,
            forced: None,
        }),
        exit_slot: Mutex::new(None),
        exit_told: Condvar::new(),
        terminate: Mutex::new(Termination {
            runner: None,
            done: false,
        }),
        terminate_done: Condvar::new(),
        stdout_dropped,
        stderr_dropped,
        reap_group,
        soft_stop: soft_stop.map(Arc::new),
    });
    // The supervisor drops its end once the leader is reaped, which ends the timer thread
    // without firing it.
    let (timer_hold, timer) = std::sync::mpsc::channel::<()>();
    supervise(child, Arc::clone(&shared), stdout, stderr, timer_hold);

    // The wall clock: past the timeout the stop handle interrupts with Forced::Timeout. A zero
    // timeout means no timer at all.
    match limits.timeout {
        Some(remainder) if remainder > Duration::ZERO => {
            let stop = StopHandle {
                shared: Arc::clone(&shared),
            };
            thread::spawn(move || {
                if let Err(RecvTimeoutError::Timeout) = timer.recv_timeout(remainder) {
                    stop.interrupt(Forced::Timeout);
                }
            });
        }
        _ => {}
    }

    Ok(Streaming { pid, shared, lines })
}

/// Splits the child's stdout into lines and tees the raw bytes to the log. Lines land in the
/// unbounded queue, so stdout is drained even when nobody reads. A line that passes the budget
/// with no newline is dropped, the group is killed and reading stops: the queue ends.
fn read_stdout(
    pipe: ChildStdout,
    log: Option<File>,
    tx: Sender<String>,
    dropped: Arc<AtomicBool>,
    pid: u32,
    output_cap: u64,
) -> JoinHandle<()> {
    let fd = pipe.as_raw_fd();
    thread::spawn(move || {
        let _pipe = pipe; // the read end stays ours until the stream ends
        let mut log = log;
        let mut pending: Vec<u8> = Vec::new();
        let mut overflowed = false;
        pump(fd, &dropped, |bytes| {
            if let Some(file) = &mut log {
                let _ = file.write_all(bytes);
            }
            if overflowed {
                return false;
            }
            pending.extend_from_slice(bytes);
            while let Some(nl) = pending.iter().position(|&b| b == b'\n') {
                let line: Vec<u8> = pending.drain(..=nl).collect();
                let _ = tx.send(String::from_utf8_lossy(&line[..nl]).into_owned());
            }
            if pending.len() as u64 > output_cap {
                pending.clear();
                overflowed = true;
                signal_group(pid, libc::SIGKILL);
                return false;
            }
            true
        });
        // The last line without "\n" is still a line; the budget path sends nothing.
        if !overflowed && !pending.is_empty() {
            let _ = tx.send(String::from_utf8_lossy(&pending).into_owned());
        }
    })
}

/// Keeps the end of stderr as written, benign lines left out.
fn read_stderr(
    pipe: ChildStderr,
    benign: Option<fn(&str) -> bool>,
    dropped: Arc<AtomicBool>,
) -> JoinHandle<String> {
    let fd = pipe.as_raw_fd();
    thread::spawn(move || {
        let _pipe = pipe;
        let mut tail = String::new();
        let mut pending: Vec<u8> = Vec::new();
        pump(fd, &dropped, |bytes| {
            pending.extend_from_slice(bytes);
            while let Some(nl) = pending.iter().position(|&b| b == b'\n') {
                let line: Vec<u8> = pending.drain(..=nl).collect();
                push_tail_line(&mut tail, &line[..nl], true, benign);
            }
            true
        });
        if !pending.is_empty() {
            push_tail_line(&mut tail, &pending, false, benign);
        }
        tail
    })
}

/// Appends one stderr line to the tail: a line past the cap is cut to its first cap bytes and
/// reading goes on, benign lines stay out, and the tail keeps the last STDERR_TAIL_CHARS chars.
fn push_tail_line(tail: &mut String, raw: &[u8], complete: bool, benign: Option<fn(&str) -> bool>) {
    let cap = DEFAULT_LINE_CAP as usize;
    let raw = &raw[..raw.len().min(cap)];
    let text = String::from_utf8_lossy(raw);
    if benign.is_some_and(|matches| matches(&text)) {
        return;
    }
    tail.push_str(&text);
    if complete {
        tail.push('\n');
    }
    keep_last_chars(tail, STDERR_TAIL_CHARS);
}

/// Keeps the last `max` chars, dropping whole chars from the front.
fn keep_last_chars(text: &mut String, max: usize) {
    let total = text.chars().count();
    if total <= max {
        return;
    }
    let start = text.char_indices().nth(total - max).map_or(0, |(i, _)| i);
    text.drain(..start);
}

/// Reads `fd` until EOF, an error, `dropped` set with nothing readable, or `on_bytes` answering
/// false. Reading goes through poll+read because a blocked read on a pipe cannot be interrupted.
fn pump(fd: RawFd, dropped: &AtomicBool, mut on_bytes: impl FnMut(&[u8]) -> bool) {
    let mut buf = [0u8; 64 * 1024];
    loop {
        let mut watched = [libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        }];
        // SAFETY: poll fills `watched` for the one open fd we pass.
        let ready = unsafe { libc::poll(watched.as_mut_ptr(), 1, POLL_WAIT_MS) };
        if ready < 0 {
            if io::Error::last_os_error().kind() == io::ErrorKind::Interrupted {
                continue;
            }
            return;
        }
        if ready == 0 {
            if dropped.load(Ordering::Relaxed) {
                return;
            }
            continue;
        }
        // SAFETY: read writes at most buf.len() bytes into `buf` from the polled fd.
        let read = unsafe { libc::read(fd, buf.as_mut_ptr().cast(), buf.len()) };
        if read < 0 {
            if io::Error::last_os_error().kind() == io::ErrorKind::Interrupted {
                continue;
            }
            return;
        }
        if read == 0 {
            return; // EOF
        }
        if !on_bytes(&buf[..read as usize]) {
            return;
        }
    }
}

/// Waits the leader out, drops the pipes grandchildren may hold, settles any group termination
/// and hands the exit to `wait`.
fn supervise(
    mut child: Child,
    shared: Arc<Shared>,
    stdout: Option<JoinHandle<()>>,
    stderr: Option<JoinHandle<String>>,
    timer: Sender<()>,
) {
    thread::spawn(move || {
        let (code, signal) = match child.wait() {
            Ok(status) => decode_exit(status),
            // A wait failure after a successful spawn names no ending we trust.
            Err(_) => (None, None),
        };
        // Past the leader's exit the kernel has reaped it and its pid is free for reuse, so
        // group calls stop; dropping the timer ends its thread without firing it.
        locked(&shared.state).reaped = true;
        shared.exit_told.notify_all();
        drop(timer);
        // The run did end on its own, so the exit stays unforced even when the rest of the
        // group is terminated here.
        if shared.reap_group {
            settle_termination(&shared, true);
        }
        // Grandchildren can still hold the pipe ends open after the leader exits; they are
        // dropped, not chased — the group is only ever signalled on request.
        let stdout_dropped = Arc::clone(&shared.stdout_dropped);
        let stderr_dropped = Arc::clone(&shared.stderr_dropped);
        thread::spawn(move || {
            thread::sleep(PIPE_DROP);
            stdout_dropped.store(true, Ordering::Relaxed);
            stderr_dropped.store(true, Ordering::Relaxed);
        });
        if let Some(reader) = stdout {
            let _ = reader.join();
        }
        let stderr_tail = stderr
            .and_then(|reader| reader.join().ok())
            .unwrap_or_default();
        // A group termination under way settles before the exit is reported: nothing the run
        // started is left behind.
        settle_termination(&shared, false);
        // An engine drained this way disposes its session and exits.
        close_input(&shared);
        let forced = locked(&shared.state).forced;
        let exit = ProcExit {
            code,
            signal,
            stderr_tail,
            forced,
        };
        let mut slot = locked(&shared.exit_slot);
        *slot = Some(exit);
        drop(slot);
        shared.exit_told.notify_all();
    });
}

/// Runs `terminate_group` once and waits for it to settle: the first caller starts it, later
/// callers wait out the same one. With `start_when_reaped` false nothing starts once the leader
/// has been reaped — its pid may be reused, so the signal could reach a stranger's group. The
/// supervisor's reap_group path passes true: clearing the leftover group is its job.
fn settle_termination(shared: &Arc<Shared>, start_when_reaped: bool) {
    let mut term = locked(&shared.terminate);
    if term.runner.is_none() && (start_when_reaped || !locked(&shared.state).reaped) {
        let pid = shared.pid;
        let grace = shared.grace;
        let worker = Arc::clone(shared);
        term.runner = Some(thread::spawn(move || {
            terminate_group(pid, grace);
            let mut term = locked(&worker.terminate);
            term.done = true;
            worker.terminate_done.notify_all();
        }));
    }
    while term.runner.is_some() && !term.done {
        term = shared
            .terminate_done
            .wait(term)
            .unwrap_or_else(|poison| poison.into_inner());
    }
}

/// The exit code, or the signal's name when a signal ended the child.
fn decode_exit(status: ExitStatus) -> (Option<i32>, Option<String>) {
    if let Some(code) = status.code() {
        return (Some(code), None);
    }
    match status.signal() {
        Some(signal) => (None, Some(signal_name(signal))),
        None => (None, None),
    }
}

/// Opens the raw stdout log for append, creating the parent directory (0700) and the file
/// (0600); a failure is a start failure.
fn open_log(path: Option<&Path>) -> io::Result<Option<File>> {
    let Some(path) = path else { return Ok(None) };
    if let Some(parent) = path.parent()
        && !parent.as_os_str().is_empty()
    {
        create_dir(parent, DIR_MODE)?;
    }
    let file = OpenOptions::new()
        .append(true)
        .create(true)
        .mode(0o600)
        .open(path)?;
    Ok(Some(file))
}

/// Writes one line plus "\n" to the child's stdin. On the first write error the stdin is dropped
/// and the answer is false: the child closed its stdin, its exit tells the story.
fn feed_line(shared: &Shared, line: &str) -> bool {
    let mut stdin = locked(&shared.stdin);
    let Some(mut writer) = stdin.take() else {
        return false;
    };
    let mut bytes = Vec::with_capacity(line.len() + 1);
    bytes.extend_from_slice(line.as_bytes());
    bytes.push(b'\n');
    match writer.write_all(&bytes) {
        Ok(()) => {
            *stdin = Some(writer);
            true
        }
        Err(_) => false,
    }
}

/// Closes the child's stdin: an RPC engine then drains, disposes its session and exits 0.
fn close_input(shared: &Shared) {
    *locked(&shared.stdin) = None;
}

impl Streaming {
    /// The process-group leader's pid.
    pub fn pid(&self) -> u32 {
        self.pid
    }

    /// Blocks up to `timeout` (forever when None) for the next line. Eof once stdout is drained.
    pub fn next_line(&mut self, timeout: Option<Duration>) -> NextLine {
        match timeout {
            None => match self.lines.recv() {
                Ok(line) => NextLine::Line(line),
                Err(_) => NextLine::Eof,
            },
            Some(limit) => match self.lines.recv_timeout(limit) {
                Ok(line) => NextLine::Line(line),
                Err(RecvTimeoutError::Timeout) => NextLine::Timeout,
                Err(RecvTimeoutError::Disconnected) => NextLine::Eof,
            },
        }
    }

    /// Writes one line plus "\n" to stdin; false once stdin is closed.
    pub fn write_line(&self, line: &str) -> bool {
        feed_line(&self.shared, line)
    }

    /// Closes stdin.
    pub fn end_input(&self) {
        close_input(&self.shared);
    }

    /// The child's stdin handle.
    pub fn input(&self) -> InputHandle {
        InputHandle {
            shared: Arc::clone(&self.shared),
        }
    }

    /// The child's stop handle.
    pub fn stop_handle(&self) -> StopHandle {
        StopHandle {
            shared: Arc::clone(&self.shared),
        }
    }

    /// Blocks until the process has exited and stdout and stderr are drained. Callable any
    /// number of times.
    pub fn wait(&self) -> ProcExit {
        let mut slot = locked(&self.shared.exit_slot);
        loop {
            if let Some(exit) = slot.as_ref() {
                return exit.clone();
            }
            slot = self
                .shared
                .exit_told
                .wait(slot)
                .unwrap_or_else(|poison| poison.into_inner());
        }
    }
}

impl InputHandle {
    /// Writes one line plus "\n" to stdin; false once stdin is closed.
    pub fn write_line(&self, line: &str) -> bool {
        feed_line(&self.shared, line)
    }

    /// Closes stdin.
    pub fn end_input(&self) {
        close_input(&self.shared);
    }

    /// The process-group leader's pid, for a soft stop that waits on the group.
    pub fn pid(&self) -> u32 {
        self.shared.pid
    }
}

impl StopHandle {
    /// The process-group leader's pid.
    pub fn pid(&self) -> u32 {
        self.shared.pid
    }

    /// The first reason wins: the exit reports it as forced. The soft stop runs on its own
    /// thread, and the stop waits for it to return, the leader's exit or the grace, whichever
    /// comes first; then the group is terminated once with the configured grace. Past the
    /// leader's reap no signal goes out any more.
    pub fn interrupt(&self, reason: Forced) {
        let reaped = {
            let mut state = locked(&self.shared.state);
            if state.forced.is_none() {
                state.forced = Some(reason);
            }
            state.reaped
        };
        if !reaped {
            self.run_soft_stop();
        }
        settle_termination(&self.shared, false);
    }

    /// Runs the soft stop on its own thread and waits for it to finish, the leader's exit or
    /// the grace window, whichever comes first.
    fn run_soft_stop(&self) {
        let Some(soft) = self.shared.soft_stop.clone() else {
            return;
        };
        let input = InputHandle {
            shared: Arc::clone(&self.shared),
        };
        let done = Arc::new((Mutex::new(false), Condvar::new()));
        let runner = Arc::clone(&done);
        thread::spawn(move || {
            soft(&input);
            let (flag, told) = &*runner;
            *locked(flag) = true;
            told.notify_all();
        });
        let deadline = Instant::now() + self.shared.grace;
        let (flag, told) = &*done;
        let mut finished = locked(flag);
        while !*finished {
            if locked(&self.shared.state).reaped {
                return;
            }
            let now = Instant::now();
            if now >= deadline {
                return;
            }
            let slice = (deadline - now).min(STOP_POLL);
            let (guard, _) = told
                .wait_timeout(finished, slice)
                .unwrap_or_else(|poison| poison.into_inner());
            finished = guard;
        }
    }

    /// SIGKILLs the group; a no-op after the leader is reaped.
    pub fn kill(&self) {
        if locked(&self.shared.state).reaped {
            return;
        }
        signal_group(self.shared.pid, libc::SIGKILL);
    }
}

/// The default output budget of a short tool: 512 MiB of stdout plus stderr. Past it the tool is
/// killed.
pub const DEFAULT_TOOL_OUTPUT: u64 = 536_870_912;

/// How long a version probe may run: a tool can sit on a first-run prompt.
pub const VERSION_TIMEOUT: Duration = Duration::from_millis(10_000);

/// The settings of one short tool run: the command line, its inputs and its budgets.
pub struct ToolSpec {
    pub program: PathBuf,
    pub args: Vec<String>,
    /// None: inherit the caller's cwd.
    pub cwd: Option<PathBuf>,
    /// The whole environment; None: inherit the caller's.
    pub env: Option<Vec<(String, String)>>,
    pub stdin: Option<Vec<u8>>,
    pub timeout: Option<Duration>,
    /// Combined stdout plus stderr budget in bytes.
    pub max_output: u64,
}

/// One short tool run's outcome.
#[derive(Debug)]
pub struct ToolOutput {
    pub code: Option<i32>,
    pub signal: Option<String>,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

/// How many bytes stdout and stderr have produced together.
#[derive(Default)]
struct OutputBudget {
    seen: AtomicU64,
}

impl OutputBudget {
    /// Counts one read and answers whether stdout plus stderr are now past `max`.
    fn past(&self, bytes: usize, max: u64) -> bool {
        self.seen.fetch_add(bytes as u64, Ordering::SeqCst) + bytes as u64 > max
    }
}

/// Runs a short tool (`<bin> --version`, a keystore tool) to completion: no shell, no process
/// group, stdin written then closed, a timeout that SIGTERMs the child, and an output budget
/// that SIGKILLs it once stdout plus stderr pass it. A tool that cannot start (not on PATH, not
/// executable, a bad cwd) is an error value.
pub fn run_tool(spec: ToolSpec) -> io::Result<ToolOutput> {
    let mut command = Command::new(&spec.program);
    command.args(&spec.args);
    if let Some(cwd) = &spec.cwd {
        command.current_dir(cwd);
    }
    if let Some(env) = &spec.env {
        command.env_clear();
        command.envs(env.iter().map(|(name, value)| (name, value)));
    }
    command
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = command.spawn()?;
    let pid = child.id() as i32;

    // One guard for the two killers: the budget kill and the timeout kill each fire once, and
    // neither signals a child the run has already reaped.
    let stopped = Arc::new(AtomicBool::new(false));
    let budget = Arc::new(OutputBudget::default());

    let stdin = child.stdin.take();
    let bytes = spec.stdin.unwrap_or_default();
    let stdin_thread = thread::spawn(move || {
        if let Some(mut pipe) = stdin {
            // A write the child refuses is its answer, not ours to fix.
            let _ = pipe.write_all(&bytes);
        }
    });

    let stdout_pipe = child.stdout.take();
    let stdout_budget = Arc::clone(&budget);
    let stdout_stopper = Arc::clone(&stopped);
    let stdout_thread = thread::spawn(move || {
        collect_pipe(
            stdout_pipe,
            &stdout_budget,
            &stdout_stopper,
            pid,
            spec.max_output,
        )
    });

    let stderr_pipe = child.stderr.take();
    let stderr_budget = Arc::clone(&budget);
    let stderr_stopper = Arc::clone(&stopped);
    let stderr_thread = thread::spawn(move || {
        collect_pipe(
            stderr_pipe,
            &stderr_budget,
            &stderr_stopper,
            pid,
            spec.max_output,
        )
    });

    // Waiting polls so a fast tool never pays its own timeout budget; past the deadline the
    // child gets one SIGTERM and the poll keeps going until it is reaped.
    let deadline = spec.timeout.map(|timeout| Instant::now() + timeout);
    let status = loop {
        if let Some(status) = child.try_wait()? {
            break status;
        }
        if deadline.is_some_and(|deadline| Instant::now() >= deadline)
            && !stopped.swap(true, Ordering::SeqCst)
        {
            unsafe { libc::kill(pid, libc::SIGTERM) };
        }
        thread::sleep(STOP_POLL);
    };

    let (code, signal) = decode_exit(status);
    let stdout = collected(stdout_thread);
    let stderr = collected(stderr_thread);
    let _ = stdin_thread.join();
    Ok(ToolOutput {
        code,
        signal,
        stdout,
        stderr,
    })
}

/// Reads one output pipe to its end, appending to the shared budget. The first read that puts
/// stdout plus stderr past the budget SIGKILLs the child; its dying closes this pipe.
fn collect_pipe(
    pipe: Option<impl io::Read>,
    budget: &OutputBudget,
    stopped: &AtomicBool,
    pid: i32,
    max_output: u64,
) -> Vec<u8> {
    let mut out = Vec::new();
    let Some(mut pipe) = pipe else { return out };
    let mut chunk = [0u8; 8192];
    loop {
        match pipe.read(&mut chunk) {
            Ok(0) | Err(_) => return out,
            Ok(read) => {
                out.extend_from_slice(&chunk[..read]);
                if budget.past(read, max_output) && !stopped.swap(true, Ordering::SeqCst) {
                    unsafe { libc::kill(pid, libc::SIGKILL) };
                }
            }
        }
    }
}

/// The bytes one reader thread collected; a read that failed keeps what landed.
fn collected(handle: JoinHandle<Vec<u8>>) -> Vec<u8> {
    handle.join().unwrap_or_default()
}

/// The first line of the version output, or why there is none: the spawn error's text, or
/// `command failed: <program> <version args>` on a non-zero exit or a signal, or
/// `printed no version` on empty output. The tool runs in `cwd`: a harness must never be
/// started inside the user's repository before its worktree exists.
pub fn tool_version(
    program: &Path,
    prefix_args: &[String],
    version_args: &[String],
    cwd: &Path,
    env: Option<Vec<(String, String)>>,
) -> Result<String, String> {
    let mut args = prefix_args.to_vec();
    args.extend(version_args.iter().cloned());
    let output = run_tool(ToolSpec {
        program: program.to_path_buf(),
        args,
        cwd: Some(cwd.to_path_buf()),
        env,
        stdin: None,
        timeout: Some(VERSION_TIMEOUT),
        max_output: DEFAULT_TOOL_OUTPUT,
    })
    .map_err(|error| error.to_string())?;
    if output.code != Some(0) {
        return Err(format!(
            "command failed: {} {}",
            program.display(),
            version_args.join(" ")
        ));
    }
    let stdout = String::from_utf8_lossy(&output.stdout);
    let first = stdout.trim().lines().next().unwrap_or_default();
    if first.is_empty() {
        return Err("printed no version".to_string());
    }
    Ok(first.to_string())
}
