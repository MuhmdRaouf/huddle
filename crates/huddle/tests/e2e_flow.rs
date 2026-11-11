use huddle_testkit::ports::free_port;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::net::{SocketAddr, TcpStream};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::thread;
use std::time::{Duration, Instant};

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn pinned_version() -> String {
    let text = fs::read_to_string(repo_root().join("plugin/dist/run")).expect("read dist/run");
    text.lines()
        .find_map(|l| l.strip_prefix("version="))
        .expect("version line")
        .to_string()
}

fn this_platform() -> (&'static str, &'static str) {
    let os = match std::env::consts::OS {
        "macos" => "darwin",
        "linux" => "linux",
        other => panic!("unsupported test OS {other}"),
    };
    let arch = match std::env::consts::ARCH {
        "aarch64" => "arm64",
        "x86_64" => "amd64",
        other => panic!("unsupported test CPU {other}"),
    };
    (os, arch)
}

fn fake_release(root: &Path) -> String {
    let (os, arch) = this_platform();
    let version = pinned_version();
    let dir = root.join("release").join(format!("v{version}"));
    fs::create_dir_all(&dir).expect("mkdir release");
    let asset = format!("huddle-{os}-{arch}");
    let path = dir.join(&asset);
    fs::copy(env!("CARGO_BIN_EXE_huddle"), &path).expect("copy the built binary");
    fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).expect("chmod");
    let out = Command::new("sh")
        .arg("-c")
        .arg("sha256sum \"$0\" 2>/dev/null || shasum -a 256 \"$0\"")
        .arg(&path)
        .output()
        .expect("sha");
    let digest = String::from_utf8_lossy(&out.stdout)
        .split_whitespace()
        .next()
        .expect("digest")
        .to_string();
    fs::write(dir.join("SHA256SUMS"), format!("{digest}  {asset}\n")).expect("write sums");
    format!("file://{}", root.join("release").display())
}

struct Env<'a> {
    home: &'a TempHome,
    port: u16,
    base: String,
}

impl Env<'_> {
    fn command(&self, program: &Path) -> Command {
        let mut cmd = Command::new(program);
        cmd.env_clear()
            .envs(self.home.env())
            .env("CLAUDE_CONFIG_DIR", self.home.root().join(".claude"))
            .env("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
            .env("HUDDLE_ROUTER_PORT", self.port.to_string())
            .env("HUDDLE_DOWNLOAD_BASE", &self.base);
        cmd
    }

    fn bootstrap(&self, args: &[&str]) -> Output {
        self.command(Path::new("/bin/sh"))
            .arg(repo_root().join("plugin/dist/run"))
            .args(args)
            .output()
            .expect("run bootstrap")
    }

    fn installed(&self, args: &[&str]) -> Output {
        let link = self.home.root().join(".local/share/huddle/bin/huddle");
        self.command(&link)
            .args(args)
            .output()
            .expect("run the installed huddle")
    }

    fn port_open(&self) -> bool {
        let addr: SocketAddr = format!("127.0.0.1:{}", self.port).parse().expect("addr");
        TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok()
    }

    fn wait_port(&self, want_open: bool) -> bool {
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline {
            if self.port_open() == want_open {
                return true;
            }
            thread::sleep(Duration::from_millis(50));
        }
        false
    }

    fn daemon_pid(&self) -> u32 {
        let path = self.home.root().join("r/huddle/router.toml");
        let doc: toml::Table =
            toml::from_str(&fs::read_to_string(path).expect("router record")).expect("toml");
        doc["pid"].as_integer().expect("pid") as u32
    }

    fn wait_new_pid(&self, old: u32) -> bool {
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline {
            if self.daemon_pid() != old {
                return true;
            }
            thread::sleep(Duration::from_millis(50));
        }
        false
    }
}

impl Drop for Env<'_> {
    fn drop(&mut self) {
        let _ = self.installed(&["router", "stop"]);
    }
}

fn assert_ok(out: &Output) {
    assert_eq!(
        out.status.code(),
        Some(0),
        "stdout: {}\nstderr: {}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
}

#[test]
fn the_full_flow_from_bootstrap_to_uninstall() {
    let home = TempHome::new();
    let base = fake_release(home.root());
    let env = Env {
        home: &home,
        port: free_port(),
        base,
    };
    let settings = home.root().join(".claude/settings.json");
    fs::create_dir_all(settings.parent().expect("parent")).expect("mkdir");
    let original = "{\n    \"theme\":   \"dark\"\n}\n";
    fs::write(&settings, original).expect("write settings");

    // `/huddle:setup`: the bootstrap downloads, verifies, installs and runs `setup --install`.
    let setup = env.bootstrap(&["setup", "--install"]);
    assert_ok(&setup);
    let stdout = String::from_utf8_lossy(&setup.stdout);
    assert!(stdout.contains("Restart Claude Code once"), "{stdout}");

    // The configs are patched and the server serves.
    assert_ne!(fs::read_to_string(&settings).expect("read"), original);
    let doc: serde_json::Value =
        serde_json::from_slice(&fs::read(&settings).expect("settings")).expect("json");
    assert_eq!(
        doc["env"]["HTTPS_PROXY"],
        format!("http://127.0.0.1:{}", env.port)
    );
    assert!(env.wait_port(true), "the router serves after setup");

    let bin_dir = home.root().join(".local/share/huddle/bin");
    assert!(bin_dir.join("huddle").exists());

    // A later session finds the server stopped and brings it back.
    let pid = env.daemon_pid();
    let killed = Command::new("/bin/kill")
        .args(["-9", &pid.to_string()])
        .status()
        .expect("kill");
    assert!(killed.success(), "signalled our own daemon");
    assert!(env.wait_port(false), "the daemon is gone");

    let hook = env.installed(&["hook", "session-start"]);
    assert_ok(&hook);
    assert_eq!(String::from_utf8_lossy(&hook.stdout), "");
    assert!(
        env.wait_port(true),
        "the session hook brought the router back"
    );
    // The daemon binds before it writes its new record: wait for the new pid.
    assert!(env.wait_new_pid(pid), "a new daemon pid");

    // A second setup changes nothing and does not ask for a restart.
    let again = env.bootstrap(&["setup", "--install"]);
    assert_ok(&again);
    assert!(!String::from_utf8_lossy(&again.stdout).contains("Restart Claude Code"));

    // Uninstall restores settings.json byte for byte and stops the router.
    let uninstall = env.installed(&["uninstall"]);
    assert_ok(&uninstall);
    assert_eq!(fs::read_to_string(&settings).expect("read"), original);
    assert!(env.wait_port(false), "uninstall stopped the router");
}
