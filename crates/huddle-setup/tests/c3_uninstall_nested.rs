use huddle_config::env::Env;
use huddle_config::paths::Paths;
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

fn fake_exe(home: &TempHome) -> PathBuf {
    let path = home.root().join("fake-huddle.sh");
    fs::write(&path, "#!/bin/sh\nexit 0\n").expect("write");
    fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).expect("chmod");
    path
}

fn touch(dir: &Path, name: &str) {
    fs::create_dir_all(dir).expect("mkdir");
    fs::write(dir.join(name), "x").expect("write");
}

#[test]
fn c3_nested_folder_before_its_parent_gets_no_line() {
    let home = TempHome::new();
    let data = home.root().join("custom/huddle");
    let config = data.join("config");
    let paths = paths_with(
        &home,
        &[
            ("HUDDLE_DATA_HOME", data.to_str().expect("utf8")),
            ("HUDDLE_CONFIG_HOME", config.to_str().expect("utf8")),
        ],
    );
    touch(&config, "config.toml");
    touch(&data, "huddle-bin");

    let report = run(&paths, &fake_exe(&home));

    let lines = report.lines.join("\n");
    assert!(!lines.contains("left in place"), "{lines}");
    assert!(!config.exists());
    assert!(!data.exists());
}

#[test]
fn c3_uninstall_still_keeps_a_folder_not_named_huddle() {
    let home = TempHome::new();
    let stranger = home.root().join("custom/tmp-data");
    let paths = paths_with(
        &home,
        &[("HUDDLE_DATA_HOME", stranger.to_str().expect("utf8"))],
    );
    touch(&stranger, "theirs.txt");
    touch(&paths.config_dir, "config.toml");

    let report = run(&paths, &fake_exe(&home));

    let lines = report.lines.join("\n");
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
