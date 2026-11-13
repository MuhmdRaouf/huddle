use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_config::providers_file::set_enabled;
use huddle_setup::uninstall::run;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

fn paths_with(home: &TempHome, extra: &[(&str, &str)]) -> Paths {
    let root = home.root().to_string_lossy().into_owned();
    let mut pairs = vec![("HOME", root.as_str())];
    pairs.extend_from_slice(extra);
    Paths::from_env(&Env::with(&pairs)).expect("paths")
}

/// A stand-in for the huddle binary: exits 1 with a message on the commands in `failing`
/// (matched against the first two arguments), 0 on every other one.
fn fake_exe(home: &TempHome, failing: &[&str]) -> PathBuf {
    let path = home.root().join("fake-huddle.sh");
    let mut script = String::from("#!/bin/sh\ncase \"$1 $2\" in\n");
    for command in failing {
        script.push_str(&format!("\"{command}\") echo refused >&2; exit 1;;\n"));
    }
    script.push_str("esac\nexit 0\n");
    fs::write(&path, script).expect("write");
    fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).expect("chmod");
    path
}

fn touch(dir: &Path, name: &str) {
    fs::create_dir_all(dir).expect("mkdir");
    fs::write(dir.join(name), "x").expect("write");
}

fn text(lines: &[String]) -> String {
    lines.join("\n")
}

#[test]
fn h1_uninstall_leaves_a_folder_not_named_huddle() {
    let home = TempHome::new();
    let stranger = home.root().join("tmp-cache");
    touch(&stranger, "theirs.txt");
    let paths = paths_with(
        &home,
        &[("HUDDLE_CACHE_HOME", stranger.to_str().expect("utf8"))],
    );
    touch(&paths.config_dir, "config.toml");

    let report = run(&paths, &fake_exe(&home, &[]));

    let lines = text(&report.lines);
    assert!(
        lines.contains(&format!(
            "left in place: {} (it is not named huddle)",
            stranger.display()
        )),
        "{lines}"
    );
    assert!(stranger.join("theirs.txt").exists());
    assert!(!paths.config_dir.exists());
}

#[test]
fn h1_failed_router_stop_keeps_the_folders_it_writes_to() {
    let home = TempHome::new();
    let paths = paths_with(&home, &[]);
    for dir in [
        &paths.state_dir,
        &paths.run_dir,
        &paths.data_dir,
        &paths.config_dir,
    ] {
        touch(dir, "kept.txt");
    }

    let report = run(&paths, &fake_exe(&home, &["router stop"]));

    let lines = text(&report.lines);
    // The default run folder sits inside the state folder and has no line of its own.
    for dir in [&paths.state_dir, &paths.data_dir] {
        assert!(
            lines.contains(&format!(
                "left in place: {} (huddle-router is still running)",
                dir.display()
            )),
            "{lines}"
        );
        assert!(dir.join("kept.txt").exists(), "{}", dir.display());
    }
    assert!(!paths.config_dir.exists(), "the config folder still goes");
}

#[test]
fn h1_failed_provider_remove_keeps_providers_toml() {
    let home = TempHome::new();
    let paths = paths_with(&home, &[]);
    fs::create_dir_all(&paths.config_dir).expect("mkdir");
    set_enabled(&paths.config_dir, "zai", true).expect("enable");
    touch(&paths.state_dir, "huddle.db");

    let report = run(&paths, &fake_exe(&home, &["provider zai"]));

    let lines = text(&report.lines);
    assert!(
        lines.contains(&format!(
            "left in place: {} (zai was not removed; providers.toml stays for a retry)",
            paths.config_dir.display()
        )),
        "{lines}"
    );
    assert!(paths.config_dir.join("providers.toml").exists());
    assert!(!paths.state_dir.exists(), "the other folders still go");
}

#[test]
fn h1_default_run_folder_goes_with_the_state_folder_and_says_nothing() {
    let home = TempHome::new();
    let paths = paths_with(&home, &[]);
    assert!(
        paths.run_dir.starts_with(&paths.state_dir),
        "{:?}",
        paths.run_dir
    );
    touch(&paths.run_dir, "router.toml");
    touch(&paths.state_dir, "huddle.db");

    let report = run(&paths, &fake_exe(&home, &[]));

    let lines = text(&report.lines);
    assert!(!lines.contains("left in place"), "{lines}");
    assert!(!paths.run_dir.exists());
    assert!(!paths.state_dir.exists());
}

#[test]
fn h1_a_missing_folder_not_named_huddle_is_not_reported() {
    let home = TempHome::new();
    let missing = home.root().join("never-made");
    let paths = paths_with(
        &home,
        &[("HUDDLE_CACHE_HOME", missing.to_str().expect("utf8"))],
    );

    let report = run(&paths, &fake_exe(&home, &[]));

    let lines = text(&report.lines);
    assert!(!lines.contains("left in place"), "{lines}");
}
