use huddle_harness::argv::{ArgvError, REPORT_SCHEMA_JSON, build, full_prompt};
use huddle_harness::entries::find;
use huddle_harness::table::{Access, Brief, SessionRef};
use std::path::Path;

fn brief(access: Access, effort: Option<&str>, session: SessionRef) -> Brief {
    Brief {
        prompt: "do it".into(),
        access,
        effort: effort.map(String::from),
        session,
    }
}

fn new_session() -> SessionRef {
    SessionRef::New {
        key: "11111111-1111-4111-8111-111111111111".into(),
    }
}

const CWD: &str = "/work/wt";
const LOG: &str = "/work/log";

fn argv(id: &str, access: Access, effort: Option<&str>, session: SessionRef) -> Vec<String> {
    build(
        &find(id).expect("entry"),
        &brief(access, effort, session),
        Path::new(CWD),
        Path::new(LOG),
    )
    .expect("launch")
    .args
}

#[test]
fn omp_writes_the_order_rust_design_needs() {
    let got = argv(
        "omp",
        Access::Readonly,
        Some("high"),
        SessionRef::Resume { id: "S1".into() },
    );
    assert_eq!(
        got,
        vec![
            "--mode",
            "rpc",
            "--no-ui",
            "--thinking",
            "high",
            "--tools",
            "read,grep,glob",
            "--approval-mode",
            "yolo",
            "--no-title",
            "--resume",
            "S1"
        ]
    );
    let launch = build(
        &find("omp").expect("omp"),
        &brief(Access::Write, None, new_session()),
        Path::new(CWD),
        Path::new(LOG),
    )
    .expect("launch");
    assert!(
        launch
            .rpc_prompt
            .as_deref()
            .expect("prompt")
            .starts_with("do it\n\n")
    );
    assert_eq!(launch.stdin, None);
}

#[test]
fn pi_write_has_only_the_session_flag_and_uses_the_callers_key() {
    let got = argv("pi", Access::Write, None, new_session());
    assert_eq!(
        got,
        vec![
            "--mode",
            "rpc",
            "--session-id",
            "11111111-1111-4111-8111-111111111111"
        ]
    );
    let got = argv(
        "pi",
        Access::Exec,
        Some("low"),
        SessionRef::Resume { id: "K".into() },
    );
    assert_eq!(
        got,
        vec![
            "--mode",
            "rpc",
            "--thinking",
            "low",
            "--tools",
            "read,grep,find,ls,bash",
            "--no-mcp",
            "--session-id",
            "K"
        ]
    );
}

#[test]
fn opencode_puts_variant_before_auto_and_the_prompt_last() {
    let launch = build(
        &find("opencode").expect("opencode"),
        &brief(
            Access::Exec,
            Some("max"),
            SessionRef::Resume { id: "ses_1".into() },
        ),
        Path::new(CWD),
        Path::new(LOG),
    )
    .expect("launch");
    let text = full_prompt("do it");
    assert_eq!(
        launch.args,
        vec![
            "run",
            "--format",
            "json",
            "--variant",
            "max",
            "--auto",
            "--session",
            "ses_1",
            text.as_str()
        ]
    );
    assert_eq!(
        launch.env,
        vec![(
            "OPENCODE_CONFIG_CONTENT".to_string(),
            r#"{"permission":{"edit":"deny","bash":"allow","question":"deny"}}"#.to_string()
        )]
    );
}

#[test]
fn zcode_attaches_the_prompt_file_and_passes_the_cwd() {
    let launch = build(
        &find("zcode").expect("zcode"),
        &brief(
            Access::Readonly,
            None,
            SessionRef::Resume {
                id: "sess_1".into(),
            },
        ),
        Path::new(CWD),
        Path::new(LOG),
    )
    .expect("launch");
    assert_eq!(
        launch.args,
        vec![
            "--cwd",
            "/work/wt",
            "--json",
            "--no-color",
            "--mode",
            "yolo",
            "--disallowed-tools",
            "Bash,Write,Edit,NotebookEdit",
            "--resume",
            "sess_1",
            "--attach",
            "/work/log/prompt.md",
            "--prompt",
            "Read the attached task brief and follow it. End with the required JSON report."
        ]
    );
    let (path, text) = launch.prompt_file.expect("file");
    assert_eq!(path, Path::new("/work/log/prompt.md"));
    assert_eq!(text, full_prompt("do it"));
}

#[test]
fn claude_takes_the_prompt_on_stdin_and_the_key_as_session_id() {
    let launch = build(
        &find("claude").expect("claude"),
        &brief(Access::Readonly, Some("max"), new_session()),
        Path::new(CWD),
        Path::new(LOG),
    )
    .expect("launch");
    assert_eq!(
        launch.args,
        vec![
            "-p",
            "--output-format",
            "stream-json",
            "--verbose",
            "--effort",
            "max",
            "--permission-mode",
            "plan",
            "--session-id",
            "11111111-1111-4111-8111-111111111111"
        ]
    );
    assert_eq!(launch.stdin, Some(full_prompt("do it")));
}

#[test]
fn a_text_harness_takes_the_prompt_after_its_flag() {
    let got = argv("hermes", Access::Write, None, new_session());
    assert_eq!(
        got,
        vec!["chat", "--yolo", "-q", full_prompt("do it").as_str()]
    );
    let codex = argv("codex", Access::Readonly, Some("high"), new_session());
    assert_eq!(
        codex,
        vec![
            "exec",
            "--skip-git-repo-check",
            "-c",
            "model_reasoning_effort=high",
            full_prompt("do it").as_str()
        ]
    );
}

#[test]
fn refusals_list_what_is_accepted() {
    let codex = find("codex").expect("codex");
    let exec = build(
        &codex,
        &brief(Access::Exec, None, new_session()),
        Path::new(CWD),
        Path::new(LOG),
    );
    assert_eq!(
        exec,
        Err(ArgvError::AccessUnsupported {
            id: "codex".into(),
            access: "exec".into(),
            supported: vec!["readonly".into(), "write".into()],
        })
    );
    assert_eq!(
        exec.expect_err("refused").to_string(),
        "codex cannot run with exec access: it supports readonly, write"
    );

    let omp = find("omp").expect("omp");
    let bad = build(
        &omp,
        &brief(Access::Write, Some("turbo"), new_session()),
        Path::new(CWD),
        Path::new(LOG),
    );
    assert_eq!(
        bad.expect_err("refused").to_string(),
        "omp does not take effort turbo: it accepts low, high, max"
    );

    let droid = find("droid").expect("droid");
    let none = build(
        &droid,
        &brief(Access::Write, Some("high"), new_session()),
        Path::new(CWD),
        Path::new(LOG),
    );
    assert_eq!(
        none.expect_err("refused").to_string(),
        "droid takes no effort setting (asked for high)"
    );

    let resume = build(
        &droid,
        &brief(Access::Write, None, SessionRef::Resume { id: "x".into() }),
        Path::new(CWD),
        Path::new(LOG),
    );
    assert_eq!(
        resume.expect_err("refused").to_string(),
        "droid cannot resume a session"
    );
}

#[test]
fn zcode_default_effort_adds_nothing() {
    let got = argv("zcode", Access::Write, Some("default"), new_session());
    assert!(!got.contains(&"--variant".to_string()));
    assert!(!got.iter().any(|a| a == "default"));
}

#[test]
fn the_report_contract_names_the_three_keys() {
    let text = full_prompt("task");
    assert!(text.starts_with("task\n\n## Final report\n\nWork unattended."));
    assert!(text.ends_with("\n```"));
    assert!(text.contains("\"open_items\""));
    let schema: serde_json::Value =
        serde_json::from_str(REPORT_SCHEMA_JSON).expect("schema parses");
    assert_eq!(
        schema["required"],
        serde_json::json!(["summary", "files", "open_items"])
    );
}
