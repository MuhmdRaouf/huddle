//! The daemon's idle cost, R-071: under half a percent of one core with no request in flight.

use huddle_config::{env::Env, paths::Paths};
use huddle_daemon::serve::{ServeOptions, run};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::{fs, sync::Arc, time::Duration};
use tokio::sync::watch;

const BUDGET: f64 = 0.005;
const SAMPLE: Duration = Duration::from_secs(10);

fn cpu_time() -> Duration {
    // SAFETY: getrusage fills the zeroed struct for this process.
    let mut usage: libc::rusage = unsafe { std::mem::zeroed() };
    assert_eq!(unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut usage) }, 0);
    Duration::new(
        usage.ru_utime.tv_sec as u64,
        usage.ru_utime.tv_usec as u32 * 1000,
    ) + Duration::new(
        usage.ru_stime.tv_sec as u64,
        usage.ru_stime.tv_usec as u32 * 1000,
    )
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_idle_daemon_costs_under_half_a_percent_of_one_core() {
    let home = TempHome::new();
    let root = home.root().to_str().expect("utf8").to_string();
    let env = Env::with(&[("HOME", root.as_str())]);
    let paths = Paths::from_env(&env).expect("paths");
    fs::create_dir_all(&paths.router_dir).expect("mkdir");
    fs::write(
        paths.router_dir.join("providers.json"),
        "{\"providers\":[\"zai\"]}",
    )
    .expect("write");
    let (stop_tx, stop_rx) = watch::channel(false);
    let options = ServeOptions {
        env,
        paths,
        port: free_port(),
        version: "test".into(),
        takeover: false,
        log: Arc::new(|_| {}),
    };
    let task = tokio::spawn(run(options, stop_rx));
    tokio::time::sleep(Duration::from_secs(1)).await;
    let before = cpu_time();
    tokio::time::sleep(SAMPLE).await;
    let spent = cpu_time() - before;
    stop_tx.send(true).expect("stop");
    task.await.expect("join").expect("serve");
    let share = spent.as_secs_f64() / SAMPLE.as_secs_f64();
    assert!(
        share < BUDGET,
        "the idle daemon used {:.3}% of a core",
        share * 100.0
    );
}
