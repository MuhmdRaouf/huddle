mod common;

use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};

use common::TempDir;

const CHANGELOG: &str = concat!(
    "# Changelog\n\n",
    "## Unreleased\n\nnot this\n\n",
    "## 1.2.3 (2026-11-01)\n\n",
    "Intro line one\nand line two.\n\n",
    "### Fixes\n\n",
    "- A bullet that\n  wraps here.\n",
    "- Second bullet.\n\n",
    "```sh\n# code stays\nas it\nis\n```\n\n",
    "## 1.2.2\n\nolder\n",
);

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .unwrap()
        .to_path_buf()
}

fn notes(home: &TempDir, version: &str, changelog: &str) -> (i32, String, String) {
    let path = home.root().join("CHANGELOG.md");
    fs::write(&path, changelog).unwrap();
    let sums = home.root().join("SHA256SUMS");
    fs::write(&sums, "abc  huddle-linux-amd64\ndef  huddle-darwin-arm64\n").unwrap();
    let args = [
        OsString::from("notes"),
        OsString::from(version),
        path.into_os_string(),
        sums.into_os_string(),
        OsString::from("cafe123"),
    ];
    let out = common::run(home, &args);
    (
        out.status.code().unwrap(),
        String::from_utf8_lossy(&out.stdout).to_string(),
        String::from_utf8_lossy(&out.stderr).to_string(),
    )
}

fn section(home: &TempDir, version: &str, changelog: &str) -> (i32, String, String) {
    let path = home.root().join("CHANGELOG.md");
    fs::write(&path, changelog).unwrap();
    let args = [
        OsString::from("section"),
        OsString::from(version),
        path.into_os_string(),
    ];
    let out = common::run(home, &args);
    (
        out.status.code().unwrap(),
        String::from_utf8_lossy(&out.stdout).to_string(),
        String::from_utf8_lossy(&out.stderr).to_string(),
    )
}

#[test]
fn it_prints_only_that_versions_section_with_lines_unwrapped() {
    let home = TempDir::new();
    let (code, out, _) = notes(&home, "1.2.3", CHANGELOG);
    assert_eq!(code, 0);
    assert!(
        out.starts_with("Intro line one and line two.\n\n### Fixes\n"),
        "{out}"
    );
    assert!(
        out.contains("- A bullet that wraps here.\n- Second bullet.\n"),
        "{out}"
    );
    assert!(out.contains("```sh\n# code stays\nas it\nis\n```"), "{out}");
    assert!(!out.contains("not this"), "{out}");
    assert!(!out.contains("older"), "{out}");
}

#[test]
fn it_appends_the_checksums_block_and_the_commit() {
    let home = TempDir::new();
    let (_, out, _) = notes(&home, "1.2.3", CHANGELOG);
    assert!(
        out.ends_with(
            "\n### Checksums\n\n```\nabc  huddle-linux-amd64\ndef  huddle-darwin-arm64\n```\nBuilt from cafe123.\n"
        ),
        "{out}"
    );
}

#[test]
fn a_missing_section_fails_naming_the_version() {
    let home = TempDir::new();
    let (code, out, err) = notes(&home, "9.9.9", CHANGELOG);
    assert_eq!(code, 1);
    assert_eq!(out, "");
    assert!(err.contains("no CHANGELOG.md section for 9.9.9"), "{err}");
}

#[test]
fn an_empty_section_counts_as_missing() {
    let home = TempDir::new();
    let (code, _, err) = notes(&home, "2.0.0", "## 2.0.0\n\n\n## 1.0.0\n\nold\n");
    assert_eq!(code, 1);
    assert!(err.contains("no CHANGELOG.md section"), "{err}");
}

#[test]
fn a_heading_inside_a_fence_does_not_end_the_section() {
    let home = TempDir::new();
    let text = "## 3.0.0\n\ntext\n\n```\n## 2.0.0\n```\n\nafter\n";
    let (_, out, _) = notes(&home, "3.0.0", text);
    assert!(out.contains("## 2.0.0"), "{out}");
    assert!(out.contains("after"), "{out}");
}

#[test]
fn the_section_is_the_bare_lines_between_the_headings() {
    let home = TempDir::new();
    let (code, out, _) = section(&home, "1.2.3", CHANGELOG);
    assert_eq!(code, 0);
    assert_eq!(
        out,
        concat!(
            "Intro line one\nand line two.\n\n",
            "### Fixes\n\n",
            "- A bullet that\n  wraps here.\n",
            "- Second bullet.\n\n",
            "```sh\n# code stays\nas it\nis\n```\n",
        )
    );
}

#[test]
fn the_section_carries_no_checksum_block() {
    let home = TempDir::new();
    let (_, out, _) = section(&home, "1.2.3", CHANGELOG);
    assert!(!out.contains("Checksums"), "{out}");
    assert!(!out.contains("cafe123"), "{out}");
}

#[test]
fn a_missing_section_word_fails_naming_the_version() {
    let home = TempDir::new();
    let (code, out, err) = section(&home, "9.9.9", CHANGELOG);
    assert_eq!(code, 1);
    assert_eq!(out, "");
    assert!(err.contains("no CHANGELOG.md section for 9.9.9"), "{err}");
}

#[test]
fn the_real_changelog_has_a_section_for_the_committed_version() {
    let home = TempDir::new();
    let version = fs::read_to_string(repo_root().join("VERSION")).unwrap();
    let changelog = fs::read_to_string(repo_root().join("CHANGELOG.md")).unwrap();
    assert_eq!(notes(&home, version.trim(), &changelog).0, 0);
}

#[test]
fn the_release_workflow_stops_when_the_notes_fail() {
    let workflow = fs::read_to_string(repo_root().join(".github/workflows/release.yml")).unwrap();
    assert!(
        workflow.contains("> dist/NOTES.md\n"),
        "the notes are their own command"
    );
    assert!(
        workflow.contains("--notes-file dist/NOTES.md"),
        "the release reads that file"
    );
    assert!(
        !workflow.contains("--notes-file -"),
        "no pipe hides a failed notes step"
    );
}
