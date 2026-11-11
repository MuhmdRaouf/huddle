//! `worktrees` over a temp git repo with an agent worktree and a job worktree.

use std::fs;

use huddle_sessions::{WorktreeKind, worktrees};
use huddle_testkit::temp_home::TempHome;

#[test]
fn d1_lists_checkout_agent_and_job_worktrees_with_counts() {
    let home = TempHome::new();
    let repo = home.make_repo().expect("repo");
    fs::write(repo.join(".git/info/exclude"), ".agents/\n").expect("exclude");
    let agent = repo.join(".agents/worktrees/dv2-d1");
    let jobs = home.root().join("jobs");
    let job = jobs.join("repo-slug/job-1");
    for (path, branch) in [(&agent, "dv2/d1"), (&job, "job/1")] {
        let target = path.to_str().expect("utf8 path");
        home.git(&repo, &["worktree", "add", "-q", "-b", branch, target])
            .expect("worktree add");
    }
    fs::write(repo.join("a.txt"), "x").expect("a");
    fs::write(repo.join("b.txt"), "x").expect("b");
    fs::write(agent.join("c.txt"), "x").expect("c");

    let canonical = repo.canonicalize().expect("canonical repo");
    let jobs_root = jobs.canonicalize().expect("canonical jobs");
    let rows = worktrees(&[repo.clone(), repo.clone()], &jobs_root);
    assert_eq!(rows.len(), 3, "a repeated cwd is listed once: {rows:?}");

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

    let j = rows
        .iter()
        .find(|w| w.kind == WorktreeKind::Job)
        .expect("job");
    assert_eq!(j.branch.as_deref(), Some("job/1"));
    assert_eq!(j.changes, Some(0));
}

#[test]
fn d1_a_cwd_outside_git_lists_nothing() {
    let home = TempHome::new();
    let plain = home.root().join("plain");
    fs::create_dir(&plain).expect("plain dir");
    assert!(worktrees(&[plain, home.root().join("missing")], home.root()).is_empty());
}
