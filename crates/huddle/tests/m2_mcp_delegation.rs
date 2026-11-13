#[path = "../../huddle-jobs/tests/support/mod.rs"]
mod support;

mod e2e_support;
use e2e_support::wait_drivers_done;

use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Stdio};
use std::time::{Duration, Instant};

use huddle_jobs::model::JobState;
use huddle_jobs::store::JobStore;
use serde_json::{Value, json};
use support::{Rig, fake_harness_env, fake_harness_env_writing, rig};

const REPORT: &str = r#"{"summary":"Renamed foo","files":[],"open_items":[]}"#;

struct Session {
    child: Child,
    reader: BufReader<std::process::ChildStdout>,
    next_id: u64,
}

impl Session {
    fn start(r: &Rig, harness_bin: &str) -> Session {
        let mut child = r
            .home
            .command(env!("CARGO_BIN_EXE_huddle"))
            .arg("mcp")
            .current_dir(&r.repo)
            .env("HUDDLE_OPENCODE_BIN", harness_bin)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let reader = BufReader::new(child.stdout.take().unwrap());
        let mut session = Session {
            child,
            reader,
            next_id: 1,
        };
        session.request(
            "initialize",
            json!({
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "m2-test", "version": "0"}
            }),
        );
        session.send(json!({"jsonrpc": "2.0", "method": "notifications/initialized"}));
        session
    }

    fn send(&mut self, message: Value) {
        let stdin = self.child.stdin.as_mut().unwrap();
        writeln!(stdin, "{message}").unwrap();
        stdin.flush().unwrap();
    }

    fn request(&mut self, method: &str, params: Value) -> Value {
        let id = self.next_id;
        self.next_id += 1;
        self.send(json!({"jsonrpc": "2.0", "id": id, "method": method, "params": params}));
        let mut line = String::new();
        self.reader.read_line(&mut line).unwrap();
        serde_json::from_str(&line).unwrap()
    }

    /// The whole tools/call answer: `structuredContent` on success, `isError` on a tool error.
    fn call(&mut self, tool: &str, arguments: Value) -> Value {
        let answer = self.request("tools/call", json!({"name": tool, "arguments": arguments}));
        answer["result"].clone()
    }

    fn ok(&mut self, tool: &str, arguments: Value) -> Value {
        let result = self.call(tool, arguments);
        assert_ne!(result["isError"], true, "{tool}: {result}");
        result["structuredContent"].clone()
    }

    fn finish(mut self) {
        drop(self.child.stdin.take());
        let _ = self.child.wait();
    }
}

fn harness_path(env: &huddle_config::env::Env) -> String {
    env.get("HUDDLE_OPENCODE_BIN").unwrap().to_string()
}

fn wait_for_state(store: &JobStore, id: &str, state: JobState) {
    let deadline = Instant::now() + Duration::from_secs(20);
    while store.get(id).unwrap().state != state {
        assert!(
            Instant::now() < deadline,
            "job {id} never reached {state:?}"
        );
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[test]
fn m2_run_status_result_round_trip() {
    let r = rig();
    let env = fake_harness_env_writing(&r, REPORT, "b.txt");
    let mut session = Session::start(&r, &harness_path(&env));

    let tools = session.request("tools/list", json!({}));
    let names: Vec<&str> = tools["result"]["tools"]
        .as_array()
        .unwrap()
        .iter()
        .map(|tool| tool["name"].as_str().unwrap())
        .collect();
    for name in [
        "huddle_run",
        "huddle_status",
        "huddle_result",
        "huddle_cancel",
    ] {
        assert!(names.contains(&name), "{names:?}");
    }

    let run = session.ok(
        "huddle_run",
        json!({"harness": "opencode", "task": "Rename foo"}),
    );
    let id = run["job_id"].as_str().unwrap().to_string();
    assert_eq!(run["state"], "awaiting_review");
    assert!(
        run["report"].as_str().unwrap().contains("Renamed foo"),
        "{run}"
    );

    let status = session.ok("huddle_status", json!({}));
    let jobs = status["jobs"].as_array().unwrap();
    assert_eq!(jobs.len(), 1, "{status}");
    assert_eq!(jobs[0]["id"], id.as_str());
    assert_eq!(jobs[0]["state"], "awaiting_review");

    let all = session.ok("huddle_status", json!({"all_repos": true}));
    assert!(
        all["jobs"]
            .as_array()
            .unwrap()
            .iter()
            .any(|job| job["id"] == id.as_str())
    );

    let result = session.ok("huddle_result", json!({"id": id}));
    assert!(result["text"].as_str().unwrap().contains("Renamed foo"));
    assert!(!result["text"].as_str().unwrap().contains("+changed"));
    let with_diff = session.ok("huddle_result", json!({"id": id, "diff": true}));
    assert!(
        with_diff["text"].as_str().unwrap().contains("changed"),
        "{with_diff}"
    );
    session.finish();
    wait_drivers_done(r.home.root());
}

fn normalise(text: &str, id: &str, sha_of: &Rig) -> String {
    let sha = support::git(&sha_of.repo, &["rev-parse", "HEAD"]);
    text.replace(id, "<id>")
        .replace(sha.get(..12).unwrap(), "<sha>")
        .replace(sha_of.repo.to_str().unwrap(), "<dir>")
}

fn cli_job(r: &Rig, bin: &str) -> String {
    let out = r
        .home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(["run", "opencode", "Rename", "foo"])
        .current_dir(&r.repo)
        .env("HUDDLE_OPENCODE_BIN", bin)
        .output()
        .unwrap();
    let text = String::from_utf8_lossy(&out.stdout).into_owned();
    text.lines()
        .next()
        .and_then(|line| line.strip_prefix("huddle job "))
        .and_then(|rest| rest.split(' ').next())
        .unwrap()
        .to_string()
}

fn cli(r: &Rig, bin: &str, args: &[&str]) -> String {
    let out = r
        .home
        .command(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .current_dir(&r.repo)
        .env("HUDDLE_OPENCODE_BIN", bin)
        .output()
        .unwrap();
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

#[test]
fn m2_accept_and_return_match_the_cli() {
    let cli_rig = rig();
    let tool_rig = rig();
    let cli_env = fake_harness_env_writing(&cli_rig, REPORT, "b.txt");
    let tool_env = fake_harness_env_writing(&tool_rig, REPORT, "b.txt");
    let (cli_bin, tool_bin) = (harness_path(&cli_env), harness_path(&tool_env));

    let cli_id = cli_job(&cli_rig, &cli_bin);
    let mut session = Session::start(&tool_rig, &tool_bin);
    let run = session.ok(
        "huddle_run",
        json!({"harness": "opencode", "task": "Rename foo", "background": true}),
    );
    let tool_id = run["job_id"].as_str().unwrap().to_string();
    let tool_store = JobStore::for_repo(&tool_rig.paths, &tool_rig.repo);
    wait_for_state(&tool_store, &tool_id, JobState::AwaitingReview);

    // return with feedback
    let cli_returned = cli(
        &cli_rig,
        &cli_bin,
        &["result", &cli_id, "--return", "Also rename bar"],
    );
    let tool_returned = session.ok(
        "huddle_result",
        json!({"id": tool_id, "action": "return", "feedback": "Also rename bar"}),
    );
    assert_eq!(
        normalise(tool_returned["text"].as_str().unwrap(), &tool_id, &tool_rig),
        normalise(&cli_returned, &cli_id, &cli_rig)
    );
    let cli_store = JobStore::for_repo(&cli_rig.paths, &cli_rig.repo);
    for (store, id) in [(&cli_store, &cli_id), (&tool_store, &tool_id)] {
        let deadline = Instant::now() + Duration::from_secs(20);
        while store.get(id).unwrap().attempts.len() < 2
            || store.get(id).unwrap().state != JobState::AwaitingReview
        {
            assert!(Instant::now() < deadline, "the returned job never settled");
            std::thread::sleep(Duration::from_millis(100));
        }
    }

    // accept with a commit
    let cli_accepted = cli(&cli_rig, &cli_bin, &["result", &cli_id, "--accept"]);
    let tool_accepted = session.ok("huddle_result", json!({"id": tool_id, "action": "accept"}));
    assert_eq!(
        normalise(tool_accepted["text"].as_str().unwrap(), &tool_id, &tool_rig),
        normalise(&cli_accepted, &cli_id, &cli_rig)
    );
    assert_eq!(tool_accepted["state"], "accepted");
    assert_eq!(
        cli_store.get(&cli_id).unwrap().state,
        tool_store.get(&tool_id).unwrap().state
    );
    session.finish();
    wait_drivers_done(cli_rig.home.root());
    wait_drivers_done(tool_rig.home.root());
}

#[test]
fn m2_accept_without_commit_leaves_the_change_uncommitted() {
    let r = rig();
    let env = fake_harness_env_writing(&r, REPORT, "b.txt");
    let mut session = Session::start(&r, &harness_path(&env));
    let run = session.ok(
        "huddle_run",
        json!({"harness": "opencode", "task": "Rename foo"}),
    );
    let id = run["job_id"].as_str().unwrap().to_string();
    let head = support::git(&r.repo, &["rev-parse", "HEAD"]);
    let accepted = session.ok(
        "huddle_result",
        json!({"id": id, "action": "accept_no_commit"}),
    );
    assert!(
        accepted["text"]
            .as_str()
            .unwrap()
            .contains("stays uncommitted")
    );
    assert_eq!(support::git(&r.repo, &["rev-parse", "HEAD"]), head);
    session.finish();
    wait_drivers_done(r.home.root());
}

#[test]
fn m2_cancel_stops_a_running_job() {
    let r = rig();
    let slow = r.home.root().join("slow-harness");
    huddle_testkit::script::write_executable(&slow, "#!/bin/sh\nexec sleep 60\n").unwrap();
    let mut session = Session::start(&r, slow.to_str().unwrap());
    let run = session.ok(
        "huddle_run",
        json!({"harness": "opencode", "task": "Take long", "background": true}),
    );
    let id = run["job_id"].as_str().unwrap().to_string();
    let store = JobStore::for_repo(&r.paths, &r.repo);
    wait_for_state(&store, &id, JobState::Running);

    let cancelled = session.ok("huddle_cancel", json!({"id": id}));
    assert_eq!(cancelled["state"], "cancelled");
    assert_eq!(store.get(&id).unwrap().state, JobState::Cancelled);
    session.finish();
    wait_drivers_done(r.home.root());
}

#[test]
fn m2_a_bad_harness_is_a_tool_error_not_a_crash() {
    let r = rig();
    let env = fake_harness_env(&r, REPORT, 0);
    let mut session = Session::start(&r, &harness_path(&env));
    let result = session.call(
        "huddle_run",
        json!({"harness": "no-such-harness", "task": "x"}),
    );
    assert_eq!(result["isError"], true, "{result}");
    assert!(
        result["content"][0]["text"]
            .as_str()
            .unwrap()
            .contains("no harness named"),
        "{result}"
    );
    let missing = session.call("huddle_result", json!({"id": "nope"}));
    assert_eq!(missing["isError"], true, "{missing}");
    let version = session.ok("huddle_version", json!({}));
    assert!(version["version"].is_string());
    session.finish();
}
