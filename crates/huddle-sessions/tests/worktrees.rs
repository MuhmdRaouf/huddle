//! `worktrees` over a temp git repo with an agent worktree.

use std::fs;

use huddle_sessions::{WorktreeKind, worktrees};
use huddle_testkit::temp_home::TempHome;

#[test]
fn d1_lists_checkout_and_agent_worktrees_with_counts() {
    let home = TempHome::new();
    let repo = home.make_repo().expect("repo");
    fs::write(repo.join(".git/info/exclude"), ".agents/\n").expect("exclude");
    let agent = repo.join(".agents/worktrees/dv2-d1");
    let target = agent.to_str().expect("utf8 path");
    home.git(&repo, &["worktree", "add", "-q", "-b", "dv2/d1", target])
        .expect("worktree add");
    fs::write(repo.join("a.txt"), "x").expect("a");
    fs::write(repo.join("b.txt"), "x").expect("b");
    fs::write(agent.join("c.txt"), "x").expect("c");

    let canonical = repo.canonicalize().expect("canonical repo");
    let rows = worktrees(&[repo.clone(), repo.clone()]);
    assert_eq!(rows.len(), 2, "a repeated cwd is listed once: {rows:?}");

    let checkout = rows
        .iter()
        .find(|w| w.kind == WorktreeKind::Checkout)
        .expect("checkout");
    assert_eq!(checkout.path, canonical.to_str().expect("utf8"));
    assert_eq!(checkout.project, "repo");
    assert_eq!(checkout.branch.as_deref(), Some("main"));
    assert_eq!(checkout.changes, Some(2));
    assert_eq!(checkout.used_by, None);
    assert_eq!(checkout.head.as_deref().map(str::len), Some(8));
    assert!(checkout.last_commit_ms.is_some_and(|ms| ms > 0));

    let a = rows
        .iter()
        .find(|w| w.kind == WorktreeKind::Agent)
        .expect("agent");
    assert_eq!(a.branch.as_deref(), Some("dv2/d1"));
    assert_eq!(a.changes, Some(1));
    assert_eq!(a.project, "repo");
}

#[test]
fn d1_a_cwd_outside_git_lists_nothing() {
    let home = TempHome::new();
    let plain = home.root().join("plain");
    fs::create_dir(&plain).expect("plain dir");
    assert!(worktrees(&[plain, home.root().join("missing")]).is_empty());
}

#[test]
fn l29_the_worktrees_returned_are_unchanged() {
    let home = TempHome::new();
    let repo = home.make_repo().expect("repo");
    fs::write(repo.join(".git/info/exclude"), ".agents/\n").expect("exclude");
    let agent = repo.join(".agents/worktrees/dv2-l29");
    let target = agent.to_str().expect("utf8 path");
    home.git(&repo, &["worktree", "add", "-q", "-b", "dv2/l29", target])
        .expect("worktree add");
    fs::write(repo.join("a.txt"), "x").expect("a");
    fs::write(agent.join("b.txt"), "x").expect("b");

    // 30 sessions in one repo: the main checkout, its subfolders and the agent worktree,
    // whose own sessions resolve to the same repository.
    let mut cwds = vec![repo.clone()];
    for n in 0..26 {
        let dir = repo.join(format!("pkg{n}"));
        fs::create_dir(&dir).expect("session folder");
        cwds.push(dir);
    }
    cwds.push(agent.clone());
    let nested = repo.join("pkg0/src");
    fs::create_dir(&nested).expect("nested folder");
    cwds.push(nested);
    let agent_sub = agent.join("sub");
    fs::create_dir(&agent_sub).expect("agent folder");
    cwds.push(agent_sub);
    assert_eq!(cwds.len(), 30);

    let rows = worktrees(&cwds);
    let single = worktrees(std::slice::from_ref(&repo));
    assert_eq!(rows, single, "30 cwds list the same worktrees as one cwd");

    let canonical = repo.canonicalize().expect("canonical repo");
    let repo_key = canonical.to_str().expect("utf8 repo");
    assert_eq!(rows.len(), 2, "the repo and the agent worktree: {rows:?}");
    let checkout = &rows[0];
    assert_eq!(checkout.path, repo_key);
    assert_eq!(checkout.project, "repo");
    assert_eq!(checkout.repo, repo_key);
    assert_eq!(checkout.branch.as_deref(), Some("main"));
    assert_eq!(checkout.kind, WorktreeKind::Checkout);
    assert_eq!(checkout.changes, Some(1));
    assert_eq!(checkout.used_by, None);
    assert_eq!(checkout.head.as_deref().map(str::len), Some(8));
    assert!(checkout.last_commit_ms.is_some_and(|ms| ms > 0));

    let agent_row = &rows[1];
    let agent_key = agent.canonicalize().expect("canonical agent");
    assert_eq!(agent_row.path, agent_key.to_str().expect("utf8 agent"));
    assert_eq!(agent_row.project, "repo");
    assert_eq!(agent_row.repo, repo_key);
    assert_eq!(agent_row.branch.as_deref(), Some("dv2/l29"));
    assert_eq!(agent_row.kind, WorktreeKind::Agent);
    assert_eq!(agent_row.changes, Some(1));
    assert_eq!(agent_row.used_by, None);
    assert_eq!(agent_row.head.as_deref().map(str::len), Some(8));
    assert!(agent_row.last_commit_ms.is_some_and(|ms| ms > 0));
}
