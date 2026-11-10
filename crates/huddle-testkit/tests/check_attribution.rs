//! The attribution check binary: clean histories pass, AI credits fail.

use std::path::Path;
use std::process::Output;

use huddle_testkit::temp_home::TempHome;

const BIN: &str = env!("CARGO_BIN_EXE_huddle-check-attribution");

fn run(home: &TempHome, repo: &Path) -> Output {
    home.command(BIN)
        .current_dir(repo)
        .output()
        .expect("the check runs")
}

/// One commit past the clean init commit, with `message` as the full message.
fn commit(home: &TempHome, repo: &Path, file: &str, message: &str) {
    std::fs::write(repo.join(file), "1").unwrap();
    home.git(repo, &["add", file]).unwrap();
    home.git(repo, &["commit", "-q", "-m", message]).unwrap();
}

/// One commit past the clean init commit, authored as `name <email>`.
fn commit_as(home: &TempHome, repo: &Path, file: &str, name: &str, email: &str) {
    std::fs::write(repo.join(file), "1").unwrap();
    home.git(repo, &["add", file]).unwrap();
    let status = home
        .command("git")
        .current_dir(repo)
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_AUTHOR_NAME", name)
        .env("GIT_AUTHOR_EMAIL", email)
        .args(["commit", "-q", "-m", "bot work"])
        .status()
        .expect("the commit runs");
    assert!(status.success(), "the commit failed");
}

#[test]
fn a_clean_history_exits_0_with_the_clean_line() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    let output = run(&home, &repo);
    assert!(output.status.success());
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        stdout.contains("check-attribution: no AI attribution in the history"),
        "{stdout}"
    );
}

#[test]
fn a_co_author_trailer_exits_1_naming_co_author() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    commit(
        &home,
        &repo,
        "a",
        "add a\n\nCo-authored-by: Someone <a@b.c>",
    );
    let output = run(&home, &repo);
    assert!(!output.status.success());
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains("co-author"), "{stderr}");
}

#[test]
fn an_ai_author_exits_1_naming_author() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    commit_as(&home, &repo, "b", "Bot", "x@anthropic.com");
    let output = run(&home, &repo);
    assert!(!output.status.success());
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains(" author "), "{stderr}");
}

#[test]
fn a_generated_with_body_line_exits_1() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    commit(&home, &repo, "c", "add c\n\nGenerated with Claude Code");
    let output = run(&home, &repo);
    assert!(!output.status.success());
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains("attribution line"), "{stderr}");
}

#[test]
fn a_copilotish_body_line_exits_0() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    commit(&home, &repo, "d", "add d\n\nthe copilotish helper helped");
    let output = run(&home, &repo);
    assert!(output.status.success());
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        stdout.contains("check-attribution: no AI attribution in the history"),
        "{stdout}"
    );
}

#[test]
fn an_assisted_by_copilot_trailer_exits_1() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    commit(&home, &repo, "e", "add e\n\nAssisted-by: GitHub Copilot");
    let output = run(&home, &repo);
    assert!(!output.status.success());
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains("trailer"), "{stderr}");
}
