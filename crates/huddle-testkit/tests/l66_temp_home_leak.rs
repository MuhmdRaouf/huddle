use std::fs;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, mpsc};
use std::thread;
use std::time::Duration;

use huddle_testkit::guard::run_guarded;
use huddle_testkit::temp_home::TempHome;

#[test]
fn l66_a_home_dropped_after_its_writer_ended_leaves_nothing() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let state = root.join(".local/state/huddle");
    let stop = Arc::new(AtomicBool::new(false));
    // A writer the process table does not show, such as a thread of the test itself. The drop
    // cannot see it, so the test ends it before the drop, after it has written at least once.
    let (written, wrote) = mpsc::channel();
    let writer = {
        let stop = Arc::clone(&stop);
        thread::spawn(move || {
            while !stop.load(Ordering::SeqCst) {
                let _ = fs::create_dir_all(&state);
                let _ = fs::write(state.join("huddle.db"), "row");
                let _ = written.send(());
                thread::sleep(Duration::from_millis(5));
            }
        })
    };
    wrote.recv().expect("the writer wrote once");
    stop.store(true, Ordering::SeqCst);
    writer.join().expect("writer thread");

    drop(home);

    assert!(!root.exists(), "{} was left behind", root.display());
}

#[test]
fn l66_a_home_records_the_name_of_its_test() {
    let home = TempHome::new();
    let named = fs::read_to_string(home.root().join(".test-name")).expect("the marker file");
    assert_eq!(named.trim(), "l66_a_home_records_the_name_of_its_test");
}

#[test]
fn l66_the_guard_names_the_test_and_what_is_inside() {
    let home = TempHome::new();
    let script = "mkdir -p \"$TMPDIR/h123456/.local/state/huddle/deep\"\n\
                  printf 'some_test_name\\n' > \"$TMPDIR/h123456/.test-name\"\n\
                  printf 'first log line\\nsecond\\n' > \"$TMPDIR/h123456/.local/state/huddle/router.log\"";
    let command = vec!["sh".to_string(), "-c".to_string(), script.to_string()];
    let mut out = Vec::new();
    let code = run_guarded(home.root(), &command, &mut out);
    let out = String::from_utf8_lossy(&out);

    assert_eq!(code, 1);
    assert!(out.contains("h123456"), "{out}");
    assert!(out.contains("some_test_name"), "{out}");
    assert!(out.contains(".local/state/huddle/deep"), "{out}");
    assert!(out.contains("router.log: first log line"), "{out}");
    assert!(!out.contains("second"), "{out}");
}
