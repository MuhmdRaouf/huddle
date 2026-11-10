use huddle_config::env::Env;
use huddle_harness::resolve::{ResolveError, override_var, resolve};
use huddle_harness::table::{
    Access, AccessSpec, Binary, Caps, Check, Entry, FixedPath, Launcher, Prompt, ReportKind, Run,
    Session, SessionOwner, Transport, Wire,
};
use huddle_testkit::temp_home::TempHome;
use std::collections::BTreeMap;
use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::path::Path;

fn entry(
    id: &str,
    names: &[&str],
    env_var: Option<&str>,
    fixed: Vec<FixedPath>,
    launcher: Launcher,
) -> Entry {
    let mut access = BTreeMap::new();
    access.insert(
        Access::Write,
        AccessSpec {
            args: vec![],
            env: vec![],
        },
    );
    Entry {
        id: id.into(),
        label: id.into(),
        binary: Binary {
            names: names.iter().map(|n| n.to_string()).collect(),
            env_var: env_var.map(String::from),
            fixed,
            launcher,
            launch_env: vec![("ELECTRON_RUN_AS_NODE".into(), "1".into())],
        },
        check: Check {
            version: vec!["--version".into()],
            install_hint: "i".into(),
            login_hint: "l".into(),
            verified: true,
        },
        run: Run {
            args: vec![],
            cwd_flag: None,
            prompt: Prompt {
                transport: Transport::Argv,
                flag: None,
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
            reap_group: false,
        },
        wire: Wire::Text,
        caps: Caps {
            report: ReportKind::FreeText,
            notes: vec![],
        },
    }
}

fn executable(path: &Path) {
    fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    fs::write(path, "#!/bin/sh\nexit 0\n").expect("write");
    fs::set_permissions(path, fs::Permissions::from_mode(0o755)).expect("chmod");
}

fn plain_file(path: &Path) {
    fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    fs::write(path, "console.log('z')\n").expect("write");
    fs::set_permissions(path, fs::Permissions::from_mode(0o644)).expect("chmod");
}

fn env_with(pairs: &[(&str, &str)]) -> Env {
    Env::with(pairs)
}

#[test]
fn the_override_variable_is_huddle_id_bin() {
    assert_eq!(override_var("omp"), "HUDDLE_OMP_BIN");
    assert_eq!(override_var("cursor-agent"), "HUDDLE_CURSOR_AGENT_BIN");
}

#[test]
fn override_beats_the_own_variable_beats_path() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let on_path = root.join("bin/omp");
    let own = root.join("own/omp");
    let over = root.join("over/omp");
    executable(&on_path);
    executable(&own);
    executable(&over);

    let e = entry("omp", &["omp"], Some("OMP_CLI"), vec![], Launcher::None);
    let path = root.join("bin");
    let env = env_with(&[("PATH", path.to_str().expect("utf8"))]);
    assert_eq!(resolve(&e, &env).expect("path").program, on_path);

    let env = env_with(&[
        ("PATH", path.to_str().expect("utf8")),
        ("OMP_CLI", own.to_str().expect("utf8")),
    ]);
    assert_eq!(resolve(&e, &env).expect("own").program, own);

    let env = env_with(&[
        ("PATH", path.to_str().expect("utf8")),
        ("OMP_CLI", own.to_str().expect("utf8")),
        ("HUDDLE_OMP_BIN", over.to_str().expect("utf8")),
    ]);
    assert_eq!(resolve(&e, &env).expect("override").program, over);
}

#[test]
fn a_missing_binary_names_the_failure_as_a_value() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let e = entry("omp", &["omp"], None, vec![], Launcher::None);
    let env = env_with(&[("PATH", root.to_str().expect("utf8"))]);
    let err = resolve(&e, &env).expect_err("missing");
    assert_eq!(err, ResolveError::NotOnPath { name: "omp".into() });
    assert_eq!(err.to_string(), "omp is not on PATH");

    let dir = root.join("not-a-binary");
    fs::create_dir_all(&dir).expect("dir");
    let env = env_with(&[("HUDDLE_OMP_BIN", dir.to_str().expect("utf8"))]);
    let err = resolve(&e, &env).expect_err("not executable");
    assert_eq!(
        err.to_string(),
        format!("{} is not an executable file", dir.display())
    );

    let plain = root.join("plain");
    plain_file(&plain);
    let env = env_with(&[
        ("PATH", root.to_str().expect("utf8")),
        ("HUDDLE_OMP_BIN", plain.to_str().expect("utf8")),
    ]);
    assert!(matches!(
        resolve(&e, &env),
        Err(ResolveError::NotExecutable { .. })
    ));
}

#[test]
fn a_fixed_path_expands_home_and_needs_no_exec_bit_for_a_script() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let script = root.join("Applications/ZCode.app/Contents/Resources/glm/zcode.cjs");
    plain_file(&script);
    let electron = root.join("Applications/ZCode.app/Contents/MacOS/ZCode");
    executable(&electron);

    let fixed = vec![FixedPath {
        os: std::env::consts::OS.into(),
        path: "~/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs".into(),
    }];
    let e = entry(
        "zcode",
        &["zcode"],
        Some("ZCODE_CLI"),
        fixed,
        Launcher::ElectronOrNode,
    );
    let env = env_with(&[("HOME", root.to_str().expect("utf8")), ("PATH", "")]);
    let resolved = resolve(&e, &env).expect("bundle");
    assert_eq!(resolved.program, electron);
    assert_eq!(resolved.prefix_args, vec![script.display().to_string()]);
    assert_eq!(resolved.path, script);
    assert!(
        resolved
            .env
            .contains(&("ELECTRON_RUN_AS_NODE".to_string(), "1".to_string()))
    );
}

#[test]
fn the_app_runner_gets_the_builtin_provider_config_only_when_unset() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let contents = root.join("ZCode.app/Contents");
    let script = contents.join("Resources/glm/zcode.cjs");
    plain_file(&script);
    executable(&contents.join("MacOS/ZCode"));
    let config = contents.join("Resources/config/provider/zcode-builtin.json");
    plain_file(&config);

    let e = entry(
        "zcode",
        &["zcode"],
        Some("ZCODE_CLI"),
        vec![],
        Launcher::ElectronOrNode,
    );
    let env = env_with(&[("ZCODE_CLI", script.to_str().expect("utf8"))]);
    let resolved = resolve(&e, &env).expect("script");
    assert!(resolved.env.contains(&(
        "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE".to_string(),
        config.display().to_string()
    )));

    let env = env_with(&[
        ("ZCODE_CLI", script.to_str().expect("utf8")),
        ("ZCODE_BUILTIN_PROVIDER_CONFIG_FILE", "/mine"),
    ]);
    let resolved = resolve(&e, &env).expect("script");
    assert!(
        !resolved
            .env
            .iter()
            .any(|(k, _)| k == "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE")
    );
}

#[test]
fn a_bare_script_falls_back_to_node_from_the_tools_path_or_fails() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let script = root.join("zcode.cjs");
    plain_file(&script);
    let e = entry(
        "zcode",
        &["zcode"],
        Some("ZCODE_CLI"),
        vec![],
        Launcher::ElectronOrNode,
    );

    let bin = root.join("bin");
    executable(&bin.join("node"));
    let env = env_with(&[
        ("ZCODE_CLI", script.to_str().expect("utf8")),
        ("PATH", bin.to_str().expect("utf8")),
    ]);
    let resolved = resolve(&e, &env).expect("node");
    assert_eq!(resolved.program, bin.join("node"));
    assert_eq!(resolved.prefix_args, vec![script.display().to_string()]);
    assert!(resolved.env.is_empty());

    let env = env_with(&[
        ("ZCODE_CLI", script.to_str().expect("utf8")),
        ("PATH", "/nonexistent"),
    ]);
    let err = resolve(&e, &env).expect_err("no runner");
    assert_eq!(
        err.to_string(),
        format!(
            "{} has no ZCode app executable beside it (Contents/MacOS/ZCode) and no node on PATH",
            script.display()
        )
    );
}

#[test]
fn a_symlink_to_the_app_gui_binary_maps_to_the_bundled_script() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let contents = root.join("ZCode.app/Contents");
    let gui = contents.join("MacOS/ZCode");
    executable(&gui);
    let script = contents.join("Resources/glm/zcode.cjs");
    plain_file(&script);
    let bin = root.join("bin");
    fs::create_dir_all(&bin).expect("bin");
    symlink(&gui, bin.join("zcode")).expect("symlink");

    let e = entry(
        "zcode",
        &["zcode"],
        Some("ZCODE_CLI"),
        vec![],
        Launcher::ElectronOrNode,
    );
    let env = env_with(&[("PATH", bin.to_str().expect("utf8"))]);
    let resolved = resolve(&e, &env).expect("gui");
    assert_eq!(
        fs::canonicalize(resolved.path).expect("real"),
        fs::canonicalize(script).expect("real")
    );
    assert_eq!(
        fs::canonicalize(resolved.program).expect("real"),
        fs::canonicalize(gui).expect("real")
    );
}
