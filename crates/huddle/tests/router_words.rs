use huddle_testkit::{
    ports::{free_port, listen},
    temp_home::TempHome,
};
use std::fs;
use std::io::{Read as _, Write as _};
use std::net::TcpListener;
use std::os::unix::process::CommandExt;
use std::process::{Child, Command, Output, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;
use std::time::Duration;

fn huddle(home: &TempHome, port: u16, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .stdin(Stdio::null())
        .output()
        .expect("run")
}

fn stdout(output: &Output) -> String {
    String::from_utf8_lossy(&output.stdout).into_owned()
}

fn stderr(output: &Output) -> String {
    String::from_utf8_lossy(&output.stderr).into_owned()
}

#[test]
fn no_word_is_a_usage_error() {
    let home = TempHome::new();
    let output = huddle(&home, free_port(), &["router"]);
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(
        stderr(&output),
        "usage: huddle router serve [--takeover] | start | status [--json] | stop | log\n"
    );
}

#[test]
fn a_bad_port_is_exit_2_with_the_shared_text() {
    let home = TempHome::new();
    let output = Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(["router", "status"])
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", "abc")
        .output()
        .expect("run");
    assert_eq!(output.status.code(), Some(2));
    assert_eq!(
        stderr(&output),
        "huddle-router: $HUDDLE_ROUTER_PORT (\"abc\") is not a port\n"
    );
}

#[test]
fn status_with_nothing_running_is_exit_6() {
    let home = TempHome::new();
    let port = free_port();
    let plain = huddle(&home, port, &["router", "status"]);
    assert_eq!(plain.status.code(), Some(6));
    assert_eq!(stderr(&plain), "huddle-router: not running\n");
    let json = huddle(&home, port, &["router", "status", "--json"]);
    assert_eq!(json.status.code(), Some(6));
    assert_eq!(stdout(&json), "{\"running\":false}\n");
}

#[test]
fn stop_with_nothing_running_is_exit_6() {
    let home = TempHome::new();
    let output = huddle(&home, free_port(), &["router", "stop"]);
    assert_eq!(output.status.code(), Some(6));
    assert_eq!(stdout(&output), "huddle-router: not running\n");
}

#[test]
fn start_serves_status_names_it_a_second_serve_exits_zero_and_stop_stops_it() {
    let home = TempHome::new();
    let port = free_port();
    let started = huddle(&home, port, &["router", "start"]);
    assert_eq!(started.status.code(), Some(0), "{started:?}");
    let _stop = StopOnDrop(&home, port);
    assert_eq!(
        stdout(&started),
        format!("huddle-router: http://127.0.0.1:{port}\n")
    );

    let status = huddle(&home, port, &["router", "status", "--json"]);
    assert_eq!(status.status.code(), Some(0), "{status:?}");
    let parsed: serde_json::Value = serde_json::from_str(&stdout(&status)).expect("json");
    assert_eq!(parsed["running"], true);
    assert_eq!(parsed["name"], "huddle-router");
    assert_eq!(parsed["port"], port);

    let plain = huddle(&home, port, &["router", "status"]);
    assert_eq!(plain.status.code(), Some(0));
    assert!(
        stdout(&plain).starts_with(&format!("huddle-router: http://127.0.0.1:{port}\npid ")),
        "{}",
        stdout(&plain)
    );

    let second = huddle(&home, port, &["router", "serve"]);
    assert_eq!(second.status.code(), Some(0), "{second:?}");
    assert!(
        stdout(&second).starts_with("huddle-router: already served by pid "),
        "{}",
        stdout(&second)
    );

    let stopped = huddle(&home, port, &["router", "stop"]);
    assert_eq!(stopped.status.code(), Some(0), "{stopped:?}");
    assert!(
        stdout(&stopped).starts_with("huddle-router: stopped (pid "),
        "{}",
        stdout(&stopped)
    );
    let after = huddle(&home, port, &["router", "status"]);
    assert_eq!(after.status.code(), Some(6));
}

#[test]
fn a_serve_that_cannot_listen_names_the_port_with_one_prefix() {
    let home = TempHome::new();
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let output = huddle(&home, port, &["router", "serve"]);
    assert_eq!(output.status.code(), Some(1));
    let err = stderr(&output);
    assert!(
        err.starts_with(&format!("huddle-router: cannot listen on 127.0.0.1:{port}")),
        "{err}"
    );
    assert!(!err.starts_with("huddle-router: huddle-router:"), "{err}");
}

/// A stand-in daemon: every health probe on the port gets a 200 naming huddle-router,
/// until dropped.
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

/// A process stop must leave alone; killed on drop whatever the test decided.
struct Sleeper(Child);

impl Sleeper {
    fn start() -> Sleeper {
        Sleeper(Command::new("/bin/sleep").arg("30").spawn().expect("sleep"))
    }
}

impl Drop for Sleeper {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// Stops the router this test started, even when an assert fails first.
struct StopOnDrop<'a>(&'a TempHome, u16);

impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        let _ = huddle(self.0, self.1, &["router", "stop"]);
    }
}

fn write_record(home: &TempHome, port: u16, pid: u32) {
    let run_dir = home.root().join("r/huddle");
    fs::create_dir_all(&run_dir).expect("run dir");
    let record = format!(
        "pid = {pid}\nport = {port}\nversion = \"1.0.0\"\nstarted = \"2026-11-09T00:00:00Z\"\n"
    );
    fs::write(run_dir.join("router.toml"), record).expect("record");
}

/// Runs a word in its own process group, so a stray group signal reaches only the word.
fn huddle_in_own_group(home: &TempHome, port: u16, args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .process_group(0)
        .stdin(Stdio::null())
        .output()
        .expect("run")
}

#[test]
fn stop_sends_no_signal_when_the_record_names_no_live_daemon() {
    let home = TempHome::new();
    let listener = listen();
    let port = listener.local_addr().expect("addr").port();
    let _fake = HealthFake::start(listener);
    write_record(&home, port, 0);
    let output = huddle_in_own_group(&home, port, &["router", "stop"]);
    assert_eq!(output.status.code(), Some(6), "{output:?}");
    assert_eq!(stdout(&output), "huddle-router: not running\n");
}

#[test]
fn stop_sends_no_signal_when_the_record_names_another_port() {
    let home = TempHome::new();
    let listener = listen();
    let port = listener.local_addr().expect("addr").port();
    let other = free_port();
    assert_ne!(port, other);
    let _fake = HealthFake::start(listener);
    let mut sleeper = Sleeper::start();
    write_record(&home, other, sleeper.0.id());
    let output = huddle_in_own_group(&home, port, &["router", "stop"]);
    assert_eq!(output.status.code(), Some(6), "{output:?}");
    assert_eq!(stdout(&output), "huddle-router: not running\n");
    thread::sleep(Duration::from_millis(200));
    assert!(
        sleeper.0.try_wait().expect("poll").is_none(),
        "stop signalled the pid the record names"
    );
}

/// A handover moves the record between daemons: the served port keeps answering
/// while no record names it, and a stop inside that window must wait for the
/// fresh record and signal its owner.
#[test]
fn stop_waits_out_a_handover_and_signals_the_fresh_record() {
    let home = TempHome::new();
    let port = free_port();
    let started = huddle(&home, port, &["router", "start"]);
    assert_eq!(started.status.code(), Some(0), "{}", stderr(&started));
    for _ in 0..100 {
        if huddle(&home, port, &["router", "status"]).status.code() == Some(0) {
            break;
        }
        thread::sleep(Duration::from_millis(50));
    }

    // The handover window: the port answers while no record names the daemon.
    let record = home.root().join("r/huddle/router.toml");
    let parked = home.root().join("r/huddle/router.parked");
    fs::rename(&record, &parked).expect("park record");
    let restorer = thread::spawn(move || {
        thread::sleep(Duration::from_millis(200));
        fs::rename(&parked, &record).expect("restore record");
    });

    let stopped = huddle(&home, port, &["router", "stop"]);
    restorer.join().expect("restorer");
    assert_eq!(
        stopped.status.code(),
        Some(0),
        "stdout: {} | stderr: {}",
        stdout(&stopped),
        stderr(&stopped)
    );
    assert!(stdout(&stopped).starts_with("huddle-router: stopped (pid "));
    assert_eq!(
        huddle(&home, port, &["router", "status"]).status.code(),
        Some(6)
    );
}
