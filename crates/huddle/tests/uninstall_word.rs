use huddle_daemon::singleton::holder;
use huddle_procs::live::is_alive;
use huddle_testkit::ports::{free_port, listen};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::io::{Read as _, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::process::{Child, Command, Output};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;
use std::time::{Duration, Instant};

fn huddle(home: &TempHome, port: u16, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .output()
        .expect("run huddle")
}

fn port_open(port: u16) -> bool {
    let addr: SocketAddr = format!("127.0.0.1:{port}").parse().expect("addr");
    TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok()
}

fn wait_closed(port: u16) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if !port_open(port) {
            return true;
        }
        thread::sleep(Duration::from_millis(50));
    }
    false
}

struct StopOnDrop<'a>(&'a TempHome, u16);
impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        let _ = huddle(self.0, self.1, &["router", "stop"]);
    }
}

#[test]
fn uninstall_restores_settings_byte_for_byte_and_stops_the_router() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let claude_dir = home.root().join(".claude");
    fs::create_dir_all(&claude_dir).expect("mkdir");
    let settings = claude_dir.join("settings.json");
    let original = "{\n      \"theme\":   \"dark\"\n}\n";
    fs::write(&settings, original).expect("write");

    let install = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        install.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&install.stderr)
    );
    assert_ne!(fs::read_to_string(&settings).expect("read"), original);

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(
        out.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(stdout.contains("restored settings.json"), "{stdout}");
    assert_eq!(fs::read_to_string(&settings).expect("read"), original);
    assert!(wait_closed(port), "the router stopped");
    assert!(
        !home
            .root()
            .join(".local/state/huddle/settings-ledger.toml")
            .exists()
    );
}

#[test]
fn uninstall_twice_says_nothing_to_undo_and_exits_zero() {
    let home = TempHome::new();
    let port = free_port();

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        stdout.contains("nothing to undo in settings.json"),
        "{stdout}"
    );
}

#[test]
fn t2_install_leaves_toml_ledgers_and_uninstall_restores_the_bytes() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    let claude_dir = home.root().join(".claude");
    fs::create_dir_all(&claude_dir).expect("mkdir");
    let settings = claude_dir.join("settings.json");
    let original =
        "{\n  \"env\": {\"HTTPS_PROXY\": \"http://corp:3128\"},\n  \"theme\":   \"dark\"\n}\n";
    fs::write(&settings, original).expect("write");
    let state = home.root().join(".local/state/huddle");

    let install = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(install.status.code(), Some(0));
    assert!(state.join("settings-ledger.toml").exists());
    assert!(!state.join("settings-ledger.json").exists());
    assert!(state.join("settings.json.pristine").exists());

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(fs::read_to_string(&settings).expect("read"), original);
    assert!(!state.join("settings-ledger.toml").exists());
}

#[test]
fn k4_uninstall_removes_only_its_own_link() {
    let own = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&own, port);
    let link = own.root().join(".local/bin/huddle");
    assert_eq!(
        huddle(&own, port, &["setup", "--install"]).status.code(),
        Some(0)
    );
    assert!(link.symlink_metadata().is_ok(), "setup linked huddle");
    assert_eq!(huddle(&own, port, &["uninstall"]).status.code(), Some(0));
    assert!(
        link.symlink_metadata().is_err(),
        "uninstall removed the link"
    );

    let moved = TempHome::new();
    let port = free_port();
    let _stop2 = StopOnDrop(&moved, port);
    let link = moved.root().join(".local/bin/huddle");
    assert_eq!(
        huddle(&moved, port, &["setup", "--install"]).status.code(),
        Some(0)
    );
    fs::remove_file(&link).expect("unlink");
    let elsewhere = moved.root().join("elsewhere");
    std::os::unix::fs::symlink(&elsewhere, &link).expect("relink");
    assert_eq!(huddle(&moved, port, &["uninstall"]).status.code(), Some(0));
    assert_eq!(fs::read_link(&link).expect("still there"), elsewhere);
}

#[test]
fn h3_install_makes_the_mcp_token_and_uninstall_removes_it() {
    use std::os::unix::fs::PermissionsExt;
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    fs::create_dir_all(home.root().join(".claude")).expect("mkdir");
    let token = home.root().join(".local/state/huddle/mcp-token");

    let install = huddle(&home, port, &["setup", "--install"]);
    assert_eq!(
        install.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&install.stderr)
    );
    let mode = fs::metadata(&token)
        .expect("setup made the token")
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o600);
    let secret = fs::read_to_string(&token).expect("read");
    let said = String::from_utf8_lossy(&install.stdout).into_owned()
        + &String::from_utf8_lossy(&install.stderr);
    assert!(!said.contains(secret.trim()), "setup printed the token");

    let uninstall = huddle(&home, port, &["uninstall"]);
    assert_eq!(uninstall.status.code(), Some(0));
    assert!(!token.exists(), "uninstall left the token");
    assert!(
        String::from_utf8_lossy(&uninstall.stdout).contains("removed the MCP token"),
        "{}",
        String::from_utf8_lossy(&uninstall.stdout)
    );
}

fn huddle_with(home: &TempHome, port: u16, extra: &[(&str, String)], args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .envs(extra.iter().map(|(name, value)| (name, value)))
        .output()
        .expect("run huddle")
}

/// Files a used install holds outside the ledger: config, history, logs, cache, jobs.
fn scatter_files(home: &TempHome) {
    let root = home.root();
    for rel in [
        ".config/huddle/config.toml",
        ".config/huddle/providers.toml",
        ".local/state/huddle/huddle.db",
        ".local/state/huddle/router.log",
        ".local/state/huddle/jobs/repo/1/job.toml",
        ".cache/huddle/build.log",
    ] {
        let path = root.join(rel);
        fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
        fs::write(path, "x").expect("write");
    }
}

fn files_under(dir: &std::path::Path) -> Vec<std::path::PathBuf> {
    fn walk(dir: &std::path::Path, out: &mut Vec<std::path::PathBuf>) {
        let Ok(entries) = fs::read_dir(dir) else {
            return;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() && !path.is_symlink() {
                walk(&path, out);
            } else {
                out.push(path);
            }
        }
    }
    let mut out = Vec::new();
    walk(dir, &mut out);
    out
}

fn install_with_files(home: &TempHome, port: u16) {
    let install = huddle(home, port, &["setup", "--install"]);
    assert_eq!(
        install.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&install.stderr)
    );
    scatter_files(home);
}

#[test]
fn h1_uninstall_leaves_nothing() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    install_with_files(&home, port);

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    let root = home.root();
    for rel in [
        ".config/huddle",
        ".local/share/huddle",
        ".local/state/huddle",
        ".cache/huddle",
        "r/huddle",
    ] {
        assert!(
            files_under(&root.join(rel)).is_empty(),
            "{rel} still holds {:?}",
            files_under(&root.join(rel))
        );
    }
    assert!(root.join(".config").is_dir(), "the parent folder stays");
    assert!(
        root.join(".local/state").is_dir(),
        "the parent folder stays"
    );
}

#[test]
fn h1_uninstall_names_each_removed_folder() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    install_with_files(&home, port);

    let out = huddle(&home, port, &["uninstall"]);

    let stdout = String::from_utf8_lossy(&out.stdout);
    let root = home.root().display();
    for rel in [
        ".config/huddle",
        ".local/share/huddle",
        ".local/state/huddle",
        ".cache/huddle",
    ] {
        let line = format!("removed {root}/{rel}");
        assert!(
            stdout.lines().any(|found| found == line),
            "{line}\n{stdout}"
        );
    }
}

#[test]
fn h1_uninstall_refuses_a_folder_that_is_not_huddles() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    install_with_files(&home, port);
    let marker = home.root().join("notes.txt");
    fs::write(&marker, "mine").expect("write");
    let extra = [("HUDDLE_STATE_HOME", home.root().display().to_string())];

    let out = huddle_with(&home, port, &extra, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        stdout.contains(&format!("left in place: {}", home.root().display())),
        "{stdout}"
    );
    assert_eq!(fs::read_to_string(&marker).expect("kept"), "mine");
    assert!(home.root().join(".claude").is_dir());
    assert!(
        !home.root().join(".config/huddle").exists(),
        "the other folders still go"
    );
}

#[test]
fn h1_uninstall_does_not_follow_a_symlink_out() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    install_with_files(&home, port);
    let outside = home.root().join("outside");
    fs::create_dir_all(&outside).expect("mkdir");
    fs::write(outside.join("keep.txt"), "keep").expect("write");
    std::os::unix::fs::symlink(&outside, home.root().join(".local/state/huddle/jobs/out"))
        .expect("link");
    let whole = home.root().join("whole");
    fs::create_dir_all(&whole).expect("mkdir");
    fs::write(whole.join("keep.txt"), "keep").expect("write");
    fs::remove_dir_all(home.root().join(".cache/huddle")).expect("rm");
    std::os::unix::fs::symlink(&whole, home.root().join(".cache/huddle")).expect("link");

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(
        fs::read_to_string(outside.join("keep.txt")).expect("kept"),
        "keep"
    );
    assert_eq!(
        fs::read_to_string(whole.join("keep.txt")).expect("kept"),
        "keep"
    );
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(stdout.contains("left in place:"), "{stdout}");
    assert!(!home.root().join(".local/state/huddle").exists());
}

#[test]
fn h1_failed_restore_keeps_the_ledger() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    fs::write(
        home.root().join(".claude/settings.json"),
        "{\"theme\": \"dark\"}\n",
    )
    .expect("write");
    install_with_files(&home, port);
    let state = home.root().join(".local/state/huddle");
    let ledger = state.join("settings-ledger.toml");
    assert!(ledger.exists());
    fs::write(&ledger, "this is not a ledger = [").expect("corrupt");

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        stdout.contains(&format!("left in place: {}", state.display())),
        "{stdout}"
    );
    assert!(ledger.exists(), "the ledger stays for a retry");
    assert!(state.join("settings.json.pristine").exists());
    assert!(!home.root().join(".config/huddle").exists());
}

/// Asserts the data, state and run folders are empty, the port is closed and `router_pid` is gone.
fn assert_final(home: &TempHome, port: u16, router_pid: Option<u32>, case: &str) {
    let root = home.root();
    for rel in [".local/share/huddle", ".local/state/huddle", "r/huddle"] {
        assert!(
            files_under(&root.join(rel)).is_empty(),
            "{case}: {rel} holds {:?} 2 s after uninstall",
            files_under(&root.join(rel))
        );
    }
    assert!(!port_open(port), "{case}: the port still answers");
    if let Some(pid) = router_pid {
        assert!(!is_alive(pid), "{case}: router pid {pid} still runs");
    }
}

/// The pid holding the router lock of `home`, polled until a router holds it; none within 10 s.
fn lock_holder(home: &TempHome) -> Option<u32> {
    let lock = home.root().join("r/huddle/router.lock");
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if let Some(pid) = holder(&lock) {
            return Some(pid);
        }
        thread::sleep(Duration::from_millis(2));
    }
    None
}

/// The worker pid the router record of `home` names.
fn recorded_worker(home: &TempHome) -> Option<u32> {
    let record = fs::read_to_string(home.root().join("r/huddle/router.toml")).ok()?;
    record.lines().find_map(|line| {
        let value = line
            .strip_prefix("worker")?
            .trim_start()
            .strip_prefix('=')?;
        value.trim().parse().ok()
    })
}

#[test]
fn l67_uninstall_is_final_while_a_router_is_starting() {
    // A router coming up: its lock is held, the record and the answer may not be there yet.
    for round in 0..8 {
        let home = TempHome::new();
        let port = free_port();
        let _stop = StopOnDrop(&home, port);
        let install = huddle(&home, port, &["setup", "--install"]);
        assert_eq!(install.status.code(), Some(0), "{install:?}");
        let stop = huddle(&home, port, &["router", "stop"]);
        assert_eq!(stop.status.code(), Some(0), "{stop:?}");
        let mut start = Command::new(env!("CARGO_BIN_EXE_huddle"))
            .args(["router", "start"])
            .env_clear()
            .envs(home.env())
            .env("CLAUDE_CONFIG_DIR", home.root().join(".claude"))
            .env("HUDDLE_ROUTER_PORT", port.to_string())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .expect("spawn router start");
        let pid = lock_holder(&home);

        let out = huddle(&home, port, &["uninstall"]);

        let _ = start.wait();
        assert_eq!(out.status.code(), Some(0), "{out:?}");
        thread::sleep(Duration::from_secs(2));
        assert_final(&home, port, pid, &format!("starting round {round}"));
    }
    // Routers slow to stop, side by side: one drains a half-sent request for its whole budget,
    // one has a frozen worker the supervisor kills after its grace.
    let rounds = [
        thread::spawn(|| uninstall_while_slow_to_stop(Slow::Draining)),
        thread::spawn(|| uninstall_while_slow_to_stop(Slow::FrozenWorker)),
    ];
    for round in rounds {
        round.join().expect("slow round");
    }
}

#[derive(Debug, Clone, Copy)]
enum Slow {
    Draining,
    FrozenWorker,
}

fn uninstall_while_slow_to_stop(slow: Slow) {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    install_with_files(&home, port);
    let pid = lock_holder(&home);
    let mut held = TcpStream::connect(("127.0.0.1", port)).expect("connect");
    let half = format!(
        "POST /v1/messages HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Length: 100\r\n\r\n{{\"a\""
    );
    held.write_all(half.as_bytes())
        .expect("send half a request");
    if let Slow::FrozenWorker = slow {
        let worker = recorded_worker(&home).expect("the record names a worker");
        // SAFETY: the worker is a child of the router this test started in its own home.
        unsafe { libc::kill(worker as libc::pid_t, libc::SIGSTOP) };
    }

    let out = huddle(&home, port, &["uninstall"]);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(!stdout.contains("left in place"), "{slow:?}: {stdout}");
    thread::sleep(Duration::from_secs(2));
    assert_final(&home, port, pid, &format!("{slow:?}"));
    drop(held);
}

#[test]
fn l67_a_router_started_after_uninstall_writes_nothing() {
    let home = TempHome::new();
    let port = free_port();
    let _stop = StopOnDrop(&home, port);
    install_with_files(&home, port);
    let out = huddle(&home, port, &["uninstall"]);
    assert_eq!(out.status.code(), Some(0), "{out:?}");

    // What a hook that read the ledger just before the uninstall runs next: the start, and the
    // daemon it spawns.
    let start = huddle(
        &home,
        port,
        &["router", "start", "--by", "session-start", "--if-installed"],
    );
    let serve = huddle(
        &home,
        port,
        &["router", "serve", "--by", "session-start", "--if-installed"],
    );

    assert_eq!(start.status.code(), Some(0), "{start:?}");
    assert_eq!(serve.status.code(), Some(0), "{serve:?}");
    let root = home.root();
    for rel in [".local/share/huddle", ".local/state/huddle", "r/huddle"] {
        assert!(!root.join(rel).exists(), "{rel} exists after a late start");
    }
    assert!(!port_open(port), "no router serves the port");
}

/// A stand-in daemon: every health probe on the port gets a 200 naming huddle-router, until
/// dropped.
struct HealthFake {
    stop: Arc<AtomicBool>,
    reader: Option<thread::JoinHandle<()>>,
}

impl HealthFake {
    fn start(listener: TcpListener) -> HealthFake {
        let stop = Arc::new(AtomicBool::new(false));
        let flag = Arc::clone(&stop);
        let reader = thread::spawn(move || {
            let body = "{\"name\":\"huddle-router\",\"providers\":[\"zai\"]}";
            let answer = format!(
                "HTTP/1.1 200 OK\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
                body.len()
            );
            listener.set_nonblocking(true).expect("nonblocking");
            while !flag.load(Ordering::SeqCst) {
                match listener.accept() {
                    Ok((mut stream, _)) => {
                        let mut probe = [0u8; 512];
                        let _ = stream.read(&mut probe);
                        let _ = stream.write_all(answer.as_bytes());
                    }
                    Err(_) => thread::sleep(Duration::from_millis(10)),
                }
            }
        });
        HealthFake {
            stop,
            reader: Some(reader),
        }
    }
}

impl Drop for HealthFake {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        if let Some(reader) = self.reader.take() {
            let _ = reader.join();
        }
    }
}

/// A process the stop word must leave alone; killed on drop whatever the test decided.
struct Bystander(Child);

impl Drop for Bystander {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

fn write_record_for(home: &TempHome, port: u16, pid: u32, start: Option<&str>) {
    let run_dir = home.root().join("r/huddle");
    fs::create_dir_all(&run_dir).expect("run dir");
    let start = start.map_or(String::new(), |token| format!("start = \"{token}\"\n"));
    let record = format!(
        "pid = {pid}\nport = {port}\nversion = \"1.0.0\"\nstarted = \"2026-11-09T00:00:00Z\"\n{start}"
    );
    fs::write(run_dir.join("router.toml"), record).expect("record");
}

#[test]
fn l73_stop_never_signals_an_unproven_pid() {
    let home = TempHome::new();
    let listener = listen();
    let port = listener.local_addr().expect("addr").port();
    let _fake = HealthFake::start(listener);
    let mut sleeper = Bystander(Command::new("/bin/sleep").arg("60").spawn().expect("sleep"));
    // The right pid with a start time that is not the process's own.
    write_record_for(&home, port, sleeper.0.id(), Some("1.1"));

    let out = huddle(&home, port, &["router", "stop"]);

    assert_eq!(out.status.code(), Some(1), "{out:?}");
    assert!(
        String::from_utf8_lossy(&out.stderr).contains("cannot prove"),
        "{out:?}"
    );
    thread::sleep(Duration::from_millis(300));
    assert!(
        sleeper.0.try_wait().expect("poll").is_none(),
        "the stop word signalled a pid it had not proven"
    );
}

#[test]
fn l73_stop_ends_when_a_start_time_cannot_be_read() {
    let home = TempHome::new();
    let listener = listen();
    let port = listener.local_addr().expect("addr").port();
    let _fake = HealthFake::start(listener);
    // An exited child nobody reaped: the pid is alive to a signal probe, and its start time
    // cannot be read.
    let zombie = Command::new("/usr/bin/true").spawn().expect("true");
    thread::sleep(Duration::from_millis(300));
    write_record_for(&home, port, zombie.id(), None);
    let mut word = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "stop"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .expect("spawn the stop word");

    let deadline = Instant::now() + Duration::from_secs(15);
    let status = loop {
        if let Some(status) = word.try_wait().expect("poll") {
            break Some(status);
        }
        if Instant::now() >= deadline {
            let _ = word.kill();
            let _ = word.wait();
            break None;
        }
        thread::sleep(Duration::from_millis(50));
    };
    let mut zombie = zombie;
    let _ = zombie.wait();

    let status = status.expect("the stop word is still waiting after 15 s");
    assert_eq!(status.code(), Some(1));
}
