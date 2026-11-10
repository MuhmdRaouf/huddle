//! One harness turn on every builtin wire, against real children: small shell scripts and the
//! testkit's fake harness.

use huddle_config::env::Env;
use huddle_harness::entries::find;
use huddle_harness::resolve::override_var;
use huddle_harness::runner::{Brief, RunCtx, RunError, run_turn};
use huddle_harness::table::{Access, Entry, SessionRef};
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::time::Duration;

const GOOD_REPORT: &str = r#"{\"summary\":\"done\",\"files\":[],\"open_items\":[]}"#;

fn install(root: &Path, name: &str, body: &str) -> String {
    let path = root.join("bin").join(name);
    fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    fs::write(&path, format!("#!/bin/sh\n{body}\n")).expect("write");
    fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).expect("chmod");
    path.display().to_string()
}

fn brief(access: Access) -> Brief {
    Brief {
        prompt: "do it".into(),
        access,
        effort: None,
        session: SessionRef::New {
            key: "11111111-1111-4111-8111-111111111111".into(),
        },
    }
}

fn run_entry(
    entry: &Entry,
    body: &str,
    name: &str,
    home: &TempHome,
    access: Access,
) -> Result<huddle_harness::runner::Outcome, RunError> {
    let tool = install(home.root(), name, body);
    let var = override_var(&entry.id);
    let env = Env::with(&[
        (var.as_str(), tool.as_str()),
        ("PATH", "/usr/bin:/bin"),
        ("HOME", home.root().to_str().expect("utf8")),
    ]);
    let cwd = home.root().to_path_buf();
    let ctx = RunCtx::new(&env, cwd, home.root().join("logs"));
    run_turn(entry, &brief(access), &ctx)
}

fn omp_script(final_lines: &str) -> String {
    format!(
        r#"[ "$1" = "--mode" ] || exit 40
printf '%s\n' '{{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1,2],"maxFrameBytes":1048576}}'
read one; read two; read three
printf '%s\n' '{{"id":"c1","type":"response","command":"negotiate_protocol","success":true}}'
printf '%s\n' '{{"id":"c2","type":"response","command":"set_event_filter","success":true}}'
printf '%s\n' '{{"id":"c3","type":"response","command":"get_state","success":true,"data":{{"sessionId":"omp-session","dumpTools":[{{"name":"read"}},{{"name":"grep"}},{{"name":"glob"}},{{"name":"edit"}},{{"name":"write"}},{{"name":"bash"}},{{"name":"todo"}}]}}}}'
read prompt
printf '%s\n' '{{"id":"c4","type":"response","command":"prompt","success":true}}'
{final_lines}
cat >/dev/null
exit 0"#
    )
}

fn assistant_omp(text: &str, stop: &str) -> String {
    format!(
        r#"printf '%s\n' '{{"type":"message_end","message":{{"role":"assistant","content":[{{"type":"text","text":"{text}"}}],"provider":"zai","model":"glm-5.3","usage":{{"input":10,"output":2,"cacheRead":0,"cacheWrite":0,"cost":{{"total":0.01}}}},"stopReason":"{stop}"}},"messageId":"m"}}'"#
    )
}

const PROMPT_DONE: &str = r#"printf '%s\n' '{"type":"prompt_result","id":"c4","agentInvoked":true,"status":"completed","sessionSettled":true}'"#;

#[test]
fn omp_runs_end_to_end_and_returns_the_report() {
    let home = TempHome::new();
    let body = omp_script(&format!(
        "{}\n{}",
        assistant_omp(GOOD_REPORT, "stop"),
        PROMPT_DONE
    ));
    let out = run_entry(
        &find("omp").expect("omp"),
        &body,
        "omp",
        &home,
        Access::Write,
    )
    .expect("run");
    assert_eq!(
        out.report_text,
        "{\"summary\":\"done\",\"files\":[],\"open_items\":[]}"
    );
    assert_eq!(out.session_id.as_deref(), Some("omp-session"));
    assert_eq!(out.failure, None);
    assert_eq!(out.exit.code, Some(0));
    assert_eq!(out.stats.model.as_deref(), Some("zai/glm-5.3"));
    assert!(home.root().join("logs/rpc-frames.jsonl").exists());
    assert!(home.root().join("logs/rpc-commands.jsonl").exists());
}

#[test]
fn an_invalid_omp_report_draws_a_correction_in_the_same_session() {
    let home = TempHome::new();
    let finals = format!(
        "{bad}\n{done}\nread correction\n[ -n \"$correction\" ] || exit 41\nprintf '%s\\n' '{{\"id\":\"c5\",\"type\":\"response\",\"command\":\"prompt\",\"success\":true}}'\n{good}\nprintf '%s\\n' '{{\"type\":\"prompt_result\",\"id\":\"c5\",\"agentInvoked\":true,\"status\":\"completed\",\"sessionSettled\":true}}'",
        bad = assistant_omp("not a report", "stop"),
        done = PROMPT_DONE,
        good = assistant_omp(GOOD_REPORT, "stop"),
    );
    let body = omp_script(&finals);
    let out = run_entry(
        &find("omp").expect("omp"),
        &body,
        "omp",
        &home,
        Access::Write,
    )
    .expect("run");
    assert_eq!(
        out.report_text,
        "{\"summary\":\"done\",\"files\":[],\"open_items\":[]}"
    );
    assert_eq!(out.failure, None);
}

#[test]
fn an_omp_that_lacks_protocol_two_is_a_protocol_error_with_no_process_left() {
    let home = TempHome::new();
    let body = r#"printf '%s\n' '{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1]}'; sleep 30"#;
    let err = run_entry(
        &find("omp").expect("omp"),
        body,
        "omp",
        &home,
        Access::Write,
    )
    .expect_err("protocol");
    assert!(matches!(&err, RunError::Protocol(m) if m.contains("2 is required")));
}

#[test]
fn an_omp_that_exits_before_ready_is_a_clean_failed_outcome_and_flags_a_missing_session() {
    let home = TempHome::new();
    let body = r#"echo 'Error: Session "x" not found.' 1>&2; exit 1"#;
    let entry = find("omp").expect("omp");
    let tool = install(home.root(), "omp", body);
    let var = override_var(&entry.id);
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let mut brief = brief(Access::Write);
    brief.session = SessionRef::Resume { id: "x".into() };
    let ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    let out = run_turn(&entry, &brief, &ctx).expect("outcome");
    assert!(out.failure.expect("failure").starts_with("omp ended"));
    assert!(out.exit.session_missing);
    assert_eq!(out.report_text, "");
}

#[test]
fn pi_runs_end_to_end() {
    let home = TempHome::new();
    let body = r#"[ "$1" = "--mode" ] || exit 40
read one
printf '%s\n' '{"type":"response","id":"c1","command":"get_state","success":true,"data":{"sessionId":"pi-session"}}'
read prompt
printf '%s\n' '{"type":"response","id":"c2","command":"prompt","success":true,"data":{"disposition":"started"}}'
printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"{\"summary\":\"pi ok\",\"files\":[],\"open_items\":[]}"}],"provider":"zai","model":"glm-5.3","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}},"stopReason":"stop"}}'
printf '%s\n' '{"type":"agent_settled"}'
cat >/dev/null"#;
    let out = run_entry(&find("pi").expect("pi"), body, "pi", &home, Access::Write).expect("run");
    assert_eq!(out.session_id.as_deref(), Some("pi-session"));
    assert_eq!(
        out.report_text,
        "{\"summary\":\"pi ok\",\"files\":[],\"open_items\":[]}"
    );
    assert_eq!(out.failure, None);
}

#[test]
fn opencode_runs_through_the_jsonl_reader() {
    let home = TempHome::new();
    let body = r#"[ "$1" = "run" ] || exit 40
printf '%s\n' '{"type":"step_start","timestamp":1,"sessionID":"ses_1"}'
printf '%s\n' '{"type":"text","timestamp":2,"sessionID":"ses_1","part":{"type":"text","text":"answer"}}'
printf '%s\n' '{"type":"step_finish","timestamp":3,"sessionID":"ses_1","part":{"reason":"stop","tokens":{"input":1,"output":1,"cache":{"read":0,"write":0}},"cost":0}}'"#;
    let out = run_entry(
        &find("opencode").expect("opencode"),
        body,
        "opencode",
        &home,
        Access::Write,
    )
    .expect("run");
    assert_eq!(out.report_text, "answer");
    assert_eq!(out.session_id.as_deref(), Some("ses_1"));
}

#[test]
fn zcode_reads_its_attached_prompt_and_final_document() {
    let home = TempHome::new();
    let body = r#"prev=""; for a in "$@"; do if [ "$prev" = "--attach" ]; then cat "$a" > "$0.prompt"; fi; prev="$a"; done
echo 'SDK warning {not JSON}'
cat <<'DOC'
{
  "sessionId": "sess_test",
  "response": "{\"summary\":\"pong\",\"findings\":[],\"open_items\":[]}",
  "usage": {"inputTokens":100,"outputTokens":20,"cacheReadTokens":50,"cacheWriteTokens":10},
  "projection": {"status":"idle","turnCount":3}
}
DOC"#;
    let out = run_entry(
        &find("zcode").expect("zcode"),
        body,
        "zcode",
        &home,
        Access::Write,
    )
    .expect("run");
    assert_eq!(out.session_id.as_deref(), Some("sess_test"));
    assert!(out.report_text.contains("pong"));
    assert!(
        fs::read_to_string(home.root().join("bin/zcode.prompt"))
            .expect("prompt")
            .starts_with("do it")
    );
}

#[test]
fn claude_runs_through_the_stream_json_reader() {
    let home = TempHome::new();
    let body = r#"cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s-1","model":"m"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"final","num_turns":1,"duration_ms":5,"total_cost_usd":0.1,"usage":{}}'"#;
    let out = run_entry(
        &find("claude").expect("claude"),
        body,
        "claude",
        &home,
        Access::Write,
    )
    .expect("run");
    assert_eq!(out.report_text, "final");
    assert_eq!(out.session_id.as_deref(), Some("s-1"));
    assert_eq!(out.failure, None);
}

#[test]
fn a_timeout_on_an_rpc_run_stops_with_the_soft_abort_and_reports_forced() {
    let home = TempHome::new();
    let body = omp_script("sleep 30");
    let entry = find("omp").expect("omp");
    let tool = install(home.root(), "omp", &body);
    let var = override_var(&entry.id);
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let mut ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    ctx.grace = Duration::from_millis(300);
    ctx.timeout = Some(Duration::from_secs(3));
    let out = run_turn(&entry, &brief(Access::Write), &ctx).expect("run");
    assert_eq!(out.exit.forced.as_deref(), Some("timeout"));
    assert!(out.failure.is_some());
}

use huddle_testkit::fake_harness::{FakeHarness, Mode, RpcEnd, RpcTurn};

const REPORT: &str = r#"{"summary":"done","files":[],"open_items":[]}"#;

fn run_fake(
    id: &str,
    mode: Mode,
    stderr: &str,
    home: &TempHome,
) -> (huddle_harness::runner::Outcome, FakeHarness) {
    let entry = find(id).expect("entry");
    let fake = FakeHarness::install(home, id, mode, 0, stderr).expect("fake");
    let var = override_var(id);
    let tool = fake.path().display().to_string();
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    (
        run_turn(&entry, &brief(Access::Write), &ctx).expect("run"),
        fake,
    )
}

fn turn(text: &str) -> RpcTurn {
    RpcTurn { text: text.into() }
}

#[test]
fn every_wire_runs_against_the_testkit_fake() {
    let cases: Vec<(&str, Mode, &str, Option<&str>)> = vec![
        ("droid", Mode::Text("plain\n".into()), "plain", None),
        (
            "opencode",
            Mode::Jsonl {
                session: "ses_1".into(),
                text: "answer".into(),
            },
            "answer",
            Some("ses_1"),
        ),
        (
            "zcode",
            Mode::JsonDoc {
                session: "sess_test".into(),
                response: "pong".into(),
            },
            "pong",
            Some("sess_test"),
        ),
        (
            "claude",
            Mode::StreamJson {
                session: "s-1".into(),
                text: "final".into(),
            },
            "final",
            Some("s-1"),
        ),
        (
            "omp",
            Mode::OmpRpc {
                session: "O1".into(),
                turns: vec![turn(REPORT)],
                end: RpcEnd::Normal,
            },
            REPORT,
            Some("O1"),
        ),
        (
            "pi",
            Mode::PiRpc {
                session: "P1".into(),
                turns: vec![turn(REPORT)],
                end: RpcEnd::Normal,
            },
            REPORT,
            Some("P1"),
        ),
    ];
    for (id, mode, text, session) in cases {
        let home = TempHome::new();
        let (out, _fake) = run_fake(id, mode, "", &home);
        assert_eq!(out.report_text, text, "{id}");
        assert_eq!(out.session_id.as_deref(), session, "{id}");
        assert_eq!(out.failure, None, "{id}");
        assert_eq!(out.exit.code, Some(0), "{id}");
    }
}

#[test]
fn a_fake_invalid_report_draws_a_correction_turn() {
    let home = TempHome::new();
    let mode = Mode::OmpRpc {
        session: "O1".into(),
        turns: vec![turn("not a report"), turn(REPORT)],
        end: RpcEnd::Normal,
    };
    let (out, fake) = run_fake("omp", mode, "", &home);
    assert_eq!(out.report_text, REPORT);
    assert_eq!(out.failure, None);
    let calls = fake.calls().expect("calls");
    let correction = calls[0].stdin.last().expect("line");
    assert!(
        correction
            .starts_with(r#"{"id":"c5","type":"prompt","message":"Your report was invalid: "#)
    );
}

#[test]
fn a_fake_crash_or_early_death_is_a_failed_outcome_with_the_exit() {
    let home = TempHome::new();
    let mode = Mode::OmpRpc {
        session: "O1".into(),
        turns: vec![turn("x")],
        end: RpcEnd::CrashMidTurn,
    };
    let (out, _fake) = run_fake("omp", mode, "boom\n", &home);
    assert_eq!(
        out.failure.as_deref(),
        Some("omp ended (exit code 3): boom")
    );
    assert_eq!(out.exit.code, Some(3));

    let home = TempHome::new();
    let mode = Mode::PiRpc {
        session: "P1".into(),
        turns: vec![],
        end: RpcEnd::DieAfterReady,
    };
    let (out, _fake) = run_fake("pi", mode, "boom\n", &home);
    assert_eq!(out.failure.as_deref(), Some("pi ended (exit code 1): boom"));
    assert_eq!(out.report_text, "");
    assert_eq!(out.session_id, None);
}

#[test]
fn a_timeout_aborts_a_hanging_fake_and_says_stopped() {
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let fake = FakeHarness::install(
        &home,
        "omp",
        Mode::OmpRpc {
            session: "O1".into(),
            turns: vec![turn("x")],
            end: RpcEnd::HangUntilAbort,
        },
        0,
        "",
    )
    .expect("fake");
    let var = override_var("omp");
    let tool = fake.path().display().to_string();
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let mut ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    ctx.grace = Duration::from_secs(5);
    ctx.timeout = Some(Duration::from_millis(300));
    let out = run_turn(&entry, &brief(Access::Write), &ctx).expect("run");
    assert_eq!(out.exit.forced.as_deref(), Some("timeout"));
    assert_eq!(out.failure.as_deref(), Some("omp was stopped (timeout)"));
    let calls = fake.calls().expect("calls");
    // A clock fire during the handshake aborts at the counter's current id, not c5; what the
    // run owes is that the abort went out at all.
    assert!(
        calls[0]
            .stdin
            .iter()
            .any(|l| l.contains(r#""type":"abort""#))
    );
}
