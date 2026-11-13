//! The `huddle router serve` word, in this foreground process: the supervising parent that holds
//! the port, or with `--worker` the worker it starts, which serves on the socket it inherits.

use std::io::Write;
use std::sync::{Arc, OnceLock};

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_daemon::output::{Saved, Sink, capture};
use huddle_daemon::serve::{ServeError, ServeOptions, ServeOutcome};
use huddle_daemon::supervisor;
use huddle_router::crashlog;
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::{mpsc, watch};

const VERSION_FILE: &str = include_str!("../../../../../VERSION");

pub fn run(args: &[String], env: &Env) -> i32 {
    let Some(port) = super::router_port(env) else {
        return 2;
    };
    // Only the exact word is read; anything else after serve is ignored.
    let takeover = args.iter().any(|argument| argument == "--takeover");
    let worker = args.iter().any(|argument| argument == "--worker");
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let log = crashlog::router_logs(&paths.state_dir);
    crashlog::install_crash_hook(&log);
    let signal: Arc<OnceLock<&'static str>> = Arc::default();
    // Stdout and stderr lines become `output` events; a failed redirect leaves the streams as
    // they are.
    // The parent serves nothing and leaves its streams alone; the worker captures its own.
    let output: Sink = Arc::default();
    let (output, saved) = match worker.then(|| capture(Arc::clone(&log), Arc::clone(&output))) {
        Some(Ok(saved)) => (saved.is_some().then_some(output), saved),
        Some(Err(error)) => {
            log(format!("huddle-router: output capture: {error}"));
            (None, None)
        }
        None => (None, None),
    };
    // The word's own last message goes to the caller's streams, not to the pipe.
    let restore = move || saved.iter().for_each(Saved::restore);
    let inherited = worker.then(|| supervisor::inherited_listener(env));
    let parent = supervisor::parent_of(env);
    let options = ServeOptions {
        env: env.clone(),
        paths,
        port,
        version: VERSION_FILE.trim().to_string(),
        takeover,
        by: super::reason_of(args),
        signal: Arc::clone(&signal),
        log: Arc::clone(&log),
        output,
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
        let (drain_sender, drain_receiver) = mpsc::unbounded_channel();
        let signals = tokio::spawn(stop_on_signal(stop_sender.clone(), drain_sender, signal));
        let outcome = match inherited {
            Some(Ok(listener)) => {
                if let Some(parent) = parent {
                    tokio::spawn(supervisor::stop_when_orphaned(parent, stop_sender));
                }
                huddle_daemon::serve::run_worker(options, listener, stop_receiver, drain_receiver)
                    .await
            }
            Some(Err(text)) => Err(ServeError::Worker(text)),
            None => supervisor::run(options, stop_receiver).await,
        };
        signals.abort();
        match outcome {
            Ok(ServeOutcome::Stopped | ServeOutcome::HandedOver) => 0,
            Ok(ServeOutcome::AlreadyServed { pid }) => {
                log(exit_line(&format!("already served by pid {pid}")));
                restore();
                let _ = writeln!(
                    std::io::stdout(),
                    "huddle-router: already served by pid {pid}"
                );
                0
            }
            Err(error) => {
                log(exit_line(&format!("fatal: {error}")));
                restore();
                let _ = writeln!(std::io::stderr(), "{error}");
                1
            }
        }
    });
    // The word ends here: dropping the runtime would wait for the takeover lock's blocking
    // wait, and the outcome is already on its stream.
    std::process::exit(code);
}

/// Sets the stop flag on SIGINT or SIGTERM, and on SIGUSR1 tells a worker its socket was handed
/// over. A failed registration is logged and the daemon stays stoppable only from outside.
async fn stop_on_signal(
    stop: watch::Sender<bool>,
    handed: mpsc::UnboundedSender<u32>,
    stopped_by: Arc<OnceLock<&'static str>>,
) {
    let (Ok(mut sigint), Ok(mut sigterm), Ok(mut sigusr1)) = (
        signal(SignalKind::interrupt()),
        signal(SignalKind::terminate()),
        signal(SignalKind::user_defined1()),
    ) else {
        eprintln!("huddle-router: signal registration failed");
        return;
    };
    let name = tokio::select! {
        _ = sigint.recv() => "SIGINT",
        _ = sigterm.recv() => "SIGTERM",
        _ = sigusr1.recv() => {
            let _ = handed.send(0);
            return std::future::pending().await;
        }
    };
    let _ = stopped_by.set(name);
    let _ = stop.send(true);
}

/// The exit line of a run that ended before it served.
fn exit_line(reason: &str) -> String {
    format!(
        "huddle-router: exit pid={} reason={reason}",
        std::process::id()
    )
}
