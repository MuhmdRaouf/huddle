use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::net::{SocketAddr, TcpStream};
use std::process::{Command, Output, Stdio};
use std::thread;
use std::time::{Duration, Instant};

struct Fixture {
    home: TempHome,
    port: u16,
}

impl Fixture {
    fn new() -> Fixture {
        let home = TempHome::new();
        let router = home.root().join(".config/huddle");
        fs::create_dir_all(&router).expect("mkdir");
        fs::write(router.join("providers.toml"), "providers = [\"zai\"]\n").expect("write");
        Fixture {
            home,
            port: free_port(),
        }
    }

    fn command(&self, args: &[&str]) -> Command {
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_huddle"));
        cmd.args(args)
            .env_clear()
            .envs(self.home.env())
            .env("HUDDLE_ROUTER_PORT", self.port.to_string())
            .env("ZAI_API_KEY", "abcdefghijklmnop");
        cmd
    }

    fn run(&self, args: &[&str]) -> Output {
        self.command(args).output().expect("run huddle")
    }

    fn wait_port(&self, want_open: bool) -> bool {
        let addr: SocketAddr = format!("127.0.0.1:{}", self.port).parse().expect("addr");
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline {
            if TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok() == want_open {
                return true;
            }
            thread::sleep(Duration::from_millis(50));
        }
        false
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = self.run(&["router", "stop"]);
    }
}

#[test]
fn status_with_nothing_running_exits_6() {
    let f = Fixture::new();
    let plain = f.run(&["router", "status"]);
    assert_eq!(plain.status.code(), Some(6));
    assert_eq!(
        String::from_utf8_lossy(&plain.stderr),
        "huddle-router: not running\n"
    );

    let json = f.run(&["router", "status", "--json"]);
    assert_eq!(json.status.code(), Some(6));
    assert_eq!(
        String::from_utf8_lossy(&json.stdout).trim(),
        "{\"running\":false}"
    );
}

#[test]
fn start_then_status_then_stop() {
    let f = Fixture::new();
    let start = f.run(&["router", "start"]);
    assert_eq!(
        start.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&start.stderr)
    );
    let line = format!("huddle-router: http://127.0.0.1:{} (zai)\n", f.port);
    assert_eq!(String::from_utf8_lossy(&start.stdout), line);
    assert!(f.wait_port(true));

    let status = f.run(&["router", "status"]);
    assert_eq!(status.status.code(), Some(0));
    let text = String::from_utf8_lossy(&status.stdout);
    assert!(
        text.starts_with(&format!("huddle-router: http://127.0.0.1:{}\n", f.port)),
        "{text}"
    );
    assert!(text.contains("providers zai"), "{text}");

    let json = f.run(&["router", "status", "--json"]);
    let doc: serde_json::Value = serde_json::from_slice(&json.stdout).expect("json");
    assert_eq!(doc["running"], true);
    assert_eq!(doc["name"], "huddle-router");
    assert_eq!(doc["providers"], serde_json::json!(["zai"]));

    let stop = f.run(&["router", "stop"]);
    assert_eq!(
        stop.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&stop.stderr)
    );
    assert!(f.wait_port(false));
}

#[test]
fn a_second_start_spawns_nothing() {
    let f = Fixture::new();
    let first = f.run(&["router", "start"]);
    assert_eq!(
        first.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&first.stderr)
    );
    assert!(f.wait_port(true));
    let before = fs::read_to_string(f.home.root().join(".local/state/huddle/run/router.toml"))
        .expect("record");

    let second = f.run(&["router", "start"]);
    assert_eq!(second.status.code(), Some(0));
    assert_eq!(
        fs::read_to_string(f.home.root().join(".local/state/huddle/run/router.toml"))
            .expect("record"),
        before
    );
}

#[test]
fn a_second_serve_exits_zero_and_names_the_holder() {
    let f = Fixture::new();
    let first = f.run(&["router", "start"]);
    assert_eq!(
        first.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&first.stderr)
    );
    assert!(f.wait_port(true));

    let second = f
        .command(&["router", "serve"])
        .stdin(Stdio::null())
        .output()
        .expect("run serve");

    assert_eq!(second.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&second.stdout);
    assert!(
        stdout.starts_with("huddle-router: already served by pid "),
        "{stdout}"
    );
}

#[test]
fn a_bad_port_is_refused_by_every_word() {
    let f = Fixture::new();
    for word in ["serve", "start", "status"] {
        let out = f
            .command(&["router", word])
            .env("HUDDLE_ROUTER_PORT", "notaport")
            .output()
            .expect("run");
        assert_eq!(out.status.code(), Some(2), "{word}");
        assert_eq!(
            String::from_utf8_lossy(&out.stderr),
            "huddle-router: $HUDDLE_ROUTER_PORT (\"notaport\") is not a port\n",
            "{word}"
        );
    }
}

#[test]
fn an_unknown_router_word_prints_usage_and_exits_2() {
    let f = Fixture::new();
    let out = f.run(&["router", "frob"]);
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).starts_with("usage: huddle router"));
}
