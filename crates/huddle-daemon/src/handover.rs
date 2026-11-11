//! The daemon's handover: a running daemon gives its listening socket to a new daemon over a
//! unix socket in the run dir, so the port never closes across a binary update. The single
//! winner is decided before the descriptor is sent, and the daemon retires only after the taker
//! acknowledges the handover.

use std::fmt;
use std::fs::{self, Permissions};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::mem;
use std::net::TcpListener;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::os::unix::fs::PermissionsExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::ptr;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use huddle_config::files::{DIR_MODE, create_dir};
use tokio::sync::mpsc;

/// The unix socket in the run dir a takeover dials.
pub const HANDOVER_SOCKET: &str = "handover.sock";

/// How long one takeover connection's question and its answer may take, on both sides.
pub const ANSWER_WAIT: Duration = Duration::from_secs(5);

/// How often the accept loop wakes to check the stop flag: a takeover waits at most this long.
const ACCEPT_POLL: Duration = Duration::from_millis(200);

/// Where the daemon's handover lines go.
pub type Logger = Arc<dyn Fn(String) + Send + Sync>;

/// What every handover connection shares: the socket file's path, the claim and retirement
/// flags, and the listener whose descriptor a winning taker receives.
struct HandoverServer {
    path: PathBuf,
    stop: AtomicBool,
    claimed: AtomicBool,
    retired: AtomicBool,
    port: u16,
    listener: TcpListener,
    handed: mpsc::UnboundedSender<u32>,
    log: Logger,
}

/// A running handover listener. Dropping it does not stop it; call `stop` when serving ends.
pub struct HandoverListener {
    server: Option<Arc<HandoverServer>>,
    accept_thread: Mutex<Option<thread::JoinHandle<()>>>,
}

impl HandoverListener {
    /// Retires the socket file unless a handover already did.
    pub fn stop(&self) {
        let Some(server) = self.server.as_ref() else {
            return;
        };
        server.stop.store(true, Ordering::SeqCst);
        if server
            .retired
            .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
            .is_ok()
        {
            let _ = fs::remove_file(&server.path);
        }
        if let Ok(mut slot) = self.accept_thread.lock()
            && let Some(handle) = slot.take()
        {
            let _ = handle.join();
        }
    }
}

/// Listens on `<run_dir>/handover.sock`. A socket that cannot open (a path longer than the OS
/// allows, a run dir that cannot be made) is logged as `huddle-router: handover socket: <err>`
/// and answers a no-op listener: the router still serves.
pub fn serve_handover(
    run_dir: &Path,
    port: u16,
    listener: &TcpListener,
    handed: mpsc::UnboundedSender<u32>,
    log: Logger,
) -> HandoverListener {
    let path = run_dir.join(HANDOVER_SOCKET);
    if let Err(source) = create_dir(run_dir, DIR_MODE) {
        return cannot_serve(source, log);
    }
    // A stale file from a killed daemon never blocks the listen.
    let _ = fs::remove_file(&path);
    let unix = match UnixListener::bind(&path) {
        Ok(unix) => unix,
        Err(source) => return cannot_serve(source, log),
    };
    if let Err(source) = unix.set_nonblocking(true) {
        return cannot_serve(source, log);
    }
    if let Err(source) = fs::set_permissions(&path, Permissions::from_mode(0o600)) {
        (log)(format!("huddle-router: handover socket: {source}"));
    }
    let listener = match listener.try_clone() {
        Ok(listener) => listener,
        Err(source) => return cannot_serve(source, log),
    };
    let server = Arc::new(HandoverServer {
        path: path.clone(),
        stop: AtomicBool::new(false),
        claimed: AtomicBool::new(false),
        retired: AtomicBool::new(false),
        port,
        listener,
        handed,
        log,
    });
    let loop_server = Arc::clone(&server);
    let accept_thread = thread::spawn(move || accept_loop(loop_server, unix));
    HandoverListener {
        server: Some(server),
        accept_thread: Mutex::new(Some(accept_thread)),
    }
}

fn cannot_serve(source: io::Error, log: Logger) -> HandoverListener {
    (log)(format!("huddle-router: handover socket: {source}"));
    HandoverListener {
        server: None,
        accept_thread: Mutex::new(None),
    }
}

fn accept_loop(server: Arc<HandoverServer>, unix: UnixListener) {
    loop {
        if server.stop.load(Ordering::SeqCst) {
            return;
        }
        match unix.accept() {
            Ok((stream, _addr)) => {
                let server = Arc::clone(&server);
                thread::spawn(move || answer(&server, stream));
            }
            Err(source) if source.kind() == io::ErrorKind::WouldBlock => thread::sleep(ACCEPT_POLL),
            Err(source) if source.kind() == io::ErrorKind::Interrupted => {}
            Err(_) => return,
        }
    }
}

fn answer(server: &HandoverServer, stream: UnixStream) {
    // The accepted stream inherits the listener's non-blocking flag, and a read or write timeout
    // needs a blocking socket to wait on.
    let _ = stream.set_nonblocking(false);
    let _ = stream.set_read_timeout(Some(ANSWER_WAIT));
    let _ = stream.set_write_timeout(Some(ANSWER_WAIT));
    let mut reader = match stream.try_clone() {
        Ok(reader) => BufReader::new(reader),
        Err(_) => return,
    };
    let mut line = String::new();
    if reader.read_line(&mut line).is_err() {
        return;
    }
    let Some(pid) = parse_takeover(&line) else {
        return;
    };
    // The single winner is decided before any descriptor moves.
    if server
        .claimed
        .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
        .is_err()
    {
        return;
    }
    let port = server.port;
    let sent = send_fds(
        &stream,
        format!("ok {port}\n").as_bytes(),
        &[server.listener.as_raw_fd()],
    );
    if let Err(source) = sent {
        server.claimed.store(false, Ordering::SeqCst);
        (server.log)(format!("huddle-router: handover: {source}"));
        return;
    }
    let mut ack = String::new();
    let acked = matches!(reader.read_line(&mut ack), Ok(read) if read > 0) && ack.trim() == "got";
    if !acked {
        // The taker gave up and the kernel dropped the descriptor in flight: the socket stays
        // ours and the next taker may claim it.
        server.claimed.store(false, Ordering::SeqCst);
        return;
    }
    retire(server, pid);
}

fn retire(server: &HandoverServer, pid: u32) {
    if server
        .retired
        .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
        .is_err()
    {
        return;
    }
    server.stop.store(true, Ordering::SeqCst);
    let _ = fs::remove_file(&server.path);
    let _ = server.handed.send(pid);
}

fn parse_takeover(line: &str) -> Option<u32> {
    let trimmed = line.trim();
    let rest = trimmed.strip_prefix("takeover ").unwrap_or(trimmed);
    rest.parse::<u32>().ok()
}

/// The taker's side: dial, ask, get one descriptor, acknowledge. Blocking: run off the async
/// runtime.
pub fn takeover(run_dir: &Path, pid: u32) -> Result<TcpListener, HandoverError> {
    let path = run_dir.join(HANDOVER_SOCKET);
    let mut stream = UnixStream::connect(&path).map_err(|source| HandoverError::NoDaemon {
        path: path.clone(),
        message: source.to_string(),
    })?;
    let _ = stream.set_read_timeout(Some(ANSWER_WAIT));
    let _ = stream.set_write_timeout(Some(ANSWER_WAIT));
    let question = format!("takeover {pid}\n");
    stream
        .write_all(question.as_bytes())
        .map_err(|source| HandoverError::Socket {
            path: path.clone(),
            message: source.to_string(),
        })?;
    let mut data = [0u8; 128];
    let (received, fds) =
        recv_fds(&stream, &mut data).map_err(|source| HandoverError::NeverHandedOver {
            path: path.clone(),
            message: source.to_string(),
        })?;
    let count = fds.len();
    let mut fds = fds;
    let fd = match fds.pop() {
        Some(fd) if fds.is_empty() => fd,
        // A descriptor the taker refuses is closed by the drop.
        _ => return Err(HandoverError::WrongCount { path, count }),
    };
    let line = String::from_utf8_lossy(&data[..received])
        .trim()
        .to_string();
    let port_text = line.strip_prefix("ok ").unwrap_or(&line);
    if port_text.parse::<u16>().is_err() {
        return Err(HandoverError::Answer { path, line });
    }
    let taken = TcpListener::from(fd);
    taken
        .local_addr()
        .map_err(|source| HandoverError::Fd(source.to_string()))?;
    taken
        .set_nonblocking(true)
        .map_err(|source| HandoverError::Fd(source.to_string()))?;
    // Only now does the taker say it has the socket: a refusal above must leave the daemon
    // serving. The daemon retires and closes on the acknowledgement, so reading the stream to
    // its end proves the handover completed before this returns.
    stream
        .write_all(b"got\n")
        .map_err(|source| HandoverError::Socket {
            path: path.clone(),
            message: source.to_string(),
        })?;
    let mut closing = [0u8; 16];
    match stream.read(&mut closing) {
        Ok(0) => {}
        Ok(_) => {
            return Err(HandoverError::Socket {
                path,
                message: "the daemon kept talking after the handover".to_string(),
            });
        }
        Err(source) => {
            return Err(HandoverError::Socket {
                path,
                message: format!("the daemon never closed after the handover ({source})"),
            });
        }
    }
    Ok(taken)
}

/// Sends one message: the data bytes plus every given descriptor in one SCM_RIGHTS header.
fn send_fds(socket: &UnixStream, data: &[u8], fds: &[RawFd]) -> io::Result<()> {
    let fd_bytes = mem::size_of_val(fds) as libc::c_uint;
    // SAFETY: CMSG_SPACE computes a buffer size from its argument.
    let space = unsafe { libc::CMSG_SPACE(fd_bytes) } as usize;
    let mut control = vec![0u8; space];
    let mut iov = libc::iovec {
        iov_base: data.as_ptr() as *mut libc::c_void,
        iov_len: data.len() as _,
    };
    // SAFETY: a zeroed msghdr is an empty message; the fields below fill it in.
    let mut msg: libc::msghdr = unsafe { mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1 as _;
    msg.msg_control = control.as_mut_ptr() as _;
    msg.msg_controllen = space as _;
    // SAFETY: CMSG_FIRSTHDR answers the first header of a buffer whose controllen is set.
    let header = unsafe { libc::CMSG_FIRSTHDR(&msg) };
    if header.is_null() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "no control header",
        ));
    }
    // SAFETY: header points into the control buffer, which holds CMSG_SPACE(fd_bytes) bytes.
    unsafe {
        (*header).cmsg_level = libc::SOL_SOCKET;
        (*header).cmsg_type = libc::SCM_RIGHTS;
        (*header).cmsg_len = libc::CMSG_LEN(fd_bytes) as _;
        ptr::copy_nonoverlapping(
            fds.as_ptr(),
            libc::CMSG_DATA(header) as *mut RawFd,
            fds.len(),
        );
    }
    let sent: isize = loop {
        // SAFETY: msg and its buffers are initialized; the socket owns a valid descriptor.
        let count = unsafe { libc::sendmsg(socket.as_raw_fd(), &msg, 0) };
        if count == -1 {
            let source = io::Error::last_os_error();
            if source.kind() != io::ErrorKind::Interrupted {
                return Err(source);
            }
        } else {
            break count;
        }
    };
    if sent as usize != data.len() {
        return Err(io::Error::new(io::ErrorKind::WriteZero, "short write"));
    }
    Ok(())
}

/// Receives one message: the data bytes and every descriptor its SCM_RIGHTS headers carry, each
/// wrapped in an [`OwnedFd`] so a descriptor nobody takes is closed.
fn recv_fds(socket: &UnixStream, data: &mut [u8]) -> io::Result<(usize, Vec<OwnedFd>)> {
    let mut control = [0u8; 128];
    let mut iov = libc::iovec {
        iov_base: data.as_mut_ptr() as *mut libc::c_void,
        iov_len: data.len() as _,
    };
    // SAFETY: a zeroed msghdr is an empty message; the fields below fill it in.
    let mut msg: libc::msghdr = unsafe { mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1 as _;
    msg.msg_control = control.as_mut_ptr() as _;
    msg.msg_controllen = control.len() as _;
    let received: isize = loop {
        // SAFETY: msg and its buffers are initialized; the socket owns a valid descriptor.
        let count = unsafe { libc::recvmsg(socket.as_raw_fd(), &mut msg, 0) };
        if count == -1 {
            let source = io::Error::last_os_error();
            if source.kind() != io::ErrorKind::Interrupted {
                return Err(source);
            }
        } else {
            break count;
        }
    };
    let mut fds = Vec::new();
    // SAFETY: the headers live in msg's control buffer; CMSG_NXTHDR stops at the last one, and
    // every descriptor the kernel placed there is ours once read.
    unsafe {
        let mut header = libc::CMSG_FIRSTHDR(&msg);
        while !header.is_null() {
            if (*header).cmsg_level == libc::SOL_SOCKET && (*header).cmsg_type == libc::SCM_RIGHTS {
                let carried = ((*header).cmsg_len as usize - libc::CMSG_LEN(0) as usize)
                    / mem::size_of::<RawFd>();
                let carried_fds = libc::CMSG_DATA(header) as *const RawFd;
                for index in 0..carried {
                    fds.push(OwnedFd::from_raw_fd(carried_fds.add(index).read()));
                }
            }
            header = libc::CMSG_NXTHDR(&msg, header);
        }
    }
    Ok((received as usize, fds))
}

/// A takeover that cannot complete: the caller logs the line and falls back to a normal start.
#[derive(Debug)]
pub enum HandoverError {
    NoDaemon { path: PathBuf, message: String },
    Socket { path: PathBuf, message: String },
    NeverHandedOver { path: PathBuf, message: String },
    WrongCount { path: PathBuf, count: usize },
    Answer { path: PathBuf, line: String },
    Fd(String),
}

impl fmt::Display for HandoverError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            HandoverError::NoDaemon { path, message } => {
                write!(
                    f,
                    "no daemon to take over from at {} ({message})",
                    path.display()
                )
            }
            HandoverError::Socket { path, message } => {
                write!(f, "the handover socket at {}: {message}", path.display())
            }
            HandoverError::NeverHandedOver { path, message } => {
                write!(
                    f,
                    "the daemon at {} never handed over within 5s ({message})",
                    path.display()
                )
            }
            HandoverError::WrongCount { path, count } => {
                write!(
                    f,
                    "the daemon at {} handed over {count} sockets, want one",
                    path.display()
                )
            }
            HandoverError::Answer { path, line } => {
                write!(f, "the daemon at {} answered \"{line}\"", path.display())
            }
            HandoverError::Fd(message) => write!(f, "the handed-over socket: {message}"),
        }
    }
}

impl std::error::Error for HandoverError {}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_testkit::temp_home::TempHome;
    use std::{
        io::{BufRead, BufReader, Read, Write},
        net::TcpStream,
        os::unix::net::UnixStream,
        sync::Mutex,
        thread,
        time::Duration,
    };
    use tokio::sync::mpsc;

    fn listener() -> (TcpListener, u16) {
        let tcp = TcpListener::bind("127.0.0.1:0").expect("bind");
        let port = tcp.local_addr().expect("addr").port();
        (tcp, port)
    }

    fn no_log() -> Logger {
        Arc::new(|_line: String| {})
    }

    fn lines() -> (Arc<Mutex<Vec<String>>>, Logger) {
        let store = Arc::new(Mutex::new(Vec::new()));
        let sink = Arc::clone(&store);
        (
            store,
            Arc::new(move |line: String| sink.lock().expect("lines").push(line)),
        )
    }

    #[test]
    fn a_takeover_receives_a_listener_that_really_serves() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        let (tcp, port) = listener();
        let (handed, mut seen) = mpsc::unbounded_channel();
        let handover = serve_handover(&run_dir, port, &tcp, handed, no_log());
        assert_eq!(fs_mode(&run_dir.join(HANDOVER_SOCKET)), 0o600);
        let taken = takeover(&run_dir, 777).expect("takeover");
        assert_eq!(taken.local_addr().expect("addr").port(), port);
        assert_eq!(seen.try_recv().expect("the taker's pid is named"), 777);
        assert!(
            !run_dir.join(HANDOVER_SOCKET).exists(),
            "the socket file is retired at handover"
        );
        handover.stop();
        // A real round trip through the handed-over socket on the same port.
        taken.set_nonblocking(false).expect("blocking");
        thread::spawn(move || {
            let (mut stream, _) = taken.accept().expect("accept");
            let mut request = [0u8; 16];
            let _ = stream.read(&mut request);
            stream.write_all(b"pong").expect("write");
        });
        let mut client = TcpStream::connect(("127.0.0.1", port)).expect("connect");
        client.write_all(b"ping").expect("write");
        let mut answer = String::new();
        client.read_to_string(&mut answer).expect("read");
        assert_eq!(answer, "pong");
    }

    fn fs_mode(path: &Path) -> u32 {
        use std::os::unix::fs::PermissionsExt;
        std::fs::metadata(path).expect("stat").permissions().mode() & 0o777
    }

    #[test]
    fn a_stale_socket_file_is_cleared_before_the_listen() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        std::fs::create_dir_all(&run_dir).expect("mkdir");
        std::fs::write(run_dir.join(HANDOVER_SOCKET), b"stale").expect("write");
        let (tcp, port) = listener();
        let (handed, _seen) = mpsc::unbounded_channel();
        let handover = serve_handover(&run_dir, port, &tcp, handed, no_log());
        assert!(takeover(&run_dir, 1).is_ok());
        handover.stop();
    }

    #[test]
    fn two_takers_race_and_only_one_gets_the_socket() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        let (tcp, port) = listener();
        let (handed, mut seen) = mpsc::unbounded_channel();
        let handover = serve_handover(&run_dir, port, &tcp, handed, no_log());
        let a = {
            let dir = run_dir.clone();
            thread::spawn(move || takeover(&dir, 1))
        };
        let b = {
            let dir = run_dir.clone();
            thread::spawn(move || takeover(&dir, 2))
        };
        let results = [a.join().expect("join"), b.join().expect("join")];
        let winners = results.iter().filter(|r| r.is_ok()).count();
        assert_eq!(winners, 1, "exactly one taker owns the listener");
        assert!(seen.try_recv().is_ok());
        handover.stop();
    }

    #[test]
    fn a_taker_that_hangs_up_before_got_leaves_the_daemon_serving() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        let (tcp, port) = listener();
        let (handed, mut seen) = mpsc::unbounded_channel();
        let handover = serve_handover(&run_dir, port, &tcp, handed, no_log());
        let stream = UnixStream::connect(run_dir.join(HANDOVER_SOCKET)).expect("connect");
        (&stream).write_all(b"takeover 42\n").expect("write");
        let mut data = [0u8; 128];
        let (count, fds) = recv_fds(&stream, &mut data).expect("recv");
        assert_eq!(
            std::str::from_utf8(&data[..count]).expect("utf8"),
            &format!("ok {port}\n")
        );
        assert_eq!(fds.len(), 1);
        drop(fds);
        drop(stream); // hang up without got
        thread::sleep(Duration::from_millis(100));
        assert!(seen.try_recv().is_err(), "no handover happened");
        assert!(
            run_dir.join(HANDOVER_SOCKET).exists(),
            "the daemon keeps its socket"
        );
        assert!(
            takeover(&run_dir, 43).is_ok(),
            "a later taker still gets the socket"
        );
        handover.stop();
    }

    #[test]
    fn a_question_that_is_no_takeover_keeps_the_daemon_serving() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        let (tcp, port) = listener();
        let (handed, _seen) = mpsc::unbounded_channel();
        let handover = serve_handover(&run_dir, port, &tcp, handed, no_log());
        let mut stream = UnixStream::connect(run_dir.join(HANDOVER_SOCKET)).expect("connect");
        stream.write_all(b"not a takeover\n").expect("write");
        let mut text = String::new();
        let _ = stream.read_to_string(&mut text);
        assert!(text.is_empty());
        assert!(
            takeover(&run_dir, 1).is_ok(),
            "a proper question afterwards still works"
        );
        handover.stop();
    }

    #[test]
    fn a_missing_socket_is_a_named_error() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        let error = takeover(&run_dir, 1).expect_err("no daemon");
        assert!(matches!(error, HandoverError::NoDaemon { .. }));
        assert!(
            error
                .to_string()
                .starts_with("no daemon to take over from at "),
            "{error}"
        );
    }

    #[test]
    fn an_answer_it_cannot_use_is_refused() {
        // A fake daemon: answers without a descriptor, then with two.
        for (send, want) in [
            (0usize, "handed over 0 sockets"),
            (2, "handed over 2 sockets"),
        ] {
            let home = TempHome::new();
            let run_dir = home.root().join("run");
            std::fs::create_dir_all(&run_dir).expect("mkdir");
            let path = run_dir.join(HANDOVER_SOCKET);
            let server = std::os::unix::net::UnixListener::bind(&path).expect("bind");
            thread::spawn(move || {
                let (stream, _) = server.accept().expect("accept");
                let mut reader = BufReader::new(stream.try_clone().expect("clone"));
                let mut line = String::new();
                reader.read_line(&mut line).expect("read");
                let tcp = TcpListener::bind("127.0.0.1:0").expect("bind");
                let raw = tcp.as_raw_fd();
                let fds: Vec<RawFd> = vec![raw; send];
                send_fds(&stream, b"ok 1\n", &fds).expect("send");
            });
            let error = takeover(&run_dir, 1).expect_err("unusable");
            assert!(error.to_string().contains(want), "{error}");
        }
    }

    #[test]
    fn an_answer_without_a_port_number_is_refused() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        std::fs::create_dir_all(&run_dir).expect("mkdir");
        let path = run_dir.join(HANDOVER_SOCKET);
        let server = std::os::unix::net::UnixListener::bind(&path).expect("bind");
        thread::spawn(move || {
            let (stream, _) = server.accept().expect("accept");
            let mut reader = BufReader::new(stream.try_clone().expect("clone"));
            let mut line = String::new();
            reader.read_line(&mut line).expect("read");
            let tcp = TcpListener::bind("127.0.0.1:0").expect("bind");
            let raw = tcp.as_raw_fd();
            send_fds(&stream, b"ok nope\n", &[raw]).expect("send");
        });
        let error = takeover(&run_dir, 1).expect_err("unusable");
        assert!(
            error.to_string().contains("answered \"ok nope\""),
            "{error}"
        );
    }

    #[test]
    fn a_retired_daemon_leaves_its_takers_socket_alone() {
        let home = TempHome::new();
        let run_dir = home.root().join("run");
        let (tcp, port) = listener();
        let (handed, _seen) = mpsc::unbounded_channel();
        let old = serve_handover(&run_dir, port, &tcp, handed, no_log());
        let taken = takeover(&run_dir, 777).expect("takeover");
        // The taker opens its own socket, then the old daemon's late stop runs.
        let (handed_two, _seen_two) = mpsc::unbounded_channel();
        let new_handover = serve_handover(&run_dir, port, &taken, handed_two, no_log());
        old.stop();
        assert!(
            run_dir.join(HANDOVER_SOCKET).exists(),
            "the late stop must not remove the taker's socket"
        );
        new_handover.stop();
    }

    #[test]
    fn a_socket_path_past_the_limit_is_logged_and_skipped() {
        let home = TempHome::new();
        let run_dir = home.root().join("x".repeat(120)).join("run");
        let (tcp, port) = listener();
        let (handed, _seen) = mpsc::unbounded_channel();
        let (store, log) = lines();
        let handover = serve_handover(&run_dir, port, &tcp, handed, log);
        assert!(
            store
                .lock()
                .expect("lines")
                .iter()
                .any(|line| line.starts_with("huddle-router: handover socket:")),
            "{store:?}"
        );
        handover.stop();
    }
}
