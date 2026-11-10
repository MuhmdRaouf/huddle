//! The daemon's memory ceiling: a daemon whose resident size passes the ceiling hands itself
//! over to a fresh copy of its own binary. The decision is pure, the reading is `ps`, and the
//! spawn itself belongs to the serve loop.

use std::fmt;
use std::io;
use std::process::Command;
use std::time::{Duration, Instant};

use huddle_config::env::Env;

/// The resident size a daemon hands itself over past: one GiB.
pub const DEFAULT_CEILING: u64 = 1 << 30;

/// How often a serving daemon looks at its own resident size.
pub const DEFAULT_INTERVAL: Duration = Duration::from_secs(30);

#[derive(Debug)]
pub enum RssError {
    /// `ps` did not run at all.
    Spawn(io::Error),
    /// `ps` ran but printed no rss reading.
    Parse(String),
}

impl fmt::Display for RssError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RssError::Spawn(source) => write!(f, "ps did not run ({source})"),
            RssError::Parse(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for RssError {}

/// The resident size a daemon hands itself over at: `$HUDDLE_ROUTER_RSS_CEILING_MB` in MiB when
/// it names a positive integer, else one GiB.
pub fn ceiling_of(env: &Env) -> u64 {
    if let Some(raw) = env.get("HUDDLE_ROUTER_RSS_CEILING_MB")
        && let Ok(mb) = raw.parse::<u64>()
        && mb > 0
    {
        return mb << 20;
    }
    DEFAULT_CEILING
}

/// How often a daemon looks at its own resident size: `$HUDDLE_ROUTER_RSS_INTERVAL_MS` when it
/// names a positive integer, else thirty seconds.
pub fn interval_of(env: &Env) -> Duration {
    if let Some(raw) = env.get("HUDDLE_ROUTER_RSS_INTERVAL_MS")
        && let Ok(ms) = raw.parse::<u64>()
        && ms > 0
    {
        return Duration::from_millis(ms);
    }
    DEFAULT_INTERVAL
}

/// Reads `ps`'s rss column into bytes: `ps` reports KiB, one line per match, and the first
/// non-empty line wins.
pub fn parse_rss(out: &str) -> Result<u64, RssError> {
    for line in out.split('\n') {
        let Some(field) = line.split_whitespace().next() else {
            continue;
        };
        let Ok(kib) = field.parse::<u64>() else {
            return Err(RssError::Parse(format!("ps printed {line:?}, not an rss")));
        };
        return Ok(kib << 10);
    }
    Err(RssError::Parse("ps printed no rss".to_string()))
}

/// Reads one process's resident size in bytes with `ps -o rss= -p <pid>`. Blocking: run it off
/// the runtime.
pub fn read_rss(pid: u32) -> Result<u64, RssError> {
    let out = Command::new("ps")
        .args(["-o", "rss=", "-p", &pid.to_string()])
        .output()
        .map_err(RssError::Spawn)?;
    parse_rss(&String::from_utf8_lossy(&out.stdout))
}

/// What one check decided.
#[derive(Debug, PartialEq, Eq, Clone)]
pub enum RssDecision {
    /// Keep serving.
    Stay,
    /// Spawn `router serve --takeover` and write these lines to the log first.
    HandOver { lines: Vec<String> },
}

/// The daemon's own memory ceiling, checked once per interval.
pub struct RssWatch {
    ceiling: u64,
    interval: Duration,
    spawned_at: Option<Instant>,
}

impl RssWatch {
    pub fn new(ceiling: u64, interval: Duration) -> RssWatch {
        RssWatch {
            ceiling,
            interval,
            spawned_at: None,
        }
    }

    /// Decides once per check. `rss` is the resident size in bytes. A handover sets the spawn
    /// time before answering: the caller counts the try as made even when the log write or the
    /// spawn itself fails, so the next check waits one interval before trying again.
    pub fn decide(&mut self, rss: u64, now: Instant) -> RssDecision {
        if rss <= self.ceiling {
            return RssDecision::Stay;
        }
        let handover = format!(
            "huddle-router: RSS {} MiB over the ceiling; handing over",
            rss >> 20
        );
        let lines = match self.spawned_at {
            Some(at) if now.duration_since(at) < self.interval => return RssDecision::Stay,
            Some(_) => vec![
                format!(
                    "huddle-router: no takeover arrived within {}; trying again at the next check",
                    format_interval(self.interval)
                ),
                handover,
            ],
            None => vec![handover],
        };
        self.spawned_at = Some(now);
        RssDecision::HandOver { lines }
    }
}

/// Prints an interval the way the daemon logs it: `500ms`, `30s`, `1m30s`.
pub fn format_interval(interval: Duration) -> String {
    let secs = interval.as_secs();
    if secs == 0 {
        return format!("{}ms", interval.subsec_millis());
    }
    if secs < 60 {
        return format!("{secs}s");
    }
    format!("{}m{}s", secs / 60, secs % 60)
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_config::env::Env;

    #[test]
    fn the_ceiling_comes_from_the_environment_or_defaults_to_one_gib() {
        assert_eq!(ceiling_of(&Env::with(&[])), 1 << 30);
        assert_eq!(
            ceiling_of(&Env::with(&[("HUDDLE_ROUTER_RSS_CEILING_MB", "1")])),
            1 << 20
        );
        assert_eq!(
            ceiling_of(&Env::with(&[("HUDDLE_ROUTER_RSS_CEILING_MB", "0")])),
            1 << 30
        );
        assert_eq!(
            ceiling_of(&Env::with(&[("HUDDLE_ROUTER_RSS_CEILING_MB", "junk")])),
            1 << 30
        );
    }

    #[test]
    fn the_interval_comes_from_the_environment_or_defaults_to_thirty_seconds() {
        assert_eq!(interval_of(&Env::with(&[])), Duration::from_secs(30));
        assert_eq!(
            interval_of(&Env::with(&[("HUDDLE_ROUTER_RSS_INTERVAL_MS", "500")])),
            Duration::from_millis(500)
        );
        assert_eq!(
            interval_of(&Env::with(&[("HUDDLE_ROUTER_RSS_INTERVAL_MS", "-1")])),
            Duration::from_secs(30)
        );
    }

    #[test]
    fn parse_reads_the_first_rss_line_in_kib() {
        assert_eq!(parse_rss("  1024\n").expect("parse"), 1 << 20);
        assert_eq!(parse_rss("\n  10\n  20\n").expect("parse"), 10 << 10);
        assert_eq!(
            parse_rss("   \n").expect_err("empty").to_string(),
            "ps printed no rss"
        );
        assert_eq!(
            parse_rss("nope\n").expect_err("nope").to_string(),
            "ps printed \"nope\", not an rss"
        );
    }

    #[test]
    fn read_rss_reads_this_process() {
        assert!(read_rss(std::process::id()).expect("rss") > 0);
    }

    #[test]
    fn under_the_ceiling_nothing_happens() {
        let mut watch = RssWatch::new(100, Duration::from_secs(30));
        assert_eq!(watch.decide(100, Instant::now()), RssDecision::Stay);
    }

    #[test]
    fn over_the_ceiling_hands_over_once() {
        let mut watch = RssWatch::new(100, Duration::from_secs(30));
        let now = Instant::now();
        assert_eq!(
            watch.decide(3 << 20, now),
            RssDecision::HandOver {
                lines: vec!["huddle-router: RSS 3 MiB over the ceiling; handing over".to_string()]
            }
        );
        assert_eq!(
            watch.decide(3 << 20, now + Duration::from_secs(10)),
            RssDecision::Stay,
            "a spawn is in flight"
        );
    }

    #[test]
    fn a_try_no_takeover_answered_is_tried_again_after_one_interval() {
        let mut watch = RssWatch::new(100, Duration::from_secs(30));
        let now = Instant::now();
        let _ = watch.decide(3 << 20, now);
        let later = now + Duration::from_secs(31);
        assert_eq!(
            watch.decide(3 << 20, later),
            RssDecision::HandOver {
                lines: vec![
                    "huddle-router: no takeover arrived within 30s; trying again at the next check"
                        .to_string(),
                    "huddle-router: RSS 3 MiB over the ceiling; handing over".to_string(),
                ],
            }
        );
    }

    #[test]
    fn intervals_print_the_way_the_daemon_logs_them() {
        assert_eq!(format_interval(Duration::from_secs(30)), "30s");
        assert_eq!(format_interval(Duration::from_millis(500)), "500ms");
        assert_eq!(format_interval(Duration::from_secs(90)), "1m30s");
    }
}
