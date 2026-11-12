//! huddle-router: probe.
//!
//! While the Anthropic path fails, one task looks at the network on its own: a DNS lookup of the
//! Anthropic host and a bare TCP connect to its first address, one line per round. The task
//! lives as long as its handle; dropping the handle ends it.

use crate::lane::Logger;
use crate::upstream::FailureCode;
use std::io::{self, ErrorKind};
use std::time::{Duration, Instant};
use tokio::net::{TcpStream, lookup_host};
use tokio::sync::watch;

/// How often a round runs and how long its connect may take.
#[derive(Debug, Clone, Copy)]
pub struct ProbeTiming {
    pub interval: Duration,
    pub connect_limit: Duration,
}

impl Default for ProbeTiming {
    fn default() -> ProbeTiming {
        ProbeTiming {
            interval: Duration::from_secs(15),
            connect_limit: Duration::from_secs(5),
        }
    }
}

/// The host and port a round looks at.
#[derive(Debug, Clone)]
pub struct ProbeTarget {
    pub host: String,
    pub port: u16,
}

/// A running probe; dropping it stops the task.
pub struct ProbeHandle {
    _stop: watch::Sender<bool>,
}

/// Starts the probe task on the current runtime. None when no runtime is running.
pub fn start(target: ProbeTarget, timing: ProbeTiming, log: Logger) -> Option<ProbeHandle> {
    let runtime = tokio::runtime::Handle::try_current().ok()?;
    let (stop, mut gone) = watch::channel(false);
    runtime.spawn(async move {
        loop {
            tokio::select! {
                _ = tokio::time::sleep(timing.interval) => {}
                _ = gone.changed() => return,
            }
            let line = round(&target, timing.connect_limit).await;
            if gone.has_changed().is_err() {
                return;
            }
            log(line);
        }
    });
    Some(ProbeHandle { _stop: stop })
}

/// One round: `huddle-router: probe <host> dns=ok(3 ms, 1.2.3.4) connect=timeout(5000 ms)`.
async fn round(target: &ProbeTarget, limit: Duration) -> String {
    let head = format!("huddle-router: probe {}", target.host);
    let started = Instant::now();
    let first = match lookup_host((target.host.as_str(), target.port)).await {
        Ok(mut addrs) => addrs.next(),
        Err(error) => {
            return format!(
                "{head} dns=error({}, {} ms) connect=skipped",
                error_name(&error),
                started.elapsed().as_millis()
            );
        }
    };
    let dns_ms = started.elapsed().as_millis();
    let Some(addr) = first else {
        return format!("{head} dns=error(no address, {dns_ms} ms) connect=skipped");
    };
    let started = Instant::now();
    let connect = match tokio::time::timeout(limit, TcpStream::connect(addr)).await {
        Ok(Ok(_)) => format!("ok({} ms)", started.elapsed().as_millis()),
        Ok(Err(error)) => format!(
            "error({}, {} ms)",
            error_name(&error),
            started.elapsed().as_millis()
        ),
        Err(_) => format!("timeout({} ms)", limit.as_millis()),
    };
    format!(
        "{head} dns=ok({dns_ms} ms, {}) connect={connect}",
        addr.ip()
    )
}

fn error_name(error: &io::Error) -> String {
    match error.kind() {
        ErrorKind::ConnectionRefused => FailureCode::EConnRefused.as_str().to_owned(),
        ErrorKind::TimedOut => FailureCode::ETimedOut.as_str().to_owned(),
        kind => format!("{kind}"),
    }
}
