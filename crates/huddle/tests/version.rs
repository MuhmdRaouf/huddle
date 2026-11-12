use std::process::Command;

const VERSION_FILE: &str = include_str!("../../../VERSION");

fn huddle(args: &[&str]) -> std::process::Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .output()
        .unwrap()
}

#[test]
fn version_flag_prints_the_version_file() {
    let out = huddle(&["--version"]);
    assert_eq!(out.status.code(), Some(0));
    assert_eq!(
        String::from_utf8_lossy(&out.stdout),
        format!("huddle {}\n", VERSION_FILE.trim())
    );
    assert_eq!(String::from_utf8_lossy(&out.stderr), "");
}

#[test]
fn version_word_answers_like_the_flag() {
    assert_eq!(huddle(&["version"]).stdout, huddle(&["--version"]).stdout);
}

#[test]
fn cargo_package_version_equals_the_version_file() {
    assert_eq!(env!("CARGO_PKG_VERSION"), VERSION_FILE.trim());
}

#[test]
fn version_file_is_three_numbers() {
    let parts: Vec<&str> = VERSION_FILE.trim().split('.').collect();
    assert_eq!(parts.len(), 3);
    assert!(
        parts
            .iter()
            .all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()))
    );
}

#[test]
fn an_unknown_word_exits_2_without_naming_it() {
    let out = huddle(&["bogus-word"]);
    assert_eq!(out.status.code(), Some(2));
    assert_eq!(String::from_utf8_lossy(&out.stdout), "");
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(stderr.starts_with("huddle: unknown word\n"), "{stderr}");
    assert!(!stderr.contains("bogus-word"), "{stderr}");
}

#[test]
fn no_word_shows_where_you_stand_and_exits_0() {
    let home = huddle_testkit::temp_home::TempHome::new();
    let out = home.command(env!("CARGO_BIN_EXE_huddle")).output().unwrap();
    assert_eq!(out.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&out.stdout).starts_with("huddle "));
}
