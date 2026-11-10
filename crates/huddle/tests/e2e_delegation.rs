mod e2e_support;

use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

const FAKE_HARNESS: &str = "#!/bin/sh\nprintf 'hello from the harness\\n' > created_by_harness.txt\nprintf '{\"summary\":\"added a note\",\"files\":[\"created_by_harness.txt\"],\"open_items\":[]}\\n'\n";

struct Rig {
    home: TempHome,
    repo: PathBuf,
    harness: PathBuf,
}

impl Rig {
    fn new() -> Rig {
        let home = TempHome::new();
        let repo = home.root().join("repo");
        fs::create_dir_all(&repo).expect("mkdir repo");
        let harness = home.root().join("fake-amp");
        fs::write(&harness, FAKE_HARNESS).expect("write harness");
        fs::set_permissions(&harness, fs::Permissions::from_mode(0o755)).expect("chmod");
        let rig = Rig {
            home,
            repo,
            harness,
        };
        rig.git(&["init", "-b", "main"]);
        rig.git(&["config", "user.email", "t@example.com"]);
        rig.git(&["config", "user.name", "Tester"]);
        fs::write(rig.repo.join("README.md"), "# repo\n").expect("write readme");
        rig.git(&["add", "README.md"]);
        rig.git(&["commit", "-m", "init"]);
        rig
    }

    fn git(&self, args: &[&str]) {
        let status = Command::new("git")
            .args(args)
            .current_dir(&self.repo)
            .status()
            .expect("git");
        assert!(status.success(), "git {args:?}");
    }

    fn huddle(&self, args: &[&str]) -> Output {
        Command::new(env!("CARGO_BIN_EXE_huddle"))
            .args(args)
            .current_dir(&self.repo)
            .env_clear()
            .envs(self.home.env())
            .env("PATH", "/usr/bin:/bin")
            .env("HUDDLE_AMP_BIN", &self.harness)
            .output()
            .expect("run huddle")
    }

    fn one_job(&self) -> serde_json::Value {
        let out = self.huddle(&["status", "--json"]);
        assert_eq!(
            out.status.code(),
            Some(0),
            "{}",
            String::from_utf8_lossy(&out.stderr)
        );
        let jobs: serde_json::Value = serde_json::from_slice(&out.stdout).expect("json");
        let list = jobs.as_array().expect("array");
        assert_eq!(list.len(), 1, "{jobs}");
        list[0].clone()
    }
}

impl Drop for Rig {
    fn drop(&mut self) {
        e2e_support::wait_drivers_done(self.home.root());
    }
}

fn string_field(job: &serde_json::Value, key: &str) -> String {
    job[key]
        .as_str()
        .unwrap_or_else(|| panic!("no {key} in {job}"))
        .to_string()
}

fn stdout(out: &Output) -> String {
    String::from_utf8_lossy(&out.stdout).into_owned()
}

#[test]
fn run_then_diff_then_accept_lands_the_change() {
    let rig = Rig::new();

    let run = rig.huddle(&["run", "amp", "add a note to the repository"]);
    assert_eq!(
        run.status.code(),
        Some(0),
        "{}{}",
        stdout(&run),
        String::from_utf8_lossy(&run.stderr)
    );

    let job = rig.one_job();
    let id = string_field(&job, "id");
    let worktree = string_field(&job, "worktree");
    assert_ne!(
        Path::new(&worktree),
        rig.repo,
        "the job runs in its own worktree"
    );
    assert!(
        Path::new(&worktree).join("created_by_harness.txt").exists(),
        "the harness worked in the worktree"
    );
    assert!(
        !rig.repo.join("created_by_harness.txt").exists(),
        "the repository stays untouched before accept"
    );

    let report = rig.huddle(&["result", &id]);
    assert_eq!(
        report.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&report.stderr)
    );
    assert!(
        stdout(&report).contains("added a note"),
        "{}",
        stdout(&report)
    );

    let diff = rig.huddle(&["result", &id, "--diff"]);
    assert_eq!(
        diff.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&diff.stderr)
    );
    assert!(
        stdout(&diff).contains("created_by_harness.txt"),
        "{}",
        stdout(&diff)
    );

    let accept = rig.huddle(&["result", &id, "--accept"]);
    assert_eq!(
        accept.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&accept.stderr)
    );
    assert!(
        stdout(&accept).contains(&format!("huddle job {id} accepted: commit ")),
        "{}",
        stdout(&accept)
    );
    assert_eq!(
        fs::read_to_string(rig.repo.join("created_by_harness.txt")).expect("read"),
        "hello from the harness\n"
    );
    assert!(
        !Path::new(&worktree).exists(),
        "accept cleaned its worktree"
    );
}

#[test]
fn a_cancel_removes_the_worktree_and_leaves_the_repository_alone() {
    let rig = Rig::new();
    let run = rig.huddle(&["run", "amp", "something else"]);
    assert_eq!(
        run.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&run.stderr)
    );
    let job = rig.one_job();
    let id = string_field(&job, "id");
    let worktree = string_field(&job, "worktree");

    let cancel = rig.huddle(&["cancel", &id]);
    assert_eq!(
        cancel.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&cancel.stderr)
    );

    assert!(
        !Path::new(&worktree).exists(),
        "cancel removed the worktree"
    );
    assert!(!rig.repo.join("created_by_harness.txt").exists());
    let after = rig.one_job();
    assert_eq!(after["state"], "cancelled", "{after}");
}
