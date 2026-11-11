mod e2e_support;

use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::fs;
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
        Rig::with_harness(FAKE_HARNESS)
    }

    fn with_harness(script: &str) -> Rig {
        let home = TempHome::new();
        let repo = home.root().join("repo");
        fs::create_dir_all(&repo).expect("mkdir repo");
        let harness = home.root().join("fake-amp");
        write_executable(&harness, script).expect("write harness");
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
        self.huddle_in(&self.repo, args)
    }

    fn huddle_in(&self, dir: &Path, args: &[&str]) -> Output {
        Command::new(env!("CARGO_BIN_EXE_huddle"))
            .args(args)
            .current_dir(dir)
            .env_clear()
            .envs(self.home.env())
            .env("PATH", "/usr/bin:/bin")
            .env("HUDDLE_AMP_BIN", &self.harness)
            .output()
            .expect("run huddle")
    }

    fn worktree_count(&self) -> usize {
        let out = Command::new("git")
            .args(["worktree", "list"])
            .current_dir(&self.repo)
            .output()
            .expect("git worktree list");
        String::from_utf8_lossy(&out.stdout).lines().count()
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
fn t3_run_works_in_place_then_diff_and_accept_commit_on_the_current_branch() {
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
    assert_eq!(
        Path::new(&string_field(&job, "dir")),
        rig.repo.canonicalize().expect("canonical repo"),
        "the job runs in the caller's folder"
    );
    assert!(
        rig.repo.join("created_by_harness.txt").exists(),
        "the harness changed the folder in place"
    );
    assert_eq!(rig.worktree_count(), 1, "no worktree is made by run");

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
    let log = Command::new("git")
        .args(["log", "-1", "--format=%s"])
        .current_dir(&rig.repo)
        .output()
        .expect("git log");
    assert_eq!(
        String::from_utf8_lossy(&log.stdout).trim(),
        "add a note to the repository"
    );
    assert_eq!(rig.worktree_count(), 1, "accept leaves no worktree either");
}

#[test]
fn t3_dir_runs_and_commits_in_a_worktree_the_session_made() {
    let rig = Rig::new();
    let wt = rig.home.root().join("session-wt");
    rig.git(&[
        "worktree",
        "add",
        "-q",
        "-b",
        "session",
        wt.to_str().expect("utf8"),
    ]);

    let run = rig.huddle(&[
        "run",
        "amp",
        "--dir",
        wt.to_str().expect("utf8"),
        "add a note to the repository",
    ]);
    assert_eq!(
        run.status.code(),
        Some(0),
        "{}{}",
        stdout(&run),
        String::from_utf8_lossy(&run.stderr)
    );
    assert!(wt.join("created_by_harness.txt").exists());
    assert!(!rig.repo.join("created_by_harness.txt").exists());

    let job = rig.one_job();
    let id = string_field(&job, "id");
    assert_eq!(
        Path::new(&string_field(&job, "dir")),
        wt.canonicalize().expect("canonical worktree")
    );
    let accept = rig.huddle(&["result", &id, "--accept"]);
    assert_eq!(
        accept.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&accept.stderr)
    );
    let branch_log = Command::new("git")
        .args(["log", "session", "-1", "--format=%s"])
        .current_dir(&rig.repo)
        .output()
        .expect("git log");
    assert_eq!(
        String::from_utf8_lossy(&branch_log.stdout).trim(),
        "add a note to the repository"
    );
    let main_log = Command::new("git")
        .args(["log", "main", "-1", "--format=%s"])
        .current_dir(&rig.repo)
        .output()
        .expect("git log");
    assert_eq!(String::from_utf8_lossy(&main_log.stdout).trim(), "init");
    assert_eq!(rig.worktree_count(), 2, "only the session's own worktree");
}

#[test]
fn t3_a_second_write_job_in_the_same_folder_is_refused_while_one_runs() {
    let rig = Rig::with_harness(
        "#!/bin/sh\ncase \"$1\" in --version) echo 1.0; exit 0;; esac\nsleep 30\n",
    );
    let first = rig.huddle(&["run", "amp", "--background", "first task"]);
    assert_eq!(
        first.status.code(),
        Some(0),
        "{}{}",
        stdout(&first),
        String::from_utf8_lossy(&first.stderr)
    );
    // The detached driver writes its process record within moments; a status sweep before that
    // sees a running job with no driver.
    std::thread::sleep(std::time::Duration::from_secs(2));
    let job = rig.one_job();
    let id = string_field(&job, "id");

    let second = rig.huddle(&["run", "amp", "--background", "second task"]);
    assert_ne!(second.status.code(), Some(0));
    let err = String::from_utf8_lossy(&second.stderr).into_owned();
    assert!(
        err.contains(&format!("job {id} is still running a write job")),
        "{err}"
    );

    let cancel = rig.huddle(&["cancel", &id]);
    assert_eq!(cancel.status.code(), Some(0));
}

#[test]
fn t3_a_cancel_leaves_the_folders_files_as_they_are() {
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

    let cancel = rig.huddle(&["cancel", &id]);
    assert_eq!(
        cancel.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&cancel.stderr)
    );

    assert!(
        rig.repo.join("created_by_harness.txt").exists(),
        "cancel leaves the harness's file"
    );
    let after = rig.one_job();
    assert_eq!(after["state"], "cancelled", "{after}");
}

#[test]
fn t3_job_files_on_disk_are_toml() {
    let rig = Rig::new();
    let run = rig.huddle(&["run", "amp", "add a note"]);
    assert_eq!(run.status.code(), Some(0));
    let id = string_field(&rig.one_job(), "id");
    let jobs = rig.home.root().join(".local/state/huddle/jobs");
    let dir = fs::read_dir(&jobs)
        .expect("jobs root")
        .filter_map(Result::ok)
        .map(|entry| entry.path().join(&id))
        .find(|path| path.exists())
        .expect("job dir");
    let names: Vec<String> = fs::read_dir(&dir)
        .expect("job dir")
        .filter_map(Result::ok)
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .collect();
    assert!(names.contains(&"job.toml".to_string()), "{names:?}");
    assert!(
        names.iter().all(|name| !name.ends_with(".json")),
        "no JSON record is kept: {names:?}"
    );
}
