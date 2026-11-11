use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn dist_run() -> PathBuf {
    repo_root().join("plugin/dist/run")
}

fn pinned_version() -> String {
    let text = fs::read_to_string(dist_run()).expect("read dist/run");
    text.lines()
        .find_map(|l| l.strip_prefix("version="))
        .expect("dist/run has a version= line")
        .to_string()
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

/// A fake `uname` so the test fixes the platform: linux on amd64 unless told otherwise.
fn shims(root: &Path, kernel: &str, machine: &str) -> PathBuf {
    let dir = root.join("shims");
    fs::create_dir_all(&dir).expect("mkdir shims");
    let script =
        format!("#!/bin/sh\ncase \"$1\" in -s) echo {kernel} ;; -m) echo {machine} ;; esac\n");
    let path = dir.join("uname");
    write_executable(&path, &script).expect("write uname");
    dir
}

/// A release directory on disk; `base` is its file:// URL.
fn fake_release(root: &Path, version: &str, bad_digest: bool) -> String {
    let dir = root.join("release").join(format!("v{version}"));
    fs::create_dir_all(&dir).expect("mkdir release");
    let asset = dir.join("huddle-linux-amd64");
    fs::write(&asset, "#!/bin/sh\necho \"fake huddle $*\"\n").expect("write asset");
    let digest = if bad_digest {
        "0".repeat(64)
    } else {
        sha256_hex(&asset)
    };
    fs::write(
        dir.join("SHA256SUMS"),
        format!("{digest}  huddle-linux-amd64\n"),
    )
    .expect("write sums");
    format!("file://{}", root.join("release").display())
}

fn run_bootstrap(home: &TempHome, shims: &Path, base: &str, args: &[&str]) -> Output {
    let mut cmd = Command::new("sh");
    cmd.arg(dist_run()).args(args);
    cmd.env_clear().envs(home.env());
    cmd.env("PATH", format!("{}:/usr/bin:/bin", shims.display()));
    cmd.env("HUDDLE_DOWNLOAD_BASE", base);
    cmd.output().expect("run dist/run")
}

fn stdout_of(out: &Output) -> String {
    String::from_utf8_lossy(&out.stdout).into_owned()
}

fn stderr_of(out: &Output) -> String {
    String::from_utf8_lossy(&out.stderr).into_owned()
}

#[test]
fn pinned_version_equals_the_version_file() {
    let file = fs::read_to_string(repo_root().join("VERSION")).expect("read VERSION");
    assert_eq!(pinned_version(), file.trim());
}

#[test]
fn setup_downloads_verifies_installs_and_runs_the_binary() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let version = pinned_version();
    let base = fake_release(&root, &version, false);
    let shim_dir = shims(&root, "Linux", "x86_64");

    let out = run_bootstrap(&home, &shim_dir, &base, &["setup", "--install"]);

    assert_eq!(out.status.code(), Some(0), "stderr: {}", stderr_of(&out));
    assert_eq!(stdout_of(&out), "fake huddle setup --install\n");
    assert_eq!(stderr_of(&out), "");

    let bin_dir = root.join(".local/share/huddle/bin");
    let target = bin_dir.join(format!("huddle-{version}"));
    let mode = fs::metadata(&target)
        .expect("binary installed")
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o755);

    let link = fs::read_link(bin_dir.join("huddle")).expect("huddle is a symlink");
    assert_eq!(
        link,
        PathBuf::from(format!("huddle-{version}")),
        "the link is relative"
    );

    let leftovers: Vec<String> = fs::read_dir(&bin_dir)
        .expect("read bin dir")
        .map(|e| e.expect("entry").file_name().to_string_lossy().into_owned())
        .filter(|n| n.starts_with('.'))
        .collect();
    assert!(leftovers.is_empty(), "temp files left: {leftovers:?}");
}

#[test]
fn a_checksum_mismatch_installs_nothing() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let version = pinned_version();
    let base = fake_release(&root, &version, true);
    let shim_dir = shims(&root, "Linux", "x86_64");

    let out = run_bootstrap(&home, &shim_dir, &base, &["setup"]);

    assert_eq!(out.status.code(), Some(127));
    assert_eq!(
        stderr_of(&out),
        "huddle: huddle download failed its checksum\n"
    );
    assert_eq!(stdout_of(&out), "");
    let target = root
        .join(".local/share/huddle/bin")
        .join(format!("huddle-{version}"));
    assert!(!target.exists(), "no binary after a bad checksum");
}

#[test]
fn an_unreachable_release_says_the_url_and_exits_127() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let version = pinned_version();
    let shim_dir = shims(&root, "Linux", "x86_64");
    let base = format!("file://{}", root.join("no-release-here").display());

    let out = run_bootstrap(&home, &shim_dir, &base, &["setup"]);

    assert_eq!(out.status.code(), Some(127));
    let want = format!(
        "huddle: could not download huddle {version} ({base}/v{version}/huddle-linux-amd64)\n"
    );
    // The cargo-less fallback prints its install hint before the release error.
    let stderr = stderr_of(&out);
    assert!(stderr.ends_with(&want), "{stderr}");
}

#[test]
fn hooks_and_unknown_words_change_nothing_before_setup() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let shim_dir = shims(&root, "Linux", "x86_64");
    let bin_dir = root.join(".local/share/huddle/bin");
    let base = format!("file://{}", root.join("nothing").display());

    let hook = run_bootstrap(&home, &shim_dir, &base, &["hook", "session-start"]);
    assert_eq!(hook.status.code(), Some(0));
    assert_eq!(stdout_of(&hook), "");
    assert_eq!(stderr_of(&hook), "");
    assert!(!bin_dir.exists(), "a hook must not create the bin dir");

    let status = run_bootstrap(&home, &shim_dir, &base, &["status"]);
    assert_eq!(status.status.code(), Some(127));
    assert_eq!(stderr_of(&status), "huddle: run /huddle:setup first\n");

    let unknown = run_bootstrap(&home, &shim_dir, &base, &["frob"]);
    assert_eq!(unknown.status.code(), Some(2));
    assert_eq!(stderr_of(&unknown), "huddle: unknown command frob\n");

    let plan9_shims = shims(&root, "Plan9", "mips");
    let version = pinned_version();
    let platform = run_bootstrap(&home, &plan9_shims, &base, &["setup"]);
    assert_eq!(platform.status.code(), Some(127));
    assert_eq!(
        stderr_of(&platform),
        format!("huddle: no huddle {version} binary for this platform (unsupported-unsupported)\n")
    );
}
