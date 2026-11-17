//! L67: a router a hook started needs the install mark. When an uninstall removes the mark after
//! the hook looked, the router takes the lock, finds no mark and leaves without writing anything.

use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::serve::{ServeOptions, ServeOutcome};
use huddle_daemon::supervisor;
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::sync::{Arc, Mutex};
use tokio::sync::watch;

fn options(home: &TempHome, mark: std::path::PathBuf) -> (ServeOptions, Arc<Mutex<Vec<String>>>) {
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    let lines: Arc<Mutex<Vec<String>>> = Arc::default();
    let sink = Arc::clone(&lines);
    let opts = ServeOptions {
        env,
        paths,
        port: free_port(),
        version: "test".into(),
        takeover: false,
        by: "session-start".into(),
        install_mark: Some(mark),
        signal: Arc::default(),
        log: Arc::new(move |line| sink.lock().expect("lines").push(line)),
        output: None,
    };
    (opts, lines)
}

#[tokio::test]
async fn l67_a_router_without_the_install_mark_leaves_nothing_after_taking_the_lock() {
    let home = TempHome::new();
    let mark = home.root().join(".local/state/huddle/settings-ledger.toml");
    let (opts, lines) = options(&home, mark);
    let run_dir = opts.paths.run_dir.clone();
    let state_dir = opts.paths.state_dir.clone();
    let (_stop_tx, stop_rx) = watch::channel(false);

    let outcome = supervisor::run(opts, stop_rx).await.expect("run");

    assert_eq!(outcome, ServeOutcome::Stopped);
    assert!(!run_dir.exists(), "{} is left", run_dir.display());
    assert!(!state_dir.exists(), "{} is left", state_dir.display());
    assert!(
        lines.lock().expect("lines").is_empty(),
        "nothing is logged: {:?}",
        lines.lock().expect("lines")
    );
}

#[tokio::test]
async fn l67_a_router_without_the_mark_takes_back_only_what_it_made() {
    // The run folder was there before this start: it stays with what it holds; only the lock
    // file this start made goes.
    let home = TempHome::new();
    let (opts, _lines) = options(&home, home.root().join("no-mark"));
    let run_dir = opts.paths.run_dir.clone();
    std::fs::create_dir_all(&run_dir).expect("mkdir");
    std::fs::write(run_dir.join("keep"), "x").expect("write");
    let (_stop_tx, stop_rx) = watch::channel(false);

    let outcome = supervisor::run(opts, stop_rx).await.expect("run");

    assert_eq!(outcome, ServeOutcome::Stopped);
    assert!(
        run_dir.join("keep").exists(),
        "a file it did not make stays"
    );
    assert!(!run_dir.join("router.lock").exists(), "its lock file goes");
}

#[tokio::test]
async fn l73_a_router_without_the_mark_leaves_before_it_binds_or_logs() {
    // The port is taken, so a start that got as far as the bind would fail and log it.
    let home = TempHome::new();
    let (opts, lines) = options(&home, home.root().join("no-mark"));
    let _taken = std::net::TcpListener::bind(("127.0.0.1", opts.port)).expect("bind");
    let run_dir = opts.paths.run_dir.clone();
    let state_dir = opts.paths.state_dir.clone();
    let (_stop_tx, stop_rx) = watch::channel(false);

    let outcome = supervisor::run(opts, stop_rx).await.expect("run");

    assert_eq!(outcome, ServeOutcome::Stopped);
    assert!(!run_dir.exists(), "{} is left", run_dir.display());
    assert!(!state_dir.exists(), "{} is left", state_dir.display());
    assert!(lines.lock().expect("lines").is_empty());
}
