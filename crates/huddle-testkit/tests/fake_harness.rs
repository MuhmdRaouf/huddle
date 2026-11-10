use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin, Stdio};

use huddle_testkit::fake_harness::{FakeHarness, Mode, RpcEnd, RpcTurn};
use huddle_testkit::temp_home::TempHome;
use serde_json::Value;

fn run(home: &TempHome, fake: &FakeHarness, args: &[&str]) -> (i32, String) {
    let out = home
        .command(fake.path())
        .args(args)
        .stdin(Stdio::null())
        .output()
        .unwrap();
    (
        out.status.code().unwrap(),
        String::from_utf8_lossy(&out.stdout).to_string(),
    )
}

#[test]
fn text_mode_prints_its_answer_and_records_the_call() {
    let home = TempHome::new();
    let fake = FakeHarness::install(&home, "tool", Mode::Text("answer\n".into()), 0, "").unwrap();
    let (code, out) = run(&home, &fake, &["--flag", "two words"]);
    assert_eq!(code, 0);
    assert_eq!(out, "answer\n");
    let calls = fake.calls().unwrap();
    assert_eq!(calls.len(), 1);
    assert_eq!(
        calls[0].argv[1..],
        ["--flag".to_string(), "two words".to_string()]
    );
    assert!(calls[0].env.iter().any(|(k, _)| k == "HOME"));
}

#[test]
fn exit_code_and_stderr_are_scripted() {
    let home = TempHome::new();
    let fake = FakeHarness::install(&home, "t", Mode::Text(String::new()), 3, "boom\n").unwrap();
    let out = home
        .command(fake.path())
        .stdin(Stdio::null())
        .output()
        .unwrap();
    assert_eq!(out.status.code(), Some(3));
    assert_eq!(out.stderr, b"boom\n");
}

#[test]
fn jsonl_mode_is_opencode_lines() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "opencode",
        Mode::Jsonl {
            session: "ses_1".into(),
            text: "done".into(),
        },
        0,
        "",
    )
    .unwrap();
    let (_, out) = run(&home, &fake, &["run"]);
    let types: Vec<String> = out
        .lines()
        .map(|l| {
            serde_json::from_str::<Value>(l).unwrap()["type"]
                .as_str()
                .unwrap()
                .to_string()
        })
        .collect();
    assert_eq!(types, ["step_start", "text", "step_finish"]);
    assert!(out.contains("\"sessionID\":\"ses_1\""));
}

#[test]
fn jsondoc_mode_is_one_pretty_document() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "zcode",
        Mode::JsonDoc {
            session: "sess_1".into(),
            response: "{}".into(),
        },
        0,
        "",
    )
    .unwrap();
    let (_, out) = run(&home, &fake, &[]);
    let doc: Value = serde_json::from_str(&out).unwrap();
    assert_eq!(doc["sessionId"], "sess_1");
    assert_eq!(doc["projection"]["status"], "idle");
    assert!(out.starts_with("{\n"));
}

#[test]
fn stream_json_mode_ends_with_a_result_line() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "claude",
        Mode::StreamJson {
            session: "sess-9".into(),
            text: "ok".into(),
        },
        0,
        "",
    )
    .unwrap();
    let (_, out) = run(&home, &fake, &[]);
    let last: Value = serde_json::from_str(out.lines().last().unwrap()).unwrap();
    assert_eq!(last["type"], "result");
    assert_eq!(last["result"], "ok");
    assert_eq!(last["session_id"], "sess-9");
}

struct Rpc {
    child: Child,
    stdin: Option<ChildStdin>,
    out: BufReader<std::process::ChildStdout>,
}

impl Rpc {
    fn start(home: &TempHome, fake: &FakeHarness) -> Rpc {
        let mut child = home
            .command(fake.path())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap();
        let stdin = child.stdin.take();
        let out = BufReader::new(child.stdout.take().unwrap());
        Rpc { child, stdin, out }
    }

    fn send(&mut self, frame: &str) {
        writeln!(self.stdin.as_mut().unwrap(), "{frame}").unwrap();
    }

    fn read(&mut self) -> Value {
        let mut line = String::new();
        self.out.read_line(&mut line).unwrap();
        serde_json::from_str(&line).unwrap_or(Value::Null)
    }

    fn finish(mut self) -> i32 {
        self.stdin.take();
        self.child.wait().unwrap().code().unwrap()
    }
}

#[test]
fn omp_rpc_runs_the_handshake_and_a_turn_then_exits_on_eof() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "omp",
        Mode::OmpRpc {
            session: "01abc".into(),
            turns: vec![RpcTurn {
                text: "report text".into(),
            }],
            end: RpcEnd::Normal,
        },
        0,
        "",
    )
    .unwrap();
    let mut rpc = Rpc::start(&home, &fake);
    assert_eq!(rpc.read()["type"], "ready");
    rpc.send(r#"{"id":"c1","type":"negotiate_protocol","protocolVersion":2}"#);
    assert_eq!(rpc.read()["data"]["protocolVersion"], 2);
    rpc.send(r#"{"id":"c2","type":"set_event_filter","events":[]}"#);
    assert_eq!(rpc.read()["id"], "c2");
    rpc.send(r#"{"id":"c3","type":"get_state"}"#);
    assert_eq!(rpc.read()["data"]["sessionId"], "01abc");
    rpc.send(r#"{"id":"c4","type":"prompt","message":"hi"}"#);
    assert_eq!(rpc.read()["id"], "c4");
    assert_eq!(rpc.read()["assistantMessageEvent"]["delta"], "report text");
    assert_eq!(rpc.read()["message"]["stopReason"], "stop");
    let result = rpc.read();
    assert_eq!(result["type"], "prompt_result");
    assert_eq!(result["id"], "c4");
    assert_eq!(rpc.finish(), 0);
    let calls = fake.calls().unwrap();
    assert_eq!(calls[0].stdin.len(), 4);
    assert!(calls[0].stdin[3].contains("\"prompt\""));
}

#[test]
fn omp_hang_answers_only_abort_and_die_after_ready_exits_1() {
    let home = TempHome::new();
    let hang = FakeHarness::install(
        &home,
        "hang",
        Mode::OmpRpc {
            session: "s".into(),
            turns: vec![RpcTurn { text: "x".into() }],
            end: RpcEnd::HangUntilAbort,
        },
        0,
        "",
    )
    .unwrap();
    let mut rpc = Rpc::start(&home, &hang);
    assert_eq!(rpc.read()["type"], "ready");
    rpc.send(r#"{"id":"c1","type":"prompt","message":"hi"}"#);
    assert_eq!(rpc.read()["command"], "prompt");
    rpc.send(r#"{"id":"c2","type":"abort"}"#);
    let aborted = rpc.read();
    assert_eq!(aborted["status"], "aborted");
    assert_eq!(aborted["id"], "c1");
    assert_eq!(rpc.finish(), 0);

    let dead = FakeHarness::install(
        &home,
        "dead",
        Mode::OmpRpc {
            session: "s".into(),
            turns: vec![],
            end: RpcEnd::DieAfterReady,
        },
        0,
        "boom\n",
    )
    .unwrap();
    let mut rpc = Rpc::start(&home, &dead);
    assert_eq!(rpc.read()["type"], "ready");
    assert_eq!(rpc.finish(), 1);
}

#[test]
fn pi_rpc_has_no_ready_frame_and_settles_a_turn() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "pi",
        Mode::PiRpc {
            session: "uuid-1".into(),
            turns: vec![RpcTurn { text: "a".into() }, RpcTurn { text: "b".into() }],
            end: RpcEnd::Normal,
        },
        0,
        "",
    )
    .unwrap();
    let mut rpc = Rpc::start(&home, &fake);
    rpc.send(r#"{"id":"c1","type":"get_state"}"#);
    assert_eq!(rpc.read()["data"]["sessionId"], "uuid-1");
    for (id, text) in [("c2", "a"), ("c3", "b")] {
        rpc.send(&format!(
            r#"{{"id":"{id}","type":"prompt","message":"go"}}"#
        ));
        assert_eq!(rpc.read()["id"], id);
        assert_eq!(rpc.read()["assistantMessageEvent"]["delta"], text);
        assert_eq!(rpc.read()["message"]["stopReason"], "stop");
        assert_eq!(rpc.read()["type"], "agent_settled");
    }
    assert_eq!(rpc.finish(), 0);
}

#[test]
fn pi_crash_mid_turn_exits_3_with_its_stderr() {
    let home = TempHome::new();
    let fake = FakeHarness::install(
        &home,
        "pi",
        Mode::PiRpc {
            session: "s".into(),
            turns: vec![RpcTurn { text: "a".into() }],
            end: RpcEnd::CrashMidTurn,
        },
        0,
        "died\n",
    )
    .unwrap();
    let mut rpc = Rpc::start(&home, &fake);
    rpc.send(r#"{"id":"c1","type":"prompt","message":"go"}"#);
    assert_eq!(rpc.read()["command"], "prompt");
    assert_eq!(rpc.finish(), 3);
}
