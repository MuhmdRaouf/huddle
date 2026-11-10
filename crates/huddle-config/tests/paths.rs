use huddle_config::atomic::write_atomic;
use huddle_config::env::Env;
use huddle_config::paths::{DEFAULT_ROUTER_PORT, Paths, PathsError, router_port, router_url};
use huddle_testkit::temp_home::TempHome;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;

#[test]
fn every_path_comes_from_home() {
    let p = Paths::from_env(&Env::with(&[("HOME", "/home/u")])).expect("paths");
    assert_eq!(p.home, PathBuf::from("/home/u"));
    assert_eq!(p.agents, PathBuf::from("/home/u/.agents"));
    assert_eq!(p.router_dir, PathBuf::from("/home/u/.agents/huddle-router"));
    assert_eq!(p.ca_dir, PathBuf::from("/home/u/.agents/ca"));
    assert_eq!(p.huddle_dir, PathBuf::from("/home/u/.agents/huddle"));
    assert_eq!(p.jobs_root, PathBuf::from("/home/u/.agents/huddle/jobs"));
    assert_eq!(p.bin_dir, PathBuf::from("/home/u/.agents/huddle/bin"));
    assert_eq!(p.claude_dir, PathBuf::from("/home/u/.claude"));
}

#[test]
fn overrides_move_the_router_home_and_the_ca_stays_beside_it() {
    let env = Env::with(&[
        ("HOME", "/home/u"),
        ("HUDDLE_ROUTER_HOME", "/x/r"),
        ("HUDDLE_HOME", "/x/h"),
        ("CLAUDE_CONFIG_DIR", "/x/c"),
    ]);
    let p = Paths::from_env(&env).expect("paths");
    assert_eq!(p.router_dir, PathBuf::from("/x/r"));
    assert_eq!(p.ca_dir, PathBuf::from("/x/ca"));
    assert_eq!(p.huddle_dir, PathBuf::from("/x/h"));
    assert_eq!(p.jobs_root, PathBuf::from("/x/h/jobs"));
    assert_eq!(p.claude_dir, PathBuf::from("/x/c"));
}

#[test]
fn an_empty_override_counts_as_unset() {
    let env = Env::with(&[
        ("HOME", "/home/u"),
        ("HUDDLE_ROUTER_HOME", ""),
        ("CLAUDE_CONFIG_DIR", ""),
    ]);
    let p = Paths::from_env(&env).expect("paths");
    assert_eq!(p.router_dir, PathBuf::from("/home/u/.agents/huddle-router"));
    assert_eq!(p.claude_dir, PathBuf::from("/home/u/.claude"));
}

#[test]
fn a_process_without_home_gets_a_typed_error() {
    assert_eq!(
        Paths::from_env(&Env::with(&[])).unwrap_err(),
        PathsError::NoHome
    );
    assert_eq!(
        Paths::from_env(&Env::with(&[("HOME", "")])).unwrap_err(),
        PathsError::NoHome
    );
}

#[test]
fn the_router_port_defaults_and_moves() {
    assert_eq!(
        router_port(&Env::with(&[])).expect("port"),
        DEFAULT_ROUTER_PORT
    );
    assert_eq!(
        router_port(&Env::with(&[("HUDDLE_ROUTER_PORT", "")])).expect("port"),
        DEFAULT_ROUTER_PORT
    );
    assert_eq!(
        router_port(&Env::with(&[("HUDDLE_ROUTER_PORT", "4836")])).expect("port"),
        4836
    );
    assert_eq!(
        router_url(&Env::with(&[("HUDDLE_ROUTER_PORT", "4836")])).expect("url"),
        "http://127.0.0.1:4836"
    );
    assert_eq!(
        router_url(&Env::with(&[])).expect("url"),
        "http://127.0.0.1:4835"
    );
}

#[test]
fn one_port_rule_refuses_everything_but_plain_digits() {
    for raw in [
        "0", "65536", "abc", "1e3", " 4836", "4836 ", "+4836", "-1", "4835.0",
    ] {
        let err = router_port(&Env::with(&[("HUDDLE_ROUTER_PORT", raw)])).unwrap_err();
        assert_eq!(
            err,
            PathsError::BadRouterPort(raw.to_string()),
            "raw {raw:?}"
        );
        assert_eq!(
            err.to_string(),
            format!("huddle-router: $HUDDLE_ROUTER_PORT ({raw:?}) is not a port")
        );
        assert_eq!(
            router_url(&Env::with(&[("HUDDLE_ROUTER_PORT", raw)])).unwrap_err(),
            PathsError::BadRouterPort(raw.to_string()),
            "setup and the daemon share the rule: {raw:?}"
        );
    }
}

#[test]
fn an_atomic_write_replaces_the_file_with_the_mode_and_leaves_no_temp() {
    let home = TempHome::new();
    let dir = home.root().join("state");
    let path = dir.join("thing.json");
    write_atomic(&path, b"first", 0o600).expect("write");
    assert_eq!(std::fs::read(&path).expect("read"), b"first");
    assert_eq!(
        std::fs::metadata(&path).expect("meta").permissions().mode() & 0o777,
        0o600
    );
    assert_eq!(
        std::fs::metadata(&dir).expect("meta").permissions().mode() & 0o777,
        0o700
    );
    write_atomic(&path, b"second", 0o644).expect("write");
    assert_eq!(std::fs::read(&path).expect("read"), b"second");
    assert_eq!(
        std::fs::metadata(&path).expect("meta").permissions().mode() & 0o777,
        0o644
    );
    let names: Vec<_> = std::fs::read_dir(&dir)
        .expect("dir")
        .map(|e| e.expect("e").file_name())
        .collect();
    assert_eq!(names.len(), 1, "no temp file remains: {names:?}");
}

#[test]
fn leftover_temp_names_of_a_dead_process_never_block_a_write() {
    let home = TempHome::new();
    let dir = home.root().join("state");
    std::fs::create_dir_all(&dir).expect("dir");
    for i in 0..100_u32 {
        let leftover = dir.join(format!(".thing.json.{i}.tmp"));
        std::fs::write(leftover, b"old").expect("leftover");
    }
    let path = dir.join("thing.json");
    write_atomic(&path, b"new", 0o600).expect("write");
    assert_eq!(std::fs::read(&path).expect("read"), b"new");
}

#[test]
fn a_write_that_cannot_land_removes_its_temp_file() {
    let home = TempHome::new();
    let dir = home.root().join("state");
    std::fs::create_dir_all(dir.join("thing.json")).expect("mkdir target as a directory");
    let err = write_atomic(&dir.join("thing.json"), b"x", 0o600)
        .expect_err("a directory cannot be replaced");
    assert!(!err.to_string().is_empty());
    let names: Vec<_> = std::fs::read_dir(&dir)
        .expect("dir")
        .map(|e| e.expect("e").file_name())
        .collect();
    assert_eq!(names.len(), 1, "only the directory remains: {names:?}");
}
