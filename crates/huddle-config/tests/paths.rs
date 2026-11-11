use huddle_config::atomic::write_atomic;
use huddle_config::env::Env;
use huddle_config::paths::{DEFAULT_ROUTER_PORT, Paths, PathsError, router_port, router_url};
use huddle_testkit::temp_home::TempHome;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;

fn paths(vars: &[(&str, &str)]) -> Paths {
    let mut all = vec![("HOME", "/home/u")];
    all.extend_from_slice(vars);
    Paths::from_env(&Env::with(&all)).expect("paths")
}

#[test]
fn t1_defaults_sit_in_the_xdg_default_folders() {
    let p = paths(&[]);
    assert_eq!(p.home, PathBuf::from("/home/u"));
    assert_eq!(p.config_dir, PathBuf::from("/home/u/.config/huddle"));
    assert_eq!(p.data_dir, PathBuf::from("/home/u/.local/share/huddle"));
    assert_eq!(p.state_dir, PathBuf::from("/home/u/.local/state/huddle"));
    assert_eq!(p.claude_dir, PathBuf::from("/home/u/.claude"));
}

#[test]
fn t1_derived_dirs_follow_their_parents() {
    let p = paths(&[("HUDDLE_DATA_HOME", "/x/d"), ("HUDDLE_STATE_HOME", "/x/s")]);
    assert_eq!(p.bin_dir, PathBuf::from("/x/d/bin"));
    assert_eq!(p.ca_dir, PathBuf::from("/x/d/ca"));
    assert_eq!(p.run_dir, PathBuf::from("/x/s/run"));
    assert_eq!(p.jobs_root, PathBuf::from("/x/s/jobs"));
}

#[test]
fn t1_each_xdg_variable_moves_its_kind() {
    let p = paths(&[
        ("XDG_CONFIG_HOME", "/xc"),
        ("XDG_DATA_HOME", "/xd"),
        ("XDG_STATE_HOME", "/xs"),
    ]);
    assert_eq!(p.config_dir, PathBuf::from("/xc/huddle"));
    assert_eq!(p.data_dir, PathBuf::from("/xd/huddle"));
    assert_eq!(p.state_dir, PathBuf::from("/xs/huddle"));
    assert_eq!(p.ca_dir, PathBuf::from("/xd/huddle/ca"));
    assert_eq!(p.run_dir, PathBuf::from("/xs/huddle/run"));
}

#[test]
fn t1_each_huddle_variable_wins_over_xdg() {
    let p = paths(&[
        ("XDG_CONFIG_HOME", "/xc"),
        ("XDG_DATA_HOME", "/xd"),
        ("XDG_STATE_HOME", "/xs"),
        ("HUDDLE_CONFIG_HOME", "/hc"),
        ("HUDDLE_DATA_HOME", "/hd"),
        ("HUDDLE_STATE_HOME", "/hs"),
        ("CLAUDE_CONFIG_DIR", "/cc"),
    ]);
    assert_eq!(p.config_dir, PathBuf::from("/hc"));
    assert_eq!(p.data_dir, PathBuf::from("/hd"));
    assert_eq!(p.state_dir, PathBuf::from("/hs"));
    assert_eq!(p.claude_dir, PathBuf::from("/cc"));
}

#[test]
fn t1_an_empty_value_counts_as_unset() {
    let p = paths(&[
        ("HUDDLE_CONFIG_HOME", ""),
        ("HUDDLE_DATA_HOME", ""),
        ("HUDDLE_STATE_HOME", ""),
        ("XDG_CONFIG_HOME", ""),
        ("XDG_DATA_HOME", ""),
        ("XDG_STATE_HOME", ""),
        ("CLAUDE_CONFIG_DIR", ""),
    ]);
    assert_eq!(p.config_dir, PathBuf::from("/home/u/.config/huddle"));
    assert_eq!(p.data_dir, PathBuf::from("/home/u/.local/share/huddle"));
    assert_eq!(p.state_dir, PathBuf::from("/home/u/.local/state/huddle"));
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
        0o755
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

#[test]
fn t12_a_relative_xdg_or_huddle_value_is_ignored() {
    let p = paths(&[
        ("XDG_DATA_HOME", "rel/data"),
        ("HUDDLE_STATE_HOME", "rel/state"),
        ("XDG_STATE_HOME", "/xs"),
        ("XDG_CONFIG_HOME", "./c"),
    ]);
    assert_eq!(p.data_dir, PathBuf::from("/home/u/.local/share/huddle"));
    assert_eq!(p.state_dir, PathBuf::from("/xs/huddle"));
    assert_eq!(p.config_dir, PathBuf::from("/home/u/.config/huddle"));
}

#[test]
fn t12_the_run_dir_follows_the_runtime_variables() {
    assert_eq!(
        paths(&[]).run_dir,
        PathBuf::from("/home/u/.local/state/huddle/run")
    );
    assert_eq!(
        paths(&[("XDG_RUNTIME_DIR", "/run/user/1")]).run_dir,
        PathBuf::from("/run/user/1/huddle")
    );
    assert_eq!(
        paths(&[
            ("XDG_RUNTIME_DIR", "/run/user/1"),
            ("HUDDLE_RUNTIME_DIR", "/rt")
        ])
        .run_dir,
        PathBuf::from("/rt")
    );
    assert_eq!(
        paths(&[
            ("XDG_RUNTIME_DIR", "relative"),
            ("HUDDLE_RUNTIME_DIR", "also/rel")
        ])
        .run_dir,
        PathBuf::from("/home/u/.local/state/huddle/run")
    );
}

#[test]
fn t12_the_cache_dir_defaults_and_overrides() {
    assert_eq!(paths(&[]).cache_dir, PathBuf::from("/home/u/.cache/huddle"));
    assert_eq!(
        paths(&[("XDG_CACHE_HOME", "/xc")]).cache_dir,
        PathBuf::from("/xc/huddle")
    );
    assert_eq!(
        paths(&[("XDG_CACHE_HOME", "/xc"), ("HUDDLE_CACHE_HOME", "/hc")]).cache_dir,
        PathBuf::from("/hc")
    );
    assert_eq!(
        paths(&[("XDG_CACHE_HOME", "rel")]).cache_dir,
        PathBuf::from("/home/u/.cache/huddle")
    );
}

#[test]
fn t12_create_dir_uses_the_given_mode_and_keeps_an_existing_one() {
    let home = TempHome::new();
    let deep = home.root().join("a/b/c");
    huddle_config::files::create_dir(&deep, 0o755).expect("create");
    for dir in ["a", "a/b", "a/b/c"] {
        let mode = std::fs::metadata(home.root().join(dir))
            .expect("meta")
            .permissions()
            .mode();
        assert_eq!(mode & 0o777, 0o755, "{dir}");
    }
    let private = home.root().join("p/q");
    huddle_config::files::create_dir(&private, 0o700).expect("private");
    let mode = std::fs::metadata(&private)
        .expect("meta")
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o700);
    let open = home.root().join("open");
    std::fs::create_dir(&open).expect("mkdir");
    std::fs::set_permissions(&open, std::fs::Permissions::from_mode(0o750)).expect("chmod");
    huddle_config::files::create_dir(&open, 0o755).expect("existing");
    let mode = std::fs::metadata(&open).expect("meta").permissions().mode();
    assert_eq!(mode & 0o777, 0o750);
}
