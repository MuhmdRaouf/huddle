use std::fs;

use huddle_testkit::temp_home::TempHome;

#[test]
fn make_repo_builds_a_private_repository_with_one_commit_on_main() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    assert!(repo.starts_with(home.root()));
    assert!(repo.join(".git").is_dir());
    assert!(repo.join("README.md").is_file());
    assert_eq!(
        home.git(&repo, &["rev-parse", "--abbrev-ref", "HEAD"])
            .unwrap(),
        "main"
    );
    assert_eq!(
        home.git(&repo, &["rev-list", "--count", "HEAD"]).unwrap(),
        "1"
    );
    assert_eq!(
        home.git(&repo, &["config", "commit.gpgsign"]).unwrap(),
        "false"
    );
}

#[test]
fn git_sees_no_config_of_the_owner() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    let origin = home
        .git(&repo, &["config", "--show-origin", "user.name"])
        .unwrap();
    assert!(origin.contains(".git/config"), "{origin}");
    fs::write(repo.join("x"), "1").unwrap();
    home.git(&repo, &["add", "x"]).unwrap();
    home.git(&repo, &["commit", "-q", "-m", "second"]).unwrap();
    assert_eq!(
        home.git(&repo, &["rev-list", "--count", "HEAD"]).unwrap(),
        "2"
    );
}

#[test]
fn a_failed_git_command_is_an_error_naming_stderr() {
    let home = TempHome::new();
    let repo = home.make_repo().unwrap();
    let err = home.git(&repo, &["log", "--no-such-flag"]).unwrap_err();
    assert!(err.to_string().contains("unrecognized"), "{err}");
}
