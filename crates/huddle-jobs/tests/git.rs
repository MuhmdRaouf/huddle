mod support;

use huddle_jobs::git::{self, ChangeSet, DiffMode, GitError};
use support::{git as run, rig, write};

#[test]
fn root_finds_the_top_and_refuses_outside_a_repository() {
    let r = rig();
    let sub = r.repo.join("sub");
    std::fs::create_dir(&sub).unwrap();
    assert_eq!(git::root(&sub).unwrap().canonicalize().unwrap(), r.repo);
    let outside = r.home.root().join("not-a-repo");
    std::fs::create_dir(&outside).unwrap();
    assert!(matches!(
        git::root(&outside),
        Err(GitError::NotARepo { .. })
    ));
}

#[test]
fn resolve_refuses_an_unknown_ref() {
    let r = rig();
    let head = git::head_sha(&r.repo).unwrap();
    assert_eq!(git::resolve(&r.repo, "HEAD").unwrap(), head);
    assert_eq!(
        git::resolve(&r.repo, "no-such-ref"),
        Err(GitError::BadRef {
            reference: "no-such-ref".into()
        })
    );
}

#[test]
fn changes_lists_added_modified_deleted_and_untracked() {
    let r = rig();
    write(&r.repo, "b.txt", "b\n");
    run(&r.repo, &["add", "b.txt"]);
    run(&r.repo, &["commit", "-q", "-m", "add b"]);
    let base = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "a.txt", "changed\n");
    std::fs::remove_file(r.repo.join("b.txt")).unwrap();
    write(&r.repo, "new.txt", "new\n");
    write(&r.repo, "staged.txt", "staged\n");
    run(&r.repo, &["add", "staged.txt"]);
    let got = git::changes(&r.repo, &base).unwrap();
    assert_eq!(
        got,
        ChangeSet {
            added: vec!["staged.txt".into()],
            modified: vec!["a.txt".into()],
            deleted: vec!["b.txt".into()],
            untracked: vec!["new.txt".into()],
        }
    );
}

#[test]
fn diff_shows_an_untracked_file_and_leaves_the_real_index_alone() {
    let r = rig();
    let base = git::head_sha(&r.repo).unwrap();
    write(&r.repo, "new.txt", "hello\n");
    let patch = git::diff(&r.repo, &base, DiffMode::Patch).unwrap();
    assert!(patch.contains("+hello"), "{patch}");
    let stat = git::diff(&r.repo, &base, DiffMode::Stat).unwrap();
    assert!(stat.contains("new.txt"), "{stat}");
    assert_eq!(run(&r.repo, &["status", "--porcelain"]), "?? new.txt");
    let leftovers: Vec<_> = std::fs::read_dir(r.repo.join(".git"))
        .unwrap()
        .filter_map(|e| e.ok())
        .filter(|e| e.file_name().to_string_lossy().starts_with("index.huddle-"))
        .collect();
    assert!(
        leftovers.is_empty(),
        "the private index copy must be removed"
    );
}

#[test]
fn dirty_paths_lists_edits_and_new_files() {
    let r = rig();
    assert!(git::dirty_paths(&r.repo).unwrap().is_empty());
    write(&r.repo, "a.txt", "edit\n");
    write(&r.repo, "c.txt", "new\n");
    assert_eq!(
        git::dirty_paths(&r.repo).unwrap(),
        vec!["a.txt".to_string(), "c.txt".to_string()]
    );
}

#[test]
fn commit_all_commits_the_working_state_with_the_message_given() {
    let r = rig();
    write(&r.repo, "a.txt", "edit\n");
    write(&r.repo, "c.txt", "new\n");
    let sha = git::commit_all(&r.repo, "Title\n\nBody\n").unwrap();
    assert_eq!(sha, git::head_sha(&r.repo).unwrap());
    assert_eq!(run(&r.repo, &["log", "-1", "--format=%B"]), "Title\n\nBody");
    assert_eq!(run(&r.repo, &["status", "--porcelain"]), "");
}

#[test]
fn a_git_failure_keeps_the_command_and_its_stderr() {
    let r = rig();
    let err = git::run(&r.repo, &["log", "--no-such-flag"]).unwrap_err();
    match err {
        GitError::Failed { command, stderr } => {
            assert!(command.starts_with("git log"), "{command}");
            assert!(!stderr.is_empty());
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn the_git_command_clears_location_variables_and_forces_the_c_locale() {
    let cmd = git::git_command(
        std::path::Path::new("/"),
        &["status"],
        &[("GIT_EXTRA", "1")],
    );
    let envs: Vec<(String, Option<String>)> = cmd
        .get_envs()
        .map(|(k, v)| {
            (
                k.to_string_lossy().into_owned(),
                v.map(|v| v.to_string_lossy().into_owned()),
            )
        })
        .collect();
    for name in [
        "GIT_DIR",
        "GIT_WORK_TREE",
        "GIT_INDEX_FILE",
        "GIT_COMMON_DIR",
        "GIT_NAMESPACE",
    ] {
        assert!(
            envs.contains(&(name.to_string(), None)),
            "{name} must be removed"
        );
    }
    assert!(envs.contains(&("LC_ALL".to_string(), Some("C".to_string()))));
    assert!(envs.contains(&("GIT_TERMINAL_PROMPT".to_string(), Some("0".to_string()))));
    assert!(envs.contains(&("GIT_EXTRA".to_string(), Some("1".to_string()))));
    assert_eq!(cmd.get_program(), "git");
    let args: Vec<String> = cmd
        .get_args()
        .map(|a| a.to_string_lossy().into_owned())
        .collect();
    assert_eq!(args, vec!["-c", "core.quotepath=off", "status"]);
}
