//! K7: `huddle provider <p>` guides a terminal user through the key and the setup. The binary
//! runs on the slave side of a pseudo-terminal; the key check goes to an in-process fake.

use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::{
    fs,
    os::unix::io::RawFd,
    process::{Child, Command, Stdio},
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    time::{Duration, Instant},
};

const FAKE_KEY: &str = "abcdefghijklmnop";
const PROMPT: &str = "Paste your key, then press Enter: ";
const WAIT: Duration = Duration::from_secs(20);

/// A child with a pseudo-terminal for stdin, stdout and stderr.
struct Pty {
    master: RawFd,
    slave: RawFd,
    child: Child,
    seen: String,
}

fn open_pty() -> (RawFd, RawFd) {
    let (mut master, mut slave) = (0, 0);
    let rc = unsafe {
        libc::openpty(
            &mut master,
            &mut slave,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            std::ptr::null_mut(),
        )
    };
    assert_eq!(rc, 0, "openpty");
    (master, slave)
}

impl Pty {
    fn spawn(home: &TempHome, port: u16, args: &[&str], extra: &[(&str, &str)]) -> Pty {
        let (master, slave) = open_pty();
        let dup = |fd: RawFd| unsafe { Stdio::from_raw_fd_dup(fd) };
        let mut command = Command::new(env!("CARGO_BIN_EXE_huddle"));
        command
            .args(args)
            .env_clear()
            .envs(home.env())
            .env("HUDDLE_ROUTER_PORT", port.to_string())
            .envs(extra.iter().copied())
            .stdin(dup(slave))
            .stdout(dup(slave))
            .stderr(dup(slave));
        let child = command.spawn().expect("spawn");
        Pty {
            master,
            slave,
            child,
            seen: String::new(),
        }
    }

    fn send(&self, bytes: &[u8]) {
        for byte in bytes {
            let written = unsafe { libc::write(self.master, [*byte].as_ptr().cast(), 1) };
            assert_eq!(written, 1, "write to the terminal");
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    /// Reads what the child printed until `needle` shows or the budget ends.
    fn read_until(&mut self, needle: &str) -> bool {
        let start = Instant::now();
        let from = 0;
        while start.elapsed() < WAIT {
            if self.seen[from..].contains(needle) {
                return true;
            }
            self.pump(Duration::from_millis(100));
            if self.seen[from..].contains(needle) {
                return true;
            }
            if self.child.try_wait().expect("wait").is_some() {
                self.pump(Duration::from_millis(100));
                return self.seen[from..].contains(needle);
            }
        }
        false
    }

    fn pump(&mut self, wait: Duration) {
        let mut poll = libc::pollfd {
            fd: self.master,
            events: libc::POLLIN,
            revents: 0,
        };
        let ready = unsafe { libc::poll(&mut poll, 1, wait.as_millis() as i32) };
        if ready > 0 {
            let mut buf = [0u8; 4096];
            let count = unsafe { libc::read(self.master, buf.as_mut_ptr().cast(), buf.len()) };
            if count > 0 {
                self.seen
                    .push_str(&String::from_utf8_lossy(&buf[..count as usize]));
            }
        }
    }

    /// The child's exit code, after draining its output.
    fn finish(&mut self) -> i32 {
        let start = Instant::now();
        while start.elapsed() < WAIT {
            self.pump(Duration::from_millis(50));
            if let Some(status) = self.child.try_wait().expect("wait") {
                self.pump(Duration::from_millis(50));
                return status.code().unwrap_or(-1);
            }
        }
        let _ = self.child.kill();
        panic!("the child never exited; saw: {}", self.seen);
    }

    /// The local flags, without PENDIN, which the kernel sets by itself on a mode change.
    fn lflag(&self) -> libc::tcflag_t {
        let mut state = unsafe { std::mem::zeroed::<libc::termios>() };
        assert_eq!(unsafe { libc::tcgetattr(self.slave, &mut state) }, 0);
        state.c_lflag & !libc::PENDIN
    }
}

impl Drop for Pty {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        unsafe {
            libc::close(self.master);
            libc::close(self.slave);
        }
    }
}

trait DupStdio {
    unsafe fn from_raw_fd_dup(fd: RawFd) -> Stdio;
}

impl DupStdio for Stdio {
    unsafe fn from_raw_fd_dup(fd: RawFd) -> Stdio {
        use std::os::unix::io::FromRawFd;
        unsafe { Stdio::from_raw_fd(libc::dup(fd)) }
    }
}

fn run_plain(home: &TempHome, port: u16, args: &[&str], extra: &[(&str, &str)]) {
    let _ = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .envs(extra.iter().copied())
        .stdin(Stdio::null())
        .output();
}

fn stop(home: &TempHome, port: u16) {
    run_plain(home, port, &["router", "stop"], &[]);
}

/// The ledger `/huddle:setup` leaves; any file at the path counts.
fn mark_installed(home: &TempHome) {
    let state = home.root().join(".local/state/huddle");
    fs::create_dir_all(&state).expect("state");
    fs::write(state.join("settings-ledger.toml"), "{}").expect("ledger");
}

fn store_key(home: &TempHome, port: u16) {
    let mut command = Command::new(env!("CARGO_BIN_EXE_huddle"));
    command
        .args(["provider", "zai", "key"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::piped())
        .stdout(Stdio::null());
    let mut child = command.spawn().expect("spawn");
    {
        use std::io::Write;
        let mut stdin = child.stdin.take().expect("stdin");
        writeln!(stdin, "{FAKE_KEY}").expect("write");
    }
    assert!(child.wait().expect("wait").success());
}

fn fake(plan: impl Fn(usize) -> u16 + Send + Sync + 'static) -> FakeUpstream {
    let counter = Arc::new(AtomicUsize::new(0));
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |_, _| {
            let n = counter.fetch_add(1, Ordering::SeqCst) + 1;
            Step::answer(plan(n), "{}")
        }))
}

#[test]
fn k7_a_new_key_is_masked_and_finishes_the_setup() {
    let home = TempHome::new();
    let port = free_port();
    mark_installed(&home);
    let upstream = fake(|_| 200);
    let mut pty = Pty::spawn(
        &home,
        port,
        &["provider", "zai"],
        &[("ZAI_BASE_URL", upstream.url().as_str())],
    );
    assert!(pty.read_until(PROMPT), "{}", pty.seen);
    pty.send(FAKE_KEY.as_bytes());
    assert!(pty.read_until(&"*".repeat(16)), "{}", pty.seen);
    pty.send(b"\r");
    let code = pty.finish();
    let seen = pty.seen.replace("\r\n", "\n");
    stop(&home, port);
    assert_eq!(code, 0, "{seen}");
    assert!(!seen.contains(FAKE_KEY), "the key must never print: {seen}");
    assert!(
        seen.starts_with(
            "Z.ai GLM has no key yet.\nGet one at https://z.ai/manage-apikey/apikey-list\n\n"
        ),
        "{seen}"
    );
    for line in [
        "✓ Stored in the key store",
        "✓ Z.ai GLM accepted the key",
        "✓ huddle-router routes zai",
        "✓ /model: glm-5.3, glm-5.3-flash",
        "zai is ready. In Claude Code, pick a GLM model with /model.",
    ] {
        assert!(seen.contains(line), "missing {line:?} in {seen}");
    }
    let providers =
        fs::read_to_string(home.root().join(".config/huddle/providers.toml")).expect("providers");
    assert!(providers.contains("zai"), "{providers}");
    let stored = fs::read_to_string(home.root().join(".config/zai-plugin-cc/env")).expect("key");
    assert!(stored.contains(FAKE_KEY));
}

#[test]
fn k7_a_refused_key_asks_again_and_stores_nothing() {
    let home = TempHome::new();
    let port = free_port();
    mark_installed(&home);
    let upstream = fake(|n| if n == 1 { 401 } else { 200 });
    let mut pty = Pty::spawn(
        &home,
        port,
        &["provider", "zai"],
        &[("ZAI_BASE_URL", upstream.url().as_str())],
    );
    assert!(pty.read_until(PROMPT), "{}", pty.seen);
    pty.send(b"zzzzzzzzzzzzzzzz\r");
    assert!(
        pty.read_until(
            "✗ Z.ai GLM refused this key. Check that you copied all of it, or create a new one at https://z.ai/manage-apikey/apikey-list."
        ),
        "{}",
        pty.seen
    );
    assert!(
        !home.root().join(".config/zai-plugin-cc/env").exists(),
        "a refused key is not stored"
    );
    assert!(pty.read_until(PROMPT), "{}", pty.seen);
    pty.send(format!("{FAKE_KEY}\r").as_bytes());
    let code = pty.finish();
    let seen = pty.seen.clone();
    stop(&home, port);
    assert_eq!(code, 0, "{seen}");
    assert!(!seen.contains("zzzzzzzz"), "{seen}");
    assert!(seen.contains("zai is ready."), "{seen}");
}

#[test]
fn k7_three_refused_keys_exit_six() {
    let home = TempHome::new();
    let port = free_port();
    mark_installed(&home);
    let upstream = fake(|_| 401);
    let mut pty = Pty::spawn(
        &home,
        port,
        &["provider", "zai"],
        &[("ZAI_BASE_URL", upstream.url().as_str())],
    );
    for round in 1..=3 {
        let start = Instant::now();
        while pty.seen.matches(PROMPT).count() < round && start.elapsed() < WAIT {
            pty.pump(Duration::from_millis(100));
        }
        assert_eq!(pty.seen.matches(PROMPT).count(), round, "{}", pty.seen);
        pty.send(b"zzzzzzzzzzzzzzzz\r");
        let start = Instant::now();
        while pty.seen.matches("refused this key").count() < round && start.elapsed() < WAIT {
            pty.pump(Duration::from_millis(100));
        }
    }
    assert_eq!(pty.finish(), 6, "{}", pty.seen);
    assert!(!home.root().join(".config/zai-plugin-cc/env").exists());
}

#[test]
fn k7_ctrl_c_stores_nothing_and_restores_the_terminal() {
    let home = TempHome::new();
    let port = free_port();
    mark_installed(&home);
    let mut pty = Pty::spawn(&home, port, &["provider", "zai"], &[]);
    let before = pty.lflag();
    assert!(pty.read_until(PROMPT), "{}", pty.seen);
    // The prompt is written just before the mode changes, so the mode is polled.
    let start = Instant::now();
    while pty.lflag() == before && start.elapsed() < WAIT {
        pty.pump(Duration::from_millis(20));
    }
    assert_ne!(pty.lflag(), before, "the prompt changes the terminal mode");
    pty.send(b"abc\x03");
    let code = pty.finish();
    assert_eq!(code, 130, "{}", pty.seen);
    assert!(
        pty.seen.contains("Stopped. Nothing was stored."),
        "{}",
        pty.seen
    );
    assert_eq!(pty.lflag(), before, "the terminal mode comes back");
    assert!(!home.root().join(".config/zai-plugin-cc/env").exists());
}

#[test]
fn k7_backspace_removes_a_character_and_its_star() {
    let home = TempHome::new();
    let port = free_port();
    mark_installed(&home);
    let upstream = fake(|_| 200);
    let mut pty = Pty::spawn(
        &home,
        port,
        &["provider", "zai"],
        &[("ZAI_BASE_URL", upstream.url().as_str())],
    );
    assert!(pty.read_until(PROMPT), "{}", pty.seen);
    pty.send(b"XX\x7f\x7f");
    pty.send(FAKE_KEY.as_bytes());
    pty.send(b"\r");
    let code = pty.finish();
    let seen = pty.seen.clone();
    stop(&home, port);
    assert_eq!(code, 0, "{seen}");
    assert!(seen.contains("**\x08 \x08\x08 \x08"), "{seen:?}");
    let stored = fs::read_to_string(home.root().join(".config/zai-plugin-cc/env")).expect("key");
    assert!(
        stored.contains(FAKE_KEY) && !stored.contains('X'),
        "{stored}"
    );
}

#[test]
fn k7_a_stored_key_shows_the_menu_and_quit_exits_zero() {
    let home = TempHome::new();
    let port = free_port();
    store_key(&home, port);
    let mut pty = Pty::spawn(&home, port, &["provider", "zai"], &[]);
    assert!(pty.read_until("Choose 1-4: "), "{}", pty.seen);
    let seen = pty.seen.replace("\r\n", "\n");
    assert_eq!(
        seen,
        "Z.ai GLM  key stored · off\n\n  1  Replace the key\n  2  Usage: tokens and cost\n  3  Remove zai: key, routing, /model entries, agents\n  4  Quit\n\nChoose 1-4: "
    );
    pty.send(b"9\r");
    assert!(
        pty.read_until("Choose 1-4: "),
        "a wrong choice asks again: {}",
        pty.seen
    );
    pty.send(b"4\r");
    assert_eq!(pty.finish(), 0, "{}", pty.seen);
    assert!(!pty.seen.contains(FAKE_KEY));
}

#[test]
fn k7_off_a_terminal_it_prints_the_next_steps_and_never_waits() {
    let home = TempHome::new();
    let port = free_port();
    let start = Instant::now();
    let output = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["provider", "zai"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::null())
        .output()
        .expect("run");
    assert!(start.elapsed() < Duration::from_secs(2));
    assert_eq!(output.status.code(), Some(6), "{output:?}");
    assert_eq!(
        String::from_utf8_lossy(&output.stdout),
        "zai setup: needs a key.\nIn Claude Code: /huddle:zai:setup opens a key page in your browser.\nIn a terminal: huddle provider zai\n"
    );
}

#[test]
fn k7_off_a_terminal_with_a_key_prints_the_state_and_the_actions() {
    let home = TempHome::new();
    let port = free_port();
    store_key(&home, port);
    let output = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["provider", "zai"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::null())
        .output()
        .expect("run");
    assert_eq!(output.status.code(), Some(0), "{output:?}");
    assert_eq!(
        String::from_utf8_lossy(&output.stdout),
        "Z.ai GLM  key stored · off\n\n  huddle provider zai usage    tokens and cost\n  huddle provider zai remove   turn zai off\n"
    );
}

#[test]
fn k7_a_piped_key_is_still_stored() {
    let home = TempHome::new();
    let port = free_port();
    store_key(&home, port);
    let stored = fs::read_to_string(home.root().join(".config/zai-plugin-cc/env")).expect("key");
    assert!(stored.contains(FAKE_KEY));
}
