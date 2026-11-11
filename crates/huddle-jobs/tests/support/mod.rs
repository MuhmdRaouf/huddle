#![allow(dead_code)]

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

pub struct Rig {
    pub home: TempHome,
    pub env: Env,
    pub paths: Paths,
    pub repo: PathBuf,
}

/// A temp home and a repository on branch main (TempHome::make_repo: identity, signing off and
/// hooks off through its config), with a.txt committed.
pub fn rig() -> Rig {
    let home = TempHome::new();
    let pairs: Vec<(String, String)> = home.env();
    let refs: Vec<(&str, &str)> = pairs
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let env = Env::with(&refs);
    let paths = Paths::from_env(&env).expect("paths");
    let repo = home
        .make_repo()
        .expect("make repo")
        .canonicalize()
        .expect("canonicalize");
    write(&repo, "a.txt", "one\n");
    git(&repo, &["add", "-A"]);
    git(&repo, &["commit", "-q", "-m", "add a"]);
    Rig {
        home,
        env,
        paths,
        repo,
    }
}

/// Runs git with the same local config isolation; panics with stderr on failure.
pub fn git(dir: &Path, args: &[&str]) -> String {
    let out = Command::new("git")
        .arg("-C")
        .arg(dir)
        .args(args)
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .output()
        .expect("spawn git");
    assert!(
        out.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8(out.stdout)
        .expect("utf8")
        .trim()
        .to_string()
}

pub fn write(dir: &Path, rel: &str, text: &str) {
    let path = dir.join(rel);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).expect("mkdir");
    }
    fs::write(path, text).expect("write");
}

pub fn read(dir: &Path, rel: &str) -> String {
    fs::read_to_string(dir.join(rel)).expect("read")
}

/// A write job fixture on this repository's HEAD; version 0 and no attempts.
/// The job's folder is the rig's repository.
pub fn sample_job(
    r: &Rig,
    id: &str,
    access: huddle_jobs::model::Access,
    state: huddle_jobs::model::JobState,
) -> huddle_jobs::model::Job {
    use huddle_jobs::model::Job;
    Job {
        id: id.to_string(),
        harness: "opencode".to_string(),
        title: "Do the thing".to_string(),
        task: "Do the thing".to_string(),
        access,
        effort: None,
        state,
        repo_root: r.repo.to_string_lossy().into_owned(),
        base_sha: git(&r.repo, &["rev-parse", "HEAD"]),
        base_branch: Some(git(&r.repo, &["symbolic-ref", "--short", "HEAD"])),
        dir: r.repo.to_string_lossy().into_owned(),
        start_tree: None,
        dirty_at_start: false,
        attempts: vec![],
        failure: None,
        decision: None,
        created_at: "2026-11-09T10:00:00.000Z".to_string(),
        updated_at: "2026-11-09T10:00:00.000Z".to_string(),
        version: 0,
    }
}

/// An Env whose `HUDDLE_OPENCODE_BIN` is a fake opencode from the test kit answering with this final text and exit code.
/// `sample_job` uses the `opencode` harness.
pub fn fake_harness_env(r: &Rig, final_text: &str, exit_code: i32) -> Env {
    use huddle_testkit::fake_harness::{FakeHarness, Mode};
    let fake = FakeHarness::install(
        &r.home,
        "opencode",
        Mode::Jsonl {
            session: "ses_1".to_string(),
            text: final_text.to_string(),
        },
        exit_code,
        "boom\n",
    )
    .expect("install fake harness");
    env_with(r, "HUDDLE_OPENCODE_BIN", &fake.path().to_string_lossy())
}

/// This rig's environment plus one variable.
pub fn env_with(r: &Rig, name: &str, value: &str) -> Env {
    let mut pairs = r.home.env();
    pairs.push((name.to_string(), value.to_string()));
    let refs: Vec<(&str, &str)> = pairs
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    Env::with(&refs)
}

/// As `fake_harness_env`, but the harness first writes `file` in its working folder.
pub fn fake_harness_env_writing(r: &Rig, final_text: &str, file: &str) -> Env {
    let inner = fake_harness_env(r, final_text, 0);
    let real = inner
        .get("HUDDLE_OPENCODE_BIN")
        .expect("fake harness path")
        .to_string();
    let wrapper = r.home.root().join("writing-harness");
    huddle_testkit::script::write_executable(
        &wrapper,
        &format!("#!/bin/sh\necho changed > '{file}'\nexec '{real}' \"$@\"\n"),
    )
    .expect("wrapper");
    env_with(r, "HUDDLE_OPENCODE_BIN", &wrapper.to_string_lossy())
}
