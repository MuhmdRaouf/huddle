//! One harness turn over the text wire, against a real child: a shell script standing in for the
//! tool, and the testkit's fake harness.

use huddle_config::env::Env;
use huddle_harness::resolve::override_var;
use huddle_harness::runner::{Brief, RunCtx, RunError, new_session_key, run_turn};
use huddle_harness::table::{
    Access, AccessSpec, Binary, Caps, Check, Entry, ExitInfo, Launcher, Prompt, ReportKind, Run,
    Session, SessionOwner, SessionRef, Transport, Wire,
};
use huddle_testkit::temp_home::TempHome;
use std::collections::BTreeMap;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::sync::Mutex;
use std::time::Duration;

fn entry_for(transport: Transport, flag: Option<&str>) -> Entry {
    let mut access = BTreeMap::new();
    access.insert(
        Access::Write,
        AccessSpec {
            args: vec!["--allow".into()],
            env: vec![("MY_ENV".into(), "yes".into())],
        },
    );
    Entry {
        id: "mytool".into(),
        label: "My Tool".into(),
        binary: Binary {
            names: vec!["mytool".into()],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: Check {
            version: vec!["--version".into()],
            install_hint: "i".into(),
            login_hint: "l".into(),
            verified: false,
        },
        run: Run {
            args: vec!["run".into()],
            cwd_flag: None,
            prompt: Prompt {
                transport,
                flag: flag.map(String::from),
                argv_text: None,
            },
            effort: None,
            access,
            after_access: vec![],
            session: Session {
                owner: SessionOwner::None,
                new: vec![],
                resume: vec![],
                missing: vec![],
            },
            line_cap: 1_048_576,
            reap_group: true,
        },
        wire: Wire::Text,
        caps: Caps {
            report: ReportKind::FreeText,
            notes: vec![],
        },
    }
}

fn brief() -> Brief {
    Brief {
        prompt: "do it".into(),
        access: Access::Write,
        effort: None,
        session: SessionRef::New { key: "k".into() },
    }
}

fn install_tool(root: &Path, body: &str) -> String {
    let path = root.join("bin/mytool");
    fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    fs::write(&path, format!("#!/bin/sh\n{body}\n")).expect("write");
    fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).expect("chmod");
    path.display().to_string()
}

fn run(
    body: &str,
    entry: &Entry,
    home: &TempHome,
    extra_env: &[(&str, &str)],
) -> Result<huddle_harness::runner::Outcome, RunError> {
    let tool = install_tool(home.root(), body);
    let var = override_var("mytool");
    let mut pairs: Vec<(&str, &str)> =
        vec![(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")];
    pairs.extend_from_slice(extra_env);
    let env = Env::with(&pairs);
    let ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    run_turn(entry, &brief(), &ctx)
}

#[test]
fn stdout_becomes_the_report_and_the_argv_is_built_from_table_data() {
    let home = TempHome::new();
    let e = entry_for(Transport::Argv, Some("-p"));
    let body = r#"printf '%s\n' "$@" > "$0.argv"; echo "line one"; echo "line two"; exit 0"#;
    let out = run(body, &e, &home, &[]).expect("run");
    assert_eq!(out.report_text, "line one\nline two");
    assert_eq!(out.failure, None);
    assert_eq!(out.exit, ExitInfo::exited(0));
    let argv = fs::read_to_string(home.root().join("bin/mytool.argv")).expect("argv");
    let expected = format!(
        "run\n--allow\n-p\n{}\n",
        huddle_harness::argv::full_prompt("do it")
    );
    assert_eq!(argv, expected);
    assert!(home.root().join("logs/stdout.log").exists());
}

#[test]
fn a_stdin_prompt_reaches_the_tool_and_the_env_is_the_users_minus_session_vars() {
    let home = TempHome::new();
    let e = entry_for(Transport::Stdin, None);
    let body = r#"cat > "$0.stdin"; printf '%s|%s|%s' "$MY_ENV" "${ZAI_API_KEY:-absent}" "${HOME:-nohome}""#;
    let out = run(
        body,
        &e,
        &home,
        &[("HOME", "/users/me"), ("ZAI_API_KEY", "secret")],
    )
    .expect("run");
    assert_eq!(out.report_text, "yes|absent|/users/me");
    assert_eq!(
        fs::read_to_string(home.root().join("bin/mytool.stdin")).expect("stdin"),
        huddle_harness::argv::full_prompt("do it")
    );
}

#[test]
fn a_failure_exit_is_a_failure_on_the_outcome_not_an_error() {
    let home = TempHome::new();
    let e = entry_for(Transport::Argv, None);
    let body = r#"echo nope 1>&2; exit 9"#;
    let out = run(body, &e, &home, &[]).expect("run");
    assert_eq!(out.exit.code, Some(9));
    assert_eq!(out.failure.as_deref(), Some("the harness exited 9: nope"));
}

#[test]
fn a_missing_binary_is_a_resolve_error_and_an_unsupported_access_an_argv_error() {
    let home = TempHome::new();
    let e = entry_for(Transport::Argv, None);
    let env = Env::with(&[("PATH", "/nonexistent")]);
    let ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    assert!(matches!(
        run_turn(&e, &brief(), &ctx),
        Err(RunError::Resolve(_))
    ));

    let tool = install_tool(home.root(), "exit 0");
    let var = override_var("mytool");
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    let mut bad = brief();
    bad.access = Access::Readonly;
    assert!(matches!(run_turn(&e, &bad, &ctx), Err(RunError::Argv(_))));
}

#[test]
fn on_spawn_gets_the_pid_and_a_timeout_stops_the_tool() {
    let home = TempHome::new();
    let e = entry_for(Transport::Argv, None);
    let tool = install_tool(home.root(), "sleep 30");
    let var = override_var("mytool");
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let seen = Mutex::new(None);
    let on_spawn =
        |pid: u32, _stop: huddle_procs::stream::StopHandle| *seen.lock().expect("lock") = Some(pid);
    let mut ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    ctx.grace = Duration::from_millis(100);
    ctx.timeout = Some(Duration::from_millis(100));
    ctx.on_spawn = Some(&on_spawn);
    let out = run_turn(&e, &brief(), &ctx).expect("run");
    assert!(seen.lock().expect("lock").is_some());
    assert_eq!(out.exit.forced.as_deref(), Some("timeout"));
    assert_eq!(
        out.failure.as_deref(),
        Some("the harness was stopped (timeout)")
    );
}

#[test]
fn session_keys_are_uuid_v4() {
    let key = new_session_key().expect("key");
    assert_eq!(key.len(), 36);
    assert_eq!(key.as_bytes()[14], b'4');
    assert!(matches!(key.as_bytes()[19], b'8' | b'9' | b'a' | b'b'));
    assert_ne!(new_session_key().expect("key"), key);
}

#[test]
fn a_text_harness_runs_against_the_testkit_fake() {
    use huddle_testkit::fake_harness::{FakeHarness, Mode};
    let home = TempHome::new();
    let fake = FakeHarness::install(&home, "mytool", Mode::Text("plain answer\n".into()), 0, "")
        .expect("fake");
    let e = entry_for(Transport::Stdin, None);
    let var = override_var("mytool");
    let tool = fake.path().display().to_string();
    let env = Env::with(&[(var.as_str(), tool.as_str()), ("PATH", "/usr/bin:/bin")]);
    let ctx = RunCtx::new(&env, home.root().to_path_buf(), home.root().join("logs"));
    let out = run_turn(&e, &brief(), &ctx).expect("run");
    assert_eq!(out.report_text, "plain answer");
    assert_eq!(out.failure, None);
    let calls = fake.calls().expect("calls");
    assert_eq!(
        calls[0].argv[1..],
        ["run".to_string(), "--allow".to_string()]
    );
    assert!(calls[0].stdin.join("\n").starts_with("do it"));
    assert!(
        calls[0]
            .env
            .iter()
            .any(|(k, v)| k == "MY_ENV" && v == "yes")
    );
}
