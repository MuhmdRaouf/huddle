use huddle_procs::stream::{
    Forced, InputHandle, Limits, NextLine, SoftStop, Spec, Stdin, Streaming, spawn_streaming,
};
use huddle_testkit::temp_home::TempHome;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

fn cmd(script: &str, cwd: PathBuf, stdin: Stdin) -> Spec {
    Spec {
        program: "/bin/sh".into(),
        args: vec!["-c".into(), script.into()],
        cwd,
        env: vec![("PATH".into(), "/usr/bin:/bin".into())],
        stdin,
        stdout_log: None,
        benign_stderr: None,
        reap_group: false,
        soft_stop: None,
    }
}

fn limits(grace_ms: u64, timeout_ms: Option<u64>, cap: u64) -> Limits {
    Limits {
        grace: Duration::from_millis(grace_ms),
        timeout: timeout_ms.map(Duration::from_millis),
        output_cap: cap,
    }
}

fn alive(pid: u32) -> bool {
    // SAFETY: kill with signal 0 only probes existence.
    unsafe { libc::kill(pid as i32, 0) == 0 }
}

fn drain(stream: &mut Streaming) {
    while let NextLine::Line(_) = stream.next_line(Some(Duration::from_secs(10))) {}
}

#[test]
fn a_timeout_terminates_the_group_and_reports_forced_timeout() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        cmd("sleep 30", home.root().to_path_buf(), Stdin::Piped),
        limits(200, Some(100), u64::MAX),
    )
    .expect("spawn");
    drain(&mut stream);
    let exit = stream.wait();
    assert_eq!(exit.forced, Some(Forced::Timeout));
    assert_eq!(exit.signal.as_deref(), Some("SIGTERM"));
}

#[test]
fn a_zero_timeout_means_no_timer() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        cmd("exit 0", home.root().to_path_buf(), Stdin::Piped),
        limits(100, Some(0), u64::MAX),
    )
    .expect("spawn");
    drain(&mut stream);
    let exit = stream.wait();
    assert_eq!(exit.code, Some(0));
    assert_eq!(exit.forced, None);
}

#[test]
fn the_first_interrupt_reason_wins() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        cmd("sleep 30", home.root().to_path_buf(), Stdin::Piped),
        limits(100, None, u64::MAX),
    )
    .expect("spawn");
    let stop = stream.stop_handle();
    stop.interrupt(Forced::Stopped);
    stop.interrupt(Forced::Timeout);
    drain(&mut stream);
    assert_eq!(stream.wait().forced, Some(Forced::Stopped));
}

#[test]
fn the_soft_stop_runs_first_and_a_clean_exit_stays_a_clean_exit() {
    let home = TempHome::new();
    // The SIGTERM must find the trap installed, or the child dies before it can answer; the
    // ready line proves the trap is in.
    let script = r#"trap '' TERM; echo ready; read line; echo "got:$line"; exit 0"#;
    let saw = Arc::new(AtomicBool::new(false));
    let saw2 = saw.clone();
    let soft: SoftStop = Box::new(move |input: &InputHandle| {
        saw2.store(true, Ordering::SeqCst);
        input.write_line("abort");
        input.end_input();
    });
    let mut spec = cmd(script, home.root().to_path_buf(), Stdin::Piped);
    spec.soft_stop = Some(soft);
    let mut stream = spawn_streaming(spec, limits(2000, None, u64::MAX)).expect("spawn");
    assert!(
        matches!(stream.next_line(Some(Duration::from_secs(5))), NextLine::Line(l) if l == "ready")
    );
    let stop = stream.stop_handle();
    let started = Instant::now();
    stop.interrupt(Forced::Stopped);
    assert!(saw.load(Ordering::SeqCst));
    assert!(
        matches!(stream.next_line(Some(Duration::from_secs(5))), NextLine::Line(l) if l == "got:abort")
    );
    drain(&mut stream);
    let exit = stream.wait();
    assert_eq!(exit.code, Some(0));
    assert_eq!(exit.forced, Some(Forced::Stopped));
    assert!(started.elapsed() < Duration::from_secs(5));
}

#[test]
fn a_soft_stop_that_waits_on_the_group_is_cut_off_by_the_grace() {
    let home = TempHome::new();
    let soft: SoftStop = Box::new(|input: &InputHandle| {
        input.end_input();
        while huddle_procs::group::group_alive(input.pid()) {
            std::thread::sleep(Duration::from_millis(25));
        }
    });
    let mut spec = cmd("sleep 30", home.root().to_path_buf(), Stdin::Piped);
    spec.soft_stop = Some(soft);
    let mut stream = spawn_streaming(spec, limits(150, None, u64::MAX)).expect("spawn");
    let stop = stream.stop_handle();
    let started = Instant::now();
    stop.interrupt(Forced::Stopped);
    drain(&mut stream);
    assert_eq!(stream.wait().forced, Some(Forced::Stopped));
    assert!(started.elapsed() < Duration::from_secs(5));
}

#[test]
fn a_soft_stop_that_never_finishes_is_abandoned_after_the_grace() {
    let home = TempHome::new();
    let soft: SoftStop = Box::new(|_: &InputHandle| std::thread::sleep(Duration::from_secs(60)));
    let mut spec = cmd("sleep 30", home.root().to_path_buf(), Stdin::Piped);
    spec.soft_stop = Some(soft);
    let mut stream = spawn_streaming(spec, limits(100, None, u64::MAX)).expect("spawn");
    let stop = stream.stop_handle();
    let started = Instant::now();
    stop.interrupt(Forced::Stopped);
    drain(&mut stream);
    assert_eq!(stream.wait().forced, Some(Forced::Stopped));
    assert!(started.elapsed() < Duration::from_secs(10));
}

#[test]
fn reap_group_terminates_a_leftover_grandchild_when_the_leader_exits() {
    let home = TempHome::new();
    let mut spec = cmd(
        "sleep 30 >/dev/null 2>&1 & echo $!; exit 0",
        home.root().to_path_buf(),
        Stdin::Piped,
    );
    spec.reap_group = true;
    let mut stream = spawn_streaming(spec, limits(200, None, u64::MAX)).expect("spawn");
    let pid: u32 = match stream.next_line(Some(Duration::from_secs(5))) {
        NextLine::Line(l) => l.trim().parse().expect("pid"),
        _ => panic!("no pid"),
    };
    drain(&mut stream);
    let exit = stream.wait();
    assert_eq!(exit.forced, None);
    let deadline = Instant::now() + Duration::from_secs(5);
    while alive(pid) && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(25));
    }
    assert!(!alive(pid), "the grandchild is gone");
}

#[test]
fn by_default_a_grandchild_outlives_the_leader() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        cmd(
            "sleep 30 >/dev/null 2>&1 & echo $!; exit 0",
            home.root().to_path_buf(),
            Stdin::Piped,
        ),
        limits(100, None, u64::MAX),
    )
    .expect("spawn");
    let pid: u32 = match stream.next_line(Some(Duration::from_secs(5))) {
        NextLine::Line(l) => l.trim().parse().expect("pid"),
        _ => panic!("no pid"),
    };
    drain(&mut stream);
    let _ = stream.wait();
    std::thread::sleep(Duration::from_millis(100));
    assert!(alive(pid));
    // SAFETY: the test started this process; it ends it.
    unsafe { libc::kill(pid as i32, libc::SIGKILL) };
}

#[test]
fn pipes_a_grandchild_holds_are_dropped_after_the_leader_exits() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        cmd(
            "sleep 30 &\nexit 0",
            home.root().to_path_buf(),
            Stdin::Piped,
        ),
        limits(100, None, u64::MAX),
    )
    .expect("spawn");
    let started = Instant::now();
    drain(&mut stream);
    let exit = stream.wait();
    assert_eq!(exit.code, Some(0));
    assert!(
        started.elapsed() < Duration::from_secs(5),
        "readers ended by dropping the pipes"
    );
    // SAFETY: the grandchild shares this test's process group leader id, which the test started.
    unsafe { libc::kill(-(stream.pid() as i32), libc::SIGKILL) };
}

#[test]
fn a_line_past_the_budget_kills_the_group_and_is_never_delivered() {
    let home = TempHome::new();
    let script =
        r#"i=0; while [ $i -lt 2000 ]; do printf 'xxxxxxxxxx'; i=$((i+1)); done; sleep 30"#;
    let mut stream = spawn_streaming(
        cmd(script, home.root().to_path_buf(), Stdin::Piped),
        limits(100, None, 1024),
    )
    .expect("spawn");
    assert!(matches!(
        stream.next_line(Some(Duration::from_secs(10))),
        NextLine::Eof
    ));
    let exit = stream.wait();
    assert_eq!(exit.signal.as_deref(), Some("SIGKILL"));
}

#[test]
fn a_complete_line_within_the_budget_passes() {
    let home = TempHome::new();
    let mut stream = spawn_streaming(
        cmd("echo ok", home.root().to_path_buf(), Stdin::Piped),
        limits(100, None, 1024),
    )
    .expect("spawn");
    assert!(
        matches!(stream.next_line(Some(Duration::from_secs(5))), NextLine::Line(l) if l == "ok")
    );
    drain(&mut stream);
    assert_eq!(stream.wait().code, Some(0));
}
