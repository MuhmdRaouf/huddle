use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

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

fn write_exec(path: &Path, body: &str) {
    fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
    write_executable(path, body).expect("write");
}

/// A PATH holding a fake `uname` and, when asked, a fake `cargo`.
fn shims(root: &Path, with_cargo: bool) -> PathBuf {
    let dir = root.join("shims");
    write_exec(
        &dir.join("uname"),
        "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n",
    );
    if with_cargo {
        let cargo = "#!/bin/sh\necho \"$PWD $*\" >> \"$HOME/cargo-calls\"\nmkdir -p target/release\nprintf '#!/bin/sh\\necho local huddle \"$@\"\\n' > target/release/huddle\nchmod 755 target/release/huddle\n";
        write_exec(&dir.join("cargo"), cargo);
    }
    dir
}

/// A checkout with the marker file.
fn checkout(root: &Path, name: &str) -> PathBuf {
    let dir = root.join(name);
    write_exec(
        &dir.join("crates/huddle/Cargo.toml"),
        "[package]\nname = \"huddle\"\n",
    );
    dir
}

fn run(
    home: &TempHome,
    shim_dir: &Path,
    base: &str,
    source: Option<&Path>,
    script: &Path,
    args: &[&str],
) -> Output {
    let mut cmd = Command::new("sh");
    cmd.arg(script).args(args);
    cmd.env_clear().envs(home.env());
    cmd.env("PATH", format!("{}:/usr/bin:/bin", shim_dir.display()));
    cmd.env("HUDDLE_DOWNLOAD_BASE", base);
    if let Some(s) = source {
        cmd.env("HUDDLE_SOURCE", s);
    }
    cmd.output().expect("run dist/run")
}

fn dist_run() -> PathBuf {
    repo_root().join("plugin/dist/run")
}

fn cargo_calls(root: &Path) -> String {
    fs::read_to_string(root.join("cargo-calls")).unwrap_or_default()
}

/// A PATH holding a fake `uname` for one platform.
fn platform_shims(root: &Path, kernel: &str, machine: &str) -> PathBuf {
    let dir = root.join("shims");
    write_exec(
        &dir.join("uname"),
        &format!("#!/bin/sh\ncase \"$1\" in -s) echo {kernel} ;; -m) echo {machine} ;; esac\n"),
    );
    dir
}

/// A `curl` for tests: `ok` copies a file:// URL to `-o`; otherwise it fails like a PATH curl
/// under TLS interception and records that it ran.
fn write_curl(path: &Path, ok: bool) {
    let body = if ok {
        "#!/bin/sh\nout=\nsrc=\nwhile [ $# -gt 0 ]; do\n  case \"$1\" in\n    -o) out=$2; shift 2 ;;\n    --max-time) shift 2 ;;\n    -*) shift ;;\n    *) src=$1; shift ;;\n  esac\ndone\np=${src#file://}\nif [ -f \"$p\" ]; then\n  cat \"$p\" >\"$out\"\n  exit 0\nfi\nexit 22\n"
    } else {
        "#!/bin/sh\necho called >>\"$HOME/curl-saw-call\"\nexit 60\n"
    };
    write_exec(path, body);
}

fn sha256_hex(path: &Path) -> String {
    let out = Command::new("sh")
        .arg("-c")
        .arg("sha256sum \"$0\" 2>/dev/null || shasum -a 256 \"$0\"")
        .arg(path)
        .output()
        .expect("run sha256");
    String::from_utf8(out.stdout)
        .expect("utf8")
        .split_whitespace()
        .next()
        .expect("a digest")
        .to_string()
}

/// A release directory on disk for one asset; `base` is its file:// URL.
fn platform_release(root: &Path, asset: &str) -> String {
    let dir = root.join("release").join(format!("v{}", pinned_version()));
    fs::create_dir_all(&dir).expect("mkdir release");
    let file = dir.join(asset);
    fs::write(&file, "#!/bin/sh\necho \"fake huddle $*\"\n").expect("write asset");
    fs::write(
        dir.join("SHA256SUMS"),
        format!("{}  {asset}\n", sha256_hex(&file)),
    )
    .expect("write sums");
    format!("file://{}", root.join("release").display())
}

/// `run` with extra variables for the child, such as `HUDDLE_CURL`.
fn run_env(
    home: &TempHome,
    shim_dir: &Path,
    base: &str,
    source: Option<&Path>,
    script: &Path,
    args: &[&str],
    extra: &[(&str, &str)],
) -> Output {
    let mut cmd = Command::new("sh");
    cmd.arg(script).args(args);
    cmd.env_clear().envs(home.env());
    cmd.env("PATH", format!("{}:/usr/bin:/bin", shim_dir.display()));
    cmd.env("HUDDLE_DOWNLOAD_BASE", base);
    if let Some(s) = source {
        cmd.env("HUDDLE_SOURCE", s);
    }
    for (k, v) in extra {
        cmd.env(k, v);
    }
    cmd.output().expect("run dist/run")
}

#[test]
fn no_release_builds_with_cargo_and_marks_the_binary_local() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let version = pinned_version();
    let shim_dir = shims(&root, true);
    let src = checkout(&root, "src");
    let base = format!("file://{}", root.join("none").display());

    let out = run(&home, &shim_dir, &base, Some(&src), &dist_run(), &["setup"]);

    assert_eq!(
        out.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(String::from_utf8_lossy(&out.stdout), "local huddle setup\n");
    let bin_dir = root.join(".local/share/huddle/bin");
    assert!(bin_dir.join(format!("huddle-{version}")).exists());
    assert!(bin_dir.join(format!("huddle-{version}.local")).exists());
    assert!(cargo_calls(&root).contains("build --release -p huddle"));
}

#[test]
fn no_cargo_says_what_to_install_and_exits_127() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let shim_dir = shims(&root, false);
    let src = checkout(&root, "src");
    let base = format!("file://{}", root.join("none").display());

    let out = run(&home, &shim_dir, &base, Some(&src), &dist_run(), &["setup"]);

    assert_eq!(out.status.code(), Some(127));
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.starts_with("huddle: no huddle release is published; install Rust (cargo) to build it from the marketplace checkout\n"),
        "{stderr}"
    );
    assert!(
        stderr.contains("huddle: could not download huddle"),
        "{stderr}"
    );
}

#[test]
fn a_tampered_release_never_falls_back_to_cargo() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let version = pinned_version();
    let shim_dir = shims(&root, true);
    let src = checkout(&root, "src");
    let dir = root.join("release").join(format!("v{version}"));
    fs::create_dir_all(&dir).expect("mkdir");
    fs::write(dir.join("huddle-linux-amd64"), "#!/bin/sh\n").expect("asset");
    fs::write(
        dir.join("SHA256SUMS"),
        format!("{}  huddle-linux-amd64\n", "0".repeat(64)),
    )
    .expect("sums");
    let base = format!("file://{}", root.join("release").display());

    let out = run(&home, &shim_dir, &base, Some(&src), &dist_run(), &["setup"]);

    assert_eq!(out.status.code(), Some(127));
    assert_eq!(
        String::from_utf8_lossy(&out.stderr),
        "huddle: huddle download failed its checksum\n"
    );
    assert_eq!(
        cargo_calls(&root),
        "",
        "cargo must not run after a bad checksum"
    );
}

#[test]
fn the_checkout_is_found_above_the_script_and_a_new_run_rebuilds() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let shim_dir = shims(&root, true);
    let plugin_checkout = checkout(&root, "marketplace");
    let script = plugin_checkout.join("plugin/dist/run");
    fs::create_dir_all(script.parent().expect("parent")).expect("mkdir");
    fs::copy(dist_run(), &script).expect("copy script");
    let base = format!("file://{}", root.join("none").display());

    let first = run(&home, &shim_dir, &base, None, &script, &["setup"]);
    assert_eq!(
        first.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&first.stderr)
    );
    let second = run(&home, &shim_dir, &base, None, &script, &["setup"]);
    assert_eq!(second.status.code(), Some(0));
    assert_eq!(
        cargo_calls(&root).lines().count(),
        2,
        "each setup run builds a local checkout"
    );
    assert!(
        cargo_calls(&root)
            .lines()
            .all(|l| l.starts_with(plugin_checkout.to_str().expect("utf8")))
    );
}

#[test]
fn darwin_downloads_through_the_system_curl_not_the_path_one() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let shim_dir = platform_shims(&root, "Darwin", "x86_64");
    // The PATH curl fails like the Nix one under TLS interception; the system curl trusts the
    // keychain. HUDDLE_CURL stands in for the system curl.
    write_curl(&shim_dir.join("curl"), false);
    write_curl(&root.join("system-curl"), true);
    let base = platform_release(&root, "huddle-darwin-amd64");

    let out = run_env(
        &home,
        &shim_dir,
        &base,
        None,
        &dist_run(),
        &["setup"],
        &[(
            "HUDDLE_CURL",
            root.join("system-curl").to_str().expect("utf8"),
        )],
    );

    assert_eq!(
        out.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(String::from_utf8_lossy(&out.stdout), "fake huddle setup\n");
    assert!(!root.join("curl-saw-call").exists(), "the PATH curl ran");
}

#[test]
fn linux_downloads_through_the_curl_on_the_path() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let shim_dir = platform_shims(&root, "Linux", "x86_64");
    write_curl(&shim_dir.join("curl"), true);
    write_curl(&root.join("system-curl"), false);
    let base = platform_release(&root, "huddle-linux-amd64");

    let out = run_env(
        &home,
        &shim_dir,
        &base,
        None,
        &dist_run(),
        &["setup"],
        &[(
            "HUDDLE_CURL",
            root.join("system-curl").to_str().expect("utf8"),
        )],
    );

    assert_eq!(
        out.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert_eq!(String::from_utf8_lossy(&out.stdout), "fake huddle setup\n");
    assert!(!root.join("curl-saw-call").exists(), "the failing curl ran");
}
