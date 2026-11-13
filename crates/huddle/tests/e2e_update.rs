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
    let bin = root.join(".local/share/huddle/bin");
    fs::create_dir_all(&bin).expect("mkdir bin");
    write_executable(
        &bin.join(format!("huddle-{version}")),
        &fake_binary(version),
    )
    .expect("write binary");
    symlink(format!("huddle-{version}"), bin.join("huddle")).expect("link");
    fs::write(root.join("router-version"), version).expect("router version");
}

/// A fake "latest release" answer: GitHub's JSON with the tag, as a file:// URL.
fn latest_url(root: &Path, tag: &str) -> String {
    let path = root.join("latest.json");
    fs::write(
        &path,
        format!("{{\n  \"tag_name\": \"v{tag}\",\n  \"draft\": false\n}}\n"),
    )
    .expect("write latest");
    format!("file://{}", path.display())
}

fn update(home: &TempHome, base: &str, latest: &str) -> Output {
    let mut cmd = Command::new("sh");
    cmd.arg(dist_run()).arg("update");
    cmd.env_clear().envs(home.env());
    cmd.env(
        "PATH",
        format!("{}:/usr/bin:/bin", shims(home.root()).display()),
    );
    cmd.env("HUDDLE_DOWNLOAD_BASE", base);
    cmd.env("HUDDLE_LATEST_URL", latest);
    cmd.output().expect("run dist/run update")
}

fn stdout_of(out: &Output) -> String {
    String::from_utf8_lossy(&out.stdout).into_owned()
}

fn link_of(root: &Path) -> PathBuf {
    fs::read_link(root.join(".local/share/huddle/bin/huddle")).expect("huddle is a symlink")
}

fn router_calls(root: &Path) -> String {
    fs::read_to_string(root.join("router-calls")).unwrap_or_default()
}

#[test]
fn e3_update_newer_published_switches_the_link_and_hands_the_router_over() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, OLD);
    let base = fake_release(&root, &new, false);

    let out = update(&home, &base, &latest_url(&root, &new));

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(stdout_of(&out), format!("huddle {OLD} → {new}\n"));
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{new}")));
    assert!(
        root.join(format!(".local/share/huddle/bin/huddle-{OLD}"))
            .exists()
    );
    assert_eq!(router_calls(&root), format!("start {new}\n"));
    assert!(
        !root.join(".claude/settings.json").exists(),
        "update never touches settings.json"
    );
}

#[test]
fn e3_update_nothing_newer_says_so_and_does_nothing() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, &new);
    let base = fake_release(&root, &new, false);

    let out = update(&home, &base, &latest_url(&root, &new));

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(
        stdout_of(&out),
        format!("huddle {new} is the latest published version. Nothing to update.\n")
    );
    assert_eq!(router_calls(&root), "");
}

#[test]
fn e3_update_plugin_ahead_says_the_plugin_version_is_not_published_yet() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let plugin = pinned_version();
    install(&root, OLD);
    let base = fake_release(&root, OLD, false);

    let out = update(&home, &base, &latest_url(&root, OLD));

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(
        stdout_of(&out),
        format!(
            "huddle {OLD} is the latest published version. Nothing to update.\n\
             {plugin} is not published yet. Try again in a few minutes.\n"
        )
    );
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{OLD}")));
}

#[test]
fn e3_update_unreachable_github_keeps_the_installed_version() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    install(&root, OLD);
    let base = fake_release(&root, &pinned_version(), false);
    let missing = format!("file://{}", root.join("no-such-answer.json").display());

    let out = update(&home, &base, &missing);

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(
        stdout_of(&out),
        format!("huddle: could not check for updates (GitHub unreachable). You stay on {OLD}.\n")
    );
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{OLD}")));
    assert_eq!(router_calls(&root), "");
}

#[test]
fn e3_update_binary_newer_than_the_plugin_points_to_the_plugin_update() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let plugin = pinned_version();
    let newer = "99.0.0";
    install(&root, OLD);
    let base = fake_release(&root, newer, false);

    let out = update(&home, &base, &latest_url(&root, newer));

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(
        stdout_of(&out),
        format!("huddle {OLD} → {newer}\nPlugin is {plugin}: run /plugin to update it too.\n")
    );
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{newer}")));
}

#[test]
fn e3_update_never_builds() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    install(&root, OLD);
    // The release the answer names has no assets, so the download fails.
    let base = format!("file://{}", root.join("release").display());
    let cargo_log = root.join("cargo-calls");
    write_executable(
        &shims(&root).join("cargo"),
        &format!("#!/bin/sh\necho \"$*\" >> \"{}\"\n", cargo_log.display()),
    )
    .expect("write cargo shim");

    let out = update(&home, &base, &latest_url(&root, &pinned_version()));

    assert_ne!(out.status.code(), Some(0));
    assert!(!cargo_log.exists(), "update ran cargo");
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{OLD}")));
}

#[test]
fn e3_update_refuses_a_bad_checksum() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, OLD);
    let base = fake_release(&root, &new, true);

    let out = update(&home, &base, &latest_url(&root, &new));

    assert_ne!(out.status.code(), Some(0));
    assert_eq!(stdout_of(&out), "");
    assert!(
        String::from_utf8_lossy(&out.stderr).contains("failed its checksum"),
        "{out:?}"
    );
    assert_eq!(link_of(&root), PathBuf::from(format!("huddle-{OLD}")));
    assert!(
        !root
            .join(format!(".local/share/huddle/bin/huddle-{new}"))
            .exists()
    );
    assert_eq!(router_calls(&root), "");
}

#[test]
fn e3_update_hands_a_stale_router_over_though_the_link_is_current() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let new = pinned_version();
    install(&root, &new);
    fs::write(root.join("router-version"), OLD).expect("stale router");
    let base = fake_release(&root, &new, false);

    let out = update(&home, &base, &latest_url(&root, &new));

    assert_eq!(out.status.code(), Some(0), "{out:?}");
    assert_eq!(stdout_of(&out), format!("huddle {OLD} → {new}\n"));
    assert_eq!(router_calls(&root), format!("start {new}\n"));
}
