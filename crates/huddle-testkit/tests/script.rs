use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;

use huddle_testkit::script::write_executable;
use huddle_testkit::temp_home::TempHome;

#[test]
fn a_script_runs_right_after_its_write_while_other_threads_fork() {
    let home = TempHome::new();
    let stop = Arc::new(AtomicBool::new(false));
    let forkers: Vec<_> = (0..2)
        .map(|_| {
            let stop = Arc::clone(&stop);
            thread::spawn(move || {
                while !stop.load(Ordering::Relaxed) {
                    let _ = Command::new("true").status();
                }
            })
        })
        .collect();
    let writers: Vec<_> = (0..2)
        .map(|writer| {
            let dir = home.root().join(format!("writer-{writer}"));
            thread::spawn(move || -> Result<(), String> {
                fs::create_dir_all(&dir).map_err(|err| err.to_string())?;
                for round in 0..200 {
                    let path = dir.join(format!("script-{round}"));
                    write_executable(&path, "#!/bin/sh\nexit 0\n")
                        .map_err(|err| format!("write {}: {err}", path.display()))?;
                    let status = Command::new(&path)
                        .status()
                        .map_err(|err| format!("exec {}: {err:?}", path.display()))?;
                    if !status.success() {
                        return Err(format!("{} exited {status}", path.display()));
                    }
                }
                Ok(())
            })
        })
        .collect();
    let results: Vec<_> = writers
        .into_iter()
        .map(|writer| writer.join().unwrap())
        .collect();
    stop.store(true, Ordering::Relaxed);
    for forker in forkers {
        forker.join().unwrap();
    }
    for result in results {
        result.unwrap();
    }
}

#[test]
fn a_written_script_is_executable_and_holds_its_contents() {
    let home = TempHome::new();
    let path = home.root().join("tool");
    write_executable(&path, "#!/bin/sh\necho hi\n").unwrap();
    assert_eq!(fs::read_to_string(&path).unwrap(), "#!/bin/sh\necho hi\n");
    let mode = fs::metadata(&path).unwrap().permissions().mode();
    assert_eq!(mode & 0o777, 0o755);
    let out = Command::new(&path).output().unwrap();
    assert_eq!(String::from_utf8_lossy(&out.stdout), "hi\n");
}

#[test]
fn a_write_into_a_missing_directory_names_the_path() {
    let home = TempHome::new();
    let path = home.root().join("missing").join("tool");
    let err = write_executable(&path, "#!/bin/sh\n").unwrap_err();
    assert!(
        err.to_string().contains(&path.display().to_string()),
        "{err}"
    );
}
