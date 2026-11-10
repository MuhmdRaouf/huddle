use std::fs;
use std::process::Stdio;
use std::thread;
use std::time::Duration;

use huddle_testkit::guard::run_guarded;
use huddle_testkit::temp_home::TempHome;

fn sh(script: &str) -> Vec<String> {
    vec!["sh".into(), "-c".into(), script.into()]
}

fn run(home: &TempHome, script: &str) -> (i32, String) {
    let mut out = Vec::new();
    let code = run_guarded(home.root(), &sh(script), &mut out);
    (code, String::from_utf8_lossy(&out).to_string())
}

fn guard_dirs(home: &TempHome) -> Vec<String> {
    fs::read_dir(home.root())
        .unwrap()
        .map(|e| e.unwrap().file_name().to_string_lossy().to_string())
        .filter(|name| name.starts_with('g') && name.len() == 7)
        .collect()
}

#[test]
fn a_clean_run_passes_the_commands_exit_code_through() {
    let home = TempHome::new();
    assert_eq!(run(&home, "exit 0").0, 0);
    assert_eq!(run(&home, "exit 7").0, 7);
    assert!(
        guard_dirs(&home).is_empty(),
        "the run directory stays behind"
    );
}

#[test]
fn an_entry_left_in_tmpdir_fails_the_run_naming_it() {
    let home = TempHome::new();
    let (code, out) = run(&home, "touch \"$TMPDIR/left-behind\"");
    assert_eq!(code, 1);
    assert!(out.contains("left-behind"), "{out}");
    assert!(guard_dirs(&home).is_empty());
}

#[test]
fn a_process_left_running_is_killed_and_fails_the_run() {
    let home = TempHome::new();
    let script = "printf 'while :; do sleep 1; done\\n' > \"$TMPDIR/hold.sh\"\n\
                  nohup sh \"$TMPDIR/hold.sh\" >/dev/null 2>&1 &\nsleep 1";
    let (code, out) = run(&home, script);
    assert_eq!(code, 1);
    assert!(out.contains("outlived"), "{out}");
    assert!(out.contains("killed"), "{out}");
}

#[test]
fn a_temp_root_inside_a_git_tree_is_refused() {
    let home = TempHome::new();
    fs::create_dir(home.root().join(".git")).unwrap();
    let (code, out) = run(&home, "exit 0");
    assert_eq!(code, 1);
    assert!(out.contains("inside the git work tree"), "{out}");
}

#[test]
fn a_dead_runs_folder_is_swept_and_a_live_one_is_left_alone() {
    let home = TempHome::new();
    let dead = home.root().join("gabc123");
    fs::create_dir(&dead).unwrap();
    let mut gone = home.command("true").stdout(Stdio::null()).spawn().unwrap();
    let pid = gone.id();
    gone.wait().unwrap();
    fs::write(dead.join("owner.pid"), pid.to_string()).unwrap();
    let live = home.root().join("gdef456");
    fs::create_dir(&live).unwrap();
    fs::write(live.join("owner.pid"), std::process::id().to_string()).unwrap();
    thread::sleep(Duration::from_millis(10));
    assert_eq!(run(&home, "exit 0").0, 0);
    assert!(!dead.exists());
    assert!(live.exists());
}
