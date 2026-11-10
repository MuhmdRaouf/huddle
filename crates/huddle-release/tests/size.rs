mod common;

use std::ffi::OsString;
use std::fs::{self, File};
use std::path::{Path, PathBuf};

use common::TempDir;

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .unwrap()
        .to_path_buf()
}

fn limit() -> u64 {
    fs::read_to_string(repo_root().join("release/size-limit"))
        .unwrap()
        .trim()
        .parse()
        .unwrap()
}

/// Runs the size word with the limit file in the test home, so the walk up from the current
/// directory stops there.
fn guard(home: &TempDir, files: &[&Path]) -> (i32, String) {
    let release = home.root().join("release");
    fs::create_dir_all(&release).unwrap();
    fs::write(release.join("size-limit"), limit().to_string()).unwrap();
    let mut args = vec![OsString::from("size")];
    args.extend(files.iter().map(|p| p.as_os_str().to_owned()));
    let out = common::run(home, &args);
    (
        out.status.code().unwrap(),
        String::from_utf8_lossy(&out.stderr).to_string(),
    )
}

fn binary_of(home: &TempDir, name: &str, size: u64) -> PathBuf {
    let path = home.root().join(name);
    File::create(&path).unwrap().set_len(size).unwrap();
    path
}

#[test]
fn the_committed_limit_is_10_mb_at_most() {
    assert!(limit() <= 10_000_000, "the limit is {}", limit());
}

#[test]
fn a_binary_at_the_limit_passes() {
    let home = TempDir::new();
    let binary = binary_of(&home, "at-limit", limit());
    assert_eq!(guard(&home, &[&binary]).0, 0);
}

#[test]
fn a_binary_one_byte_over_fails_naming_it() {
    let home = TempDir::new();
    let ok = binary_of(&home, "fine", 10);
    let big = binary_of(&home, "too-big", limit() + 1);
    let (code, stderr) = guard(&home, &[&ok, &big]);
    assert_eq!(code, 1);
    assert!(stderr.contains("too-big"), "{stderr}");
    assert!(
        stderr.contains(&format!("over the limit of {}", limit())),
        "{stderr}"
    );
}

#[test]
fn no_argument_is_a_usage_error() {
    let home = TempDir::new();
    assert_eq!(guard(&home, &[]).0, 2);
}

#[test]
fn a_missing_file_is_a_usage_error() {
    let home = TempDir::new();
    let (code, stderr) = guard(&home, &[Path::new("/nonexistent/huddle")]);
    assert_eq!(code, 2);
    assert!(stderr.contains("cannot read"), "{stderr}");
}
