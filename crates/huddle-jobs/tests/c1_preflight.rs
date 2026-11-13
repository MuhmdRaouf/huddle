mod support;

use huddle_jobs::preflight::{PreflightError, check_run};
use support::rig;

#[test]
fn c1_an_unknown_harness_is_named() {
    let r = rig();
    let error = check_run(&r.paths, &r.env, "nope", "write").expect_err("unknown");
    assert!(matches!(error, PreflightError::Unknown(_)), "{error:?}");
    assert_eq!(
        error.to_string(),
        "no harness named \"nope\": /huddle:setup lists them"
    );
}

#[test]
fn c1_an_unsupported_access_lists_what_the_harness_supports() {
    let r = rig();
    let error = check_run(&r.paths, &r.env, "opencode", "bogus").expect_err("access");
    let PreflightError::Access { harness, .. } = &error else {
        panic!("{error:?}");
    };
    assert_eq!(harness, "opencode");
    assert!(
        error
            .to_string()
            .starts_with("opencode cannot run with bogus access: it supports "),
        "{error}"
    );
}

#[test]
fn c1_a_missing_binary_is_not_ready() {
    let r = rig();
    let empty = r.home.root().join("no-binaries");
    std::fs::create_dir_all(&empty).unwrap();
    let mut pairs = r.home.env();
    pairs.retain(|(key, _)| key != "PATH");
    pairs.push(("PATH".into(), empty.display().to_string()));
    let refs: Vec<(&str, &str)> = pairs
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let env = huddle_config::env::Env::with(&refs);
    let error = check_run(&r.paths, &env, "opencode", "write").expect_err("binary");
    let PreflightError::NotReady { harness, .. } = &error else {
        panic!("{error:?}");
    };
    assert_eq!(harness, "opencode");
    assert!(
        error
            .to_string()
            .ends_with(": /huddle:setup opencode shows what to fix"),
        "{error}"
    );
}
