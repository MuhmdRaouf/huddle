use std::fs;
use std::path::Path;
use std::thread;

use huddle_testkit::ports::{self, free_port};
use huddle_testkit::temp_home::{TempHome, child_env, inside_git_tree};

fn value<'a>(env: &'a [(String, String)], name: &str) -> Option<&'a str> {
    env.iter()
        .find(|(key, _)| key == name)
        .map(|(_, v)| v.as_str())
}

#[test]
fn every_state_variable_points_inside_the_home() {
    let home = TempHome::new();
    let root = home.root().display().to_string();
    let env = home.env();
    for name in [
        "HOME",
        "CLAUDE_CONFIG_DIR",
        "XDG_CONFIG_HOME",
        "XDG_STATE_HOME",
        "XDG_DATA_HOME",
        "XDG_CACHE_HOME",
        "XDG_RUNTIME_DIR",
        "TMPDIR",
    ] {
        let dir = value(&env, name).unwrap_or_else(|| panic!("{name} missing"));
        assert!(dir.starts_with(&root), "{name}={dir} is outside {root}");
        assert!(Path::new(dir).is_dir(), "{name}={dir} is not a directory");
    }
}

#[test]
fn t1_a_fresh_home_has_the_xdg_folders_and_no_agents_folder() {
    let home = TempHome::new();
    let env = home.env();
    assert_eq!(value(&env, "HUDDLE_HOME"), None);
    assert!(!home.root().join(".agents").exists());
    for dir in [".config", ".local/share", ".local/state"] {
        assert!(home.root().join(dir).is_dir(), "{dir}");
    }
}

#[test]
fn a_child_runs_under_the_private_home_only() {
    let home = TempHome::new();
    let out = home
        .command("sh")
        .args([
            "-c",
            "printf '%s\\n' \"$HOME\" \"$ANTHROPIC_API_KEY$HTTPS_PROXY\"",
        ])
        .env("ANTHROPIC_API_KEY", "")
        .output()
        .unwrap();
    let text = String::from_utf8_lossy(&out.stdout);
    let mut lines = text.lines();
    assert_eq!(lines.next(), Some(home.root().to_str().unwrap()));
    assert_eq!(lines.next(), Some(""));
}

#[test]
fn owner_variables_never_reach_a_child() {
    let inherited: Vec<(String, String)> = [
        ("PATH", "/usr/bin"),
        ("LANG", "C"),
        ("ZAI_API_KEY", "secret"),
        ("KIMI_API_KEY", "secret"),
        ("ANTHROPIC_API_KEY", "secret"),
        ("HTTPS_PROXY", "http://127.0.0.1:4835"),
        ("https_proxy", "http://127.0.0.1:4835"),
        ("CLAUDE_CODE_ENTRYPOINT", "cli"),
        ("HUDDLE_DATA_HOME", "/real"),
        ("HUDDLE_STATE_HOME", "/real"),
        ("XDG_CONFIG_HOME", "/real"),
    ]
    .iter()
    .map(|(k, v)| (k.to_string(), v.to_string()))
    .collect();
    let vars = child_env(&inherited, Path::new("/fake/root"), 20000);
    let names: Vec<&str> = vars.iter().map(|(k, _)| k.as_str()).collect();
    for banned in [
        "ZAI_API_KEY",
        "KIMI_API_KEY",
        "ANTHROPIC_API_KEY",
        "HTTPS_PROXY",
        "https_proxy",
        "CLAUDE_CODE_ENTRYPOINT",
        "HUDDLE_DATA_HOME",
        "HUDDLE_STATE_HOME",
    ] {
        assert!(!names.contains(&banned), "{banned} leaked: {names:?}");
    }
    assert!(names.contains(&"PATH"));
    assert_eq!(
        vars.iter()
            .find(|(k, _)| k == "XDG_CONFIG_HOME")
            .map(|(_, v)| v.as_str()),
        Some("/fake/root/.config")
    );
    assert_eq!(
        vars.iter()
            .find(|(k, _)| k == "HUDDLE_ROUTER_PORT")
            .map(|(_, v)| v.as_str()),
        Some("20000")
    );
}

#[test]
fn dropping_the_home_removes_it() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    assert!(root.is_dir());
    drop(home);
    assert!(!root.exists());
}

#[test]
fn the_home_name_is_short_enough_for_unix_sockets() {
    let home = TempHome::new();
    assert!(home.root().file_name().unwrap().len() <= 8);
    assert_eq!(home.root().parent(), Some(std::env::temp_dir().as_path()));
}

#[test]
fn a_temp_root_inside_a_git_tree_is_found() {
    let home = TempHome::new();
    fs::create_dir(home.root().join(".git")).unwrap();
    let nested = home.root().join("nested");
    fs::create_dir(&nested).unwrap();
    assert_eq!(
        inside_git_tree(&nested),
        Some(home.canonical_root().unwrap())
    );
    assert_eq!(inside_git_tree(Path::new("/")), None);
}

#[test]
fn parallel_callers_never_get_the_same_port() {
    let handles: Vec<_> = (0..32).map(|_| thread::spawn(free_port)).collect();
    let mut seen: Vec<u16> = handles.into_iter().map(|h| h.join().unwrap()).collect();
    let total = seen.len();
    seen.sort_unstable();
    seen.dedup();
    assert_eq!(seen.len(), total);
    assert!(seen.iter().all(|p| (20000..30000).contains(p)));
}

#[test]
fn a_listener_holds_its_port_from_the_moment_it_is_learned() {
    let listener = ports::listen();
    let port = listener.local_addr().unwrap().port();
    assert!(std::net::TcpListener::bind(("127.0.0.1", port)).is_err());
}

#[test]
fn a_stale_lease_is_taken_over_and_a_live_one_is_skipped() {
    let home = TempHome::new();
    let dir = home.root().join("leases");
    // A port leased from the shared lease directory: no other test binds it while this one runs.
    let port = ports::free_port();
    assert_eq!(ports::free_in(&dir, port, port).unwrap(), port);
    assert!(ports::free_in(&dir, port, port).is_err());
    let file = dir.join(port.to_string());
    let old = std::time::SystemTime::now() - std::time::Duration::from_secs(7200);
    fs::File::options()
        .write(true)
        .open(&file)
        .unwrap()
        .set_modified(old)
        .unwrap();
    assert_eq!(ports::free_in(&dir, port, port).unwrap(), port);
}

#[test]
fn parallel_takers_of_one_stale_lease_never_both_win() {
    let home = TempHome::new();
    let dir = home.root().join("leases");
    let port = ports::free_port();
    let file = dir.join(port.to_string());
    let old = std::time::SystemTime::now() - std::time::Duration::from_secs(7200);
    for round in 0..200 {
        let _ = fs::remove_file(&file);
        assert_eq!(ports::free_in(&dir, port, port).unwrap(), port);
        fs::File::options()
            .write(true)
            .open(&file)
            .unwrap()
            .set_modified(old)
            .unwrap();
        let start = std::sync::Arc::new(std::sync::Barrier::new(8));
        let takers: Vec<_> = (0..8)
            .map(|_| {
                let (dir, start) = (dir.clone(), start.clone());
                thread::spawn(move || {
                    start.wait();
                    ports::free_in(&dir, port, port).is_ok()
                })
            })
            .collect();
        let winners = takers
            .into_iter()
            .map(|taker| taker.join().unwrap())
            .filter(|won| *won)
            .count();
        assert_eq!(winners, 1, "round {round}");
    }
}
