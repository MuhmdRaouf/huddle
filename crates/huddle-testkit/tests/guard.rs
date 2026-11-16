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

// The pids of every process whose ps line names this home: a run's processes carry their run
// directory, under the home, in TMPDIR.
fn processes_naming(home: &TempHome) -> Vec<i32> {
    let listing = std::process::Command::new("ps")
        .args(["axeww", "-o", "pid=,command="])
        .output()
        .unwrap();
    let needle = format!("{}/", home.root().display());
    String::from_utf8_lossy(&listing.stdout)
        .lines()
        .filter(|line| line.contains(&needle))
        .filter_map(|line| line.split_whitespace().next()?.parse().ok())
        .filter(|pid| *pid != std::process::id() as i32)
        .collect()
}

#[test]
fn a_loop_that_keeps_forking_leaves_no_process_behind() {
    let home = TempHome::new();
    // A loop that starts a long-lived child every 10 ms: one starts between the guard's look at
    // the process table and its kill, and outlives the loop.
    let script = "printf 'while :; do sleep 5 & sleep 0.01; done\\n' > \"$TMPDIR/spin.sh\"\n\
                  nohup sh \"$TMPDIR/spin.sh\" >/dev/null 2>&1 &\nsleep 1";
    let (code, out) = run(&home, script);
    assert_eq!(code, 1, "{out}");
    thread::sleep(Duration::from_millis(300));
    let left = processes_naming(&home);
    for pid in &left {
        // SAFETY: kill only sends a signal to a pid this test's own run started.
        unsafe { libc::kill(*pid, libc::SIGKILL) };
    }
    assert!(
        left.is_empty(),
        "processes outlived the guard: {left:?}\n{out}"
    );
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

#[test]
fn e4_every_run_leases_ports_from_one_shared_folder() {
    let home = TempHome::new();
    let seen = home.root().join("seen");
    let (code, out) = run(
        &home,
        &format!("printf %s \"$HUDDLE_PORT_LEASES\" > '{}'", seen.display()),
    );
    assert_eq!(code, 0, "{out}");
    assert_eq!(
        fs::read_to_string(seen).unwrap(),
        home.root().join("huddle-port-leases").display().to_string(),
        "the folder sits beside the run directories, so the next run sees the same leases"
    );
}

#[test]
fn t7_guard_names_what_a_leftover_holds() {
    let home = TempHome::new();
    let script = "mkdir -p \"$TMPDIR/hleft/.local/state/huddle\"\n\
                  echo secret > \"$TMPDIR/hleft/.local/state/huddle/huddle.db\"\n\
                  mkdir -p \"$TMPDIR/hleft/repo/deep/deeper\"";
    let (code, out) = run(&home, script);
    assert_eq!(code, 1, "{out}");
    assert!(out.contains("hleft"), "{out}");
    assert!(out.contains(".local"), "{out}");
    assert!(out.contains(".local/state"), "{out}");
    assert!(out.contains("repo/deep"), "{out}");
    assert!(out.contains("repo/deep/deeper"), "every level shown: {out}");
    assert!(out.contains("huddle.db"), "{out}");
    assert!(
        !out.contains("secret"),
        "a database's contents shown: {out}"
    );
    assert!(guard_dirs(&home).is_empty());
}
