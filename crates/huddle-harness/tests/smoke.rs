use huddle_config::env::Env;
use huddle_harness::check::{SmokeRunner, TurnSmoke};
use huddle_harness::entries::find;
use huddle_harness::resolve::override_var;
use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::Path;

fn tool(root: &Path, name: &str, body: &str) -> String {
    let path = root.join("bin").join(name);
    fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    write_executable(&path, &format!("#!/bin/sh\n{body}\n")).expect("write");
    path.display().to_string()
}

#[test]
fn a_text_harness_that_answers_is_ok_and_says_which_harness() {
    let home = TempHome::new();
    let entry = find("droid").expect("droid");
    let path = tool(home.root(), "droid", "cat >/dev/null; echo ok");
    let var = override_var(&entry.id);
    let env = Env::with(&[(var.as_str(), path.as_str()), ("PATH", "/usr/bin:/bin")]);
    let runner = TurnSmoke {
        env: &env,
        dir: home.root().join("smoke"),
    };
    assert_eq!(runner.smoke(&entry), Ok("ok: droid answered".to_string()));
}

#[test]
fn a_tool_that_exits_one_fails_with_the_setup_hint() {
    let home = TempHome::new();
    let entry = find("droid").expect("droid");
    let path = tool(
        home.root(),
        "droid",
        "cat >/dev/null; echo 'No API key found' 1>&2; exit 1",
    );
    let var = override_var(&entry.id);
    let env = Env::with(&[(var.as_str(), path.as_str()), ("PATH", "/usr/bin:/bin")]);
    let runner = TurnSmoke {
        env: &env,
        dir: home.root().join("smoke"),
    };
    let err = runner.smoke(&entry).expect_err("failed");
    assert!(err.contains("No API key found"));
    assert!(err.ends_with("; run droid once and log in / configure it; huddle uses it as it is"));
}

#[test]
fn a_missing_binary_reads_as_a_setup_failure() {
    let home = TempHome::new();
    let entry = find("droid").expect("droid");
    let env = Env::with(&[("PATH", home.root().to_str().expect("utf8"))]);
    let runner = TurnSmoke {
        env: &env,
        dir: home.root().join("smoke"),
    };
    let err = runner.smoke(&entry).expect_err("failed");
    assert!(err.starts_with("droid is not on PATH"));
}

#[test]
fn an_entry_without_readonly_runs_the_smoke_with_write_access() {
    let home = TempHome::new();
    let entry = find("droid").expect("droid");
    assert!(
        !entry
            .run
            .accepted_access()
            .contains(&huddle_harness::table::Access::Readonly)
    );
    let path = tool(home.root(), "droid", r#"cat >/dev/null; echo "$PWD""#);
    let var = override_var(&entry.id);
    let env = Env::with(&[(var.as_str(), path.as_str()), ("PATH", "/usr/bin:/bin")]);
    let runner = TurnSmoke {
        env: &env,
        dir: home.root().join("smoke"),
    };
    runner.smoke(&entry).expect("ok");
    assert!(home.root().join("smoke/droid/logs/stdout.log").exists());
}
