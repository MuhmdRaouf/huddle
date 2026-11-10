//! The `huddle router serve` word: the daemon in this foreground process.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::serve::{ServeOptions, ServeOutcome};
use huddle_router::crashlog;
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::watch;

const VERSION_FILE: &str = include_str!("../../../../../VERSION");

pub fn run(args: &[String], env: &Env) -> i32 {
    let Some(port) = super::router_port(env) else {
        return 2;
    };
    // Only the exact word is read; anything else after serve is ignored.
    let takeover = args.iter().any(|argument| argument == "--takeover");
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let log = crashlog::rotating_log(
        &paths.router_dir.join("router.log"),
        crashlog::ROUTER_LOG_LIMIT,
    );
    crashlog::install_crash_hook(&log);
    let options = ServeOptions {
        env: env.clone(),
        paths,
        port,
        version: VERSION_FILE.trim().to_string(),
        takeover,
        log,
    };
    let runtime = match tokio::runtime::Runtime::new() {
        Ok(runtime) => runtime,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let code = runtime.block_on(async move {
        let (stop_sender, stop_receiver) = watch::channel(false);
        let signals = tokio::spawn(stop_on_signal(stop_sender));
        let outcome = huddle_daemon::serve::run(options, stop_receiver).await;
        signals.abort();
        match outcome {
            Ok(ServeOutcome::Stopped | ServeOutcome::HandedOver) => 0,
            Ok(ServeOutcome::AlreadyServed { pid }) => {
                let _ = writeln!(
                    std::io::stdout(),
                    "huddle-router: already served by pid {pid}"
                );
                0
            }
            Err(error) => {
                let _ = writeln!(std::io::stderr(), "{error}");
                1
            }
        }
    });
    // The word ends here: dropping the runtime would wait for the takeover lock's blocking
    // wait, and the outcome is already on its stream.
    std::process::exit(code);
}

/// Sets the stop flag on SIGINT or SIGTERM. A failed registration is logged and the daemon
/// stays stoppable only from outside.
async fn stop_on_signal(stop: watch::Sender<bool>) {
    let mut sigint = match signal(SignalKind::interrupt()) {
        Ok(sig) => sig,
        Err(error) => {
            eprintln!("huddle-router: {error}");
            return;
        }
    };
    let mut sigterm = match signal(SignalKind::terminate()) {
        Ok(sig) => sig,
        Err(error) => {
            eprintln!("huddle-router: {error}");
            return;
        }
    };
    tokio::select! {
        _ = sigint.recv() => {}
        _ = sigterm.recv() => {}
    }
    let _ = stop.send(true);
}
