use huddle_harness::entries::{builtin, find};
use huddle_harness::table::{Access, ReportKind, SessionOwner, Transport, Wire};
use std::time::Duration;

const SPEC_IDS: [&str; 22] = [
    "amp",
    "agy",
    "claude",
    "codex",
    "cursor-agent",
    "droid",
    "fx",
    "gemini",
    "copilot",
    "grok",
    "hermes",
    "muse",
    "devin",
    "kimi",
    "kiro",
    "mastracode",
    "vibe",
    "omp",
    "opencode",
    "pi",
    "polygraph",
    "zcode",
];

#[test]
fn the_table_holds_exactly_the_spec_targets_in_order() {
    let ids: Vec<String> = builtin().into_iter().map(|e| e.id).collect();
    assert_eq!(ids, SPEC_IDS.map(String::from).to_vec());
}

#[test]
fn only_the_checked_clis_are_verified_and_every_other_is_text_wire() {
    let verified = ["claude", "omp", "opencode", "pi", "zcode"];
    for entry in builtin() {
        assert_eq!(
            entry.check.verified,
            verified.contains(&entry.id.as_str()),
            "{}",
            entry.id
        );
        if !entry.check.verified {
            assert_eq!(entry.wire, Wire::Text, "{}", entry.id);
            assert_eq!(entry.run.session.owner, SessionOwner::None, "{}", entry.id);
            assert_eq!(entry.caps.report, ReportKind::FreeText, "{}", entry.id);
            assert!(
                entry.run.access.contains_key(&Access::Write),
                "{}",
                entry.id
            );
            assert!(
                !entry.run.access.contains_key(&Access::Exec),
                "{}",
                entry.id
            );
        }
        assert_eq!(
            entry.check.version,
            vec!["--version".to_string()],
            "{}",
            entry.id
        );
        assert!(
            !entry.check.install_hint.is_empty() && !entry.check.login_hint.is_empty(),
            "{}",
            entry.id
        );
    }
}

#[test]
fn every_id_is_unique_and_find_finds_it() {
    let all = builtin();
    for entry in &all {
        assert_eq!(all.iter().filter(|e| e.id == entry.id).count(), 1);
        assert_eq!(find(&entry.id), Some(entry.clone()));
    }
    assert_eq!(find("nope"), None);
}

#[test]
fn omp_runs_over_rpc_with_tool_allowlists_per_access() {
    let omp = find("omp").expect("omp");
    assert_eq!(
        omp.wire,
        Wire::OmpRpc {
            handshake: Duration::from_millis(60_000)
        }
    );
    assert_eq!(omp.run.prompt.transport, Transport::Rpc);
    assert_eq!(
        omp.run.access[&Access::Readonly].args,
        vec!["--tools", "read,grep,glob"]
    );
    assert_eq!(
        omp.run.access[&Access::Write].args,
        vec!["--tools", "read,grep,glob,edit,write,bash,todo"]
    );
    assert_eq!(
        omp.run.after_access,
        vec!["--approval-mode", "yolo", "--no-title"]
    );
    assert_eq!(omp.run.line_cap, 1_048_576);
    assert!(!omp.run.reap_group);
}

#[test]
fn pi_write_has_no_tool_flags_and_pi_owns_nothing_of_the_session() {
    let pi = find("pi").expect("pi");
    assert_eq!(pi.run.session.owner, SessionOwner::Caller);
    assert_eq!(pi.run.access[&Access::Write].args, Vec::<String>::new());
    assert_eq!(
        pi.run.access[&Access::Exec].args,
        vec!["--tools", "read,grep,find,ls,bash", "--no-mcp"]
    );
    assert_eq!(pi.run.line_cap, 67_108_864);
}

#[test]
fn opencode_access_is_the_permission_config_env() {
    let oc = find("opencode").expect("opencode");
    assert_eq!(
        oc.run.access[&Access::Readonly].env,
        vec![(
            "OPENCODE_CONFIG_CONTENT".to_string(),
            r#"{"permission":{"edit":"deny","bash":"deny","question":"deny"}}"#.to_string()
        )]
    );
    assert_eq!(oc.run.after_access, vec!["--auto"]);
    assert_eq!(oc.run.prompt.transport, Transport::Argv);
}

#[test]
fn claude_is_the_users_own_cli_on_stream_json() {
    let claude = find("claude").expect("claude");
    assert_eq!(claude.wire, Wire::StreamJson);
    assert_eq!(
        claude.run.args,
        vec!["-p", "--output-format", "stream-json", "--verbose"]
    );
    assert_eq!(
        claude.run.access[&Access::Readonly].args,
        vec!["--permission-mode", "plan"]
    );
    assert_eq!(claude.run.session.new, vec!["--session-id", "{id}"]);
    assert_eq!(claude.run.line_cap, u64::MAX);
}

#[test]
fn zcode_keeps_its_bundle_paths_and_file_prompt() {
    let z = find("zcode").expect("zcode");
    assert_eq!(z.binary.env_var.as_deref(), Some("ZCODE_CLI"));
    assert_eq!(z.binary.fixed.len(), 2);
    assert_eq!(z.run.cwd_flag.as_deref(), Some("--cwd"));
    assert_eq!(z.run.prompt.transport, Transport::File);
    assert_eq!(z.run.prompt.flag.as_deref(), Some("--attach"));
    assert_eq!(
        z.run.prompt.argv_text,
        Some((
            "--prompt".to_string(),
            "Read the attached task brief and follow it. End with the required JSON report."
                .to_string()
        ))
    );
    assert_eq!(
        z.run.access[&Access::Exec].args,
        vec!["--disallowed-tools", "Write,Edit,NotebookEdit"]
    );
}

#[test]
fn a_few_paradev_rows_read_as_the_table_says() {
    let amp = find("amp").expect("amp");
    assert_eq!(amp.run.prompt.transport, Transport::Stdin);
    assert_eq!(amp.run.args, vec!["-x"]);

    let codex = find("codex").expect("codex");
    assert_eq!(codex.run.args, vec!["exec", "--skip-git-repo-check"]);
    assert_eq!(
        codex.run.access[&Access::Write].args,
        vec![
            "--dangerously-bypass-approvals-and-sandbox",
            "--dangerously-bypass-hook-trust"
        ]
    );
    assert_eq!(
        codex.run.effort.as_ref().map(|f| f.args.clone()),
        Some(vec![
            "-c".to_string(),
            "model_reasoning_effort={value}".to_string()
        ])
    );

    let kiro = find("kiro").expect("kiro");
    assert_eq!(kiro.binary.names, vec!["kiro-cli"]);

    let grok = find("grok").expect("grok");
    assert_eq!(
        grok.run.access[&Access::Readonly].args,
        vec!["--permission-mode", "plan"]
    );
}
