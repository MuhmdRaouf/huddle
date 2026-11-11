mod e2e_support;

use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::PathBuf;
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
        write_executable(&harness, FAKE_HARNESS).expect("write harness");
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
fn a_harness_outside_its_scope_is_flagged_and_claude_still_decides() {
    let rig = Rig::new();
    let run = rig.huddle(&["run", "amp", "--scope", "src/**", "add a note"]);
    assert!(
        stdout(&run).contains("Verdict: scope_violation"),
        "{}",
        stdout(&run)
    );
    assert!(
        stdout(&run).contains("outside scope: created_by_harness.txt"),
        "{}",
        stdout(&run)
    );

    let id = string_field(&rig.one_job(), "id");
    assert!(stdout(&rig.huddle(&["result", &id])).contains("Verdict: scope_violation"));
    let accept = rig.huddle(&["result", &id, "--accept"]);
    assert_eq!(
        accept.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&accept.stderr)
    );
    assert!(
        rig.repo.join("created_by_harness.txt").exists(),
        "a verdict never blocks Claude's decision"
    );
}

#[test]
fn a_gate_that_passes_commits_and_runs_again_in_the_folder() {
    let rig = Rig::new();
    let run = rig.huddle(&[
        "run",
        "amp",
        "--gate",
        "test -f created_by_harness.txt",
        "add a note",
    ]);
    assert!(stdout(&run).contains("Verdict: pass"), "{}", stdout(&run));
    let id = string_field(&rig.one_job(), "id");
    let accept = rig.huddle(&["result", &id, "--accept"]);
    assert_eq!(
        accept.status.code(),
        Some(0),
        "{}",
        String::from_utf8_lossy(&accept.stderr)
    );
    assert!(rig.repo.join("created_by_harness.txt").exists());
}

#[test]
fn accept_refuses_when_a_gate_breaks_after_the_run() {
    let rig = Rig::new();
    let run = rig.huddle(&["run", "amp", "--gate", "test ! -f landed.txt", "add a note"]);
    assert!(stdout(&run).contains("Verdict: pass"), "{}", stdout(&run));
    let id = string_field(&rig.one_job(), "id");

    fs::write(rig.repo.join("landed.txt"), "arrived after the run\n").expect("write");

    let accept = rig.huddle(&["result", &id, "--accept"]);
    assert_ne!(accept.status.code(), Some(0));
    assert!(
        String::from_utf8_lossy(&accept.stderr).contains("test ! -f landed.txt failed"),
        "{}",
        String::from_utf8_lossy(&accept.stderr)
    );
    let log = Command::new("git")
        .args(["log", "-1", "--format=%s"])
        .current_dir(&rig.repo)
        .output()
        .expect("git log");
    assert_eq!(
        String::from_utf8_lossy(&log.stdout).trim(),
        "init",
        "nothing was committed"
    );
}
