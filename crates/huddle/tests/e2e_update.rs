use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::symlink;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

const OLD: &str = "0.0.1";

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

/// A stand-in binary: it answers `--version`, reports the router version kept in
/// `$HOME/router-version`, and a `router start` moves that file to its own version and logs
/// the call in `$HOME/router-calls`.
fn fake_binary(version: &str) -> String {
    format!(
        r#"#!/bin/sh
case "$*" in
  "--version") echo "huddle {version}" ;;
  "router status --json")
    [ -f "$HOME/router-version" ] || exit 6
    echo "{{\"running\":true,\"version\":\"$(cat "$HOME/router-version")\"}}" ;;
  "router start")
    echo "{version}" > "$HOME/router-version"
    echo "start {version}" >> "$HOME/router-calls" ;;
esac
"#
    )
}

fn shims(root: &Path) -> PathBuf {
    let dir = root.join("shims");
    fs::create_dir_all(&dir).expect("mkdir shims");
    write_executable(
        &dir.join("uname"),
        "#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n",
    )
    .expect("write uname");
    dir
}

fn fake_release(root: &Path, version: &str, bad_digest: bool) -> String {
    let dir = root.join("release").join(format!("v{version}"));
    fs::create_dir_all(&dir).expect("mkdir release");
    let asset = dir.join("huddle-linux-amd64");
    fs::write(&asset, fake_binary(version)).expect("write asset");
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

/// A home with `version` installed, linked and running as the router.
fn install(root: &Path, version: &str) {
    let bin = root.join(".agents/huddle/bin");
    fs::create_dir_all(&bin).expect("mkdir bin");
    write_executable(
        &bin.join(format!("huddle-{version}")),
        &fake_binary(version),
    )
    .expect("write binary");
    symlink(format!("huddle-{version}"), bin.join("huddle")).expect("link");
    fs::write(root.join("router-version"), version).expect("router version");
}

fn update(home: &TempHome, base: &str) -> Output {
    let mut cmd = Command::new("sh");
    cmd.arg(dist_run()).arg("update");
    cmd.env_clear().envs(home.env());
    cmd.env_remove("HUDDLE_HOME");
    cmd.env(
        "PATH",
        format!("{}:/usr/bin:/bin", shims(home.root()).display()),
    );
    cmd.env("HUDDLE_DOWNLOAD_BASE", base);
    cmd.output().expect("run dist/run update")
}

fn stdout_of(out: &Output) -> String {
    String::from_utf8_lossy(&out.stdout).into_owned()
}

fn link_of(root: &Path) -> PathBuf {
    fs::read_link(root.join(".agents/huddle/bin/huddle")).expect("huddle is a symlink")
}

fn router_calls(root: &Path) -> String {
    fs::read_to_string(root.join("router-calls")).unwrap_or_default()
}

#[test]
fn update_switches_the_link_hands_the_router_over_and_prints_the_arrow() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, OLD);
    let base = fake_release(&root, &new, false);

    let out = update(&home, &base);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(stdout_of(&out), format!("huddle {OLD} → {new}\n"));
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{new}")));
    assert!(
        root.join(format!(".agents/huddle/bin/huddle-{OLD}"))
            .exists()
    );
    assert_eq!(router_calls(&root), format!("start {new}\n"));
    assert!(
        !root.join(".claude/settings.json").exists(),
        "update never touches settings.json"
    );
}

#[test]
fn a_second_update_says_it_is_the_latest_and_does_nothing() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, OLD);
    let base = fake_release(&root, &new, false);
    assert_eq!(update(&home, &base).status.code(), Some(0));

    let out = update(&home, &base);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(stdout_of(&out), format!("huddle {new} is the latest\n"));
    assert_eq!(router_calls(&root), format!("start {new}\n"));
}

#[test]
fn a_router_still_on_the_old_version_is_handed_over_though_the_link_is_current() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, &new);
    fs::write(root.join("router-version"), OLD).expect("stale router");
    let base = fake_release(&root, &new, false);

    let out = update(&home, &base);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(stdout_of(&out), format!("huddle {OLD} → {new}\n"));
    assert_eq!(router_calls(&root), format!("start {new}\n"));
}

#[test]
fn a_bad_checksum_exits_non_zero_and_leaves_the_link_alone() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, OLD);
    let base = fake_release(&root, &new, true);

    let out = update(&home, &base);

    assert_ne!(out.status.code(), Some(0));
    assert_eq!(stdout_of(&out), "");
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{OLD}")));
    assert!(
        !root
            .join(format!(".agents/huddle/bin/huddle-{new}"))
            .exists()
    );
    assert_eq!(router_calls(&root), "");
}
