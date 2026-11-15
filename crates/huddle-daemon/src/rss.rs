//! The daemon's memory ceiling: a daemon whose resident size passes the ceiling hands itself
//! over to a fresh copy of its own binary. The decision is pure, the reading is the kernel's own
//! accounting of the process (no child process), and the spawn itself belongs to the serve loop.

use std::fmt;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use huddle_config::env::Env;
use huddle_router::live::ProcessUsage;

/// The resident size a daemon hands itself over past: one GiB.
pub const DEFAULT_CEILING: u64 = 1 << 30;

/// How often a serving daemon looks at its own resident size.
pub const DEFAULT_INTERVAL: Duration = Duration::from_secs(30);

#[derive(Debug)]
pub enum RssError {
    /// The kernel refused or did not answer the reading.
    Read(String),
}

impl fmt::Display for RssError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RssError::Read(message) => f.write_str(message),
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

/// One reading of a process: its memory footprint and the CPU time it has used so far.
struct Sample {
    footprint: u64,
    cpu_ns: u64,
}

/// A CPU reading and when it was taken.
struct CpuMark {
    at: Instant,
    cpu_ns: u64,
}

/// The previous CPU reading and the pid it belongs to: the next reading's interval starts there.
static LAST_CPU: Mutex<Option<(u32, CpuMark)>> = Mutex::new(None);

/// CPU time used between two readings over the wall time between them, as a percent of one core.
/// The first reading has no interval and shows nothing.
fn cpu_percent(prev: Option<&CpuMark>, now: &CpuMark) -> Option<f64> {
    let prev = prev?;
    let wall = now.at.checked_duration_since(prev.at)?.as_nanos();
    if wall == 0 {
        return None;
    }
    let cpu = now.cpu_ns.saturating_sub(prev.cpu_ns);
    Some(cpu as f64 * 100.0 / wall as f64)
}

/// On macOS the footprint is `ri_phys_footprint`: resident plus compressed and swapped pages, the
/// number Activity Monitor shows.
// `libc` marks the timebase deprecated in favour of the `mach2` crate; the one call does not
// justify a dependency.
#[cfg(target_os = "macos")]
#[allow(deprecated)]
fn sample(pid: u32) -> Result<Sample, RssError> {
    let pid = libc::c_int::try_from(pid).map_err(|_| RssError::Read(format!("pid {pid}")))?;
    let mut info = std::mem::MaybeUninit::<libc::rusage_info_v2>::uninit();
    // SAFETY: the buffer is a `rusage_info_v2`, the layout flavor 2 fills.
    let status =
        unsafe { libc::proc_pid_rusage(pid, libc::RUSAGE_INFO_V2, info.as_mut_ptr().cast()) };
    if status != 0 {
        return Err(RssError::Read(format!(
            "proc_pid_rusage failed ({})",
            std::io::Error::last_os_error()
        )));
    }
    // SAFETY: the call succeeded, so the kernel filled the whole struct.
    let info = unsafe { info.assume_init() };
    let mut base = libc::mach_timebase_info { numer: 0, denom: 0 };
    // SAFETY: `base` is a valid out-pointer.
    if unsafe { libc::mach_timebase_info(&mut base) } != 0 || base.denom == 0 {
        return Err(RssError::Read("mach_timebase_info failed".to_string()));
    }
    let ticks = info.ri_user_time.saturating_add(info.ri_system_time);
    let cpu_ns = (u128::from(ticks) * u128::from(base.numer) / u128::from(base.denom)) as u64;
    Ok(Sample {
        footprint: info.ri_phys_footprint,
        cpu_ns,
    })
}

/// On Linux the footprint is the resident set from `/proc/<pid>/statm`, and the CPU time is the
/// user and system ticks of `/proc/<pid>/stat`.
#[cfg(target_os = "linux")]
fn sample(pid: u32) -> Result<Sample, RssError> {
    let read = |name: &str| {
        std::fs::read_to_string(format!("/proc/{pid}/{name}"))
            .map_err(|error| RssError::Read(format!("/proc/{pid}/{name}: {error}")))
    };
    let bad = |name: &str| RssError::Read(format!("/proc/{pid}/{name} is not a reading"));
    let statm = read("statm")?;
    let pages = statm
        .split_whitespace()
        .nth(1)
        .and_then(|field| field.parse::<u64>().ok())
        .ok_or_else(|| bad("statm"))?;
    let stat = read("stat")?;
    // The command name may hold spaces and parentheses: the fields start after the last `)`.
    let after = stat
        .rsplit_once(')')
        .map(|(_, rest)| rest)
        .ok_or_else(|| bad("stat"))?;
    let mut fields = after.split_whitespace().skip(11);
    let ticks = match (fields.next(), fields.next()) {
        (Some(user), Some(system)) => user
            .parse::<u64>()
            .ok()
            .zip(system.parse::<u64>().ok())
            .map(|(user, system)| user + system),
        _ => None,
    }
    .ok_or_else(|| bad("stat"))?;
    // SAFETY: `sysconf` reads no memory of ours.
    let (page, hz) = unsafe {
        (
            libc::sysconf(libc::_SC_PAGESIZE),
            libc::sysconf(libc::_SC_CLK_TCK),
        )
    };
    if page <= 0 || hz <= 0 {
        return Err(RssError::Read("sysconf failed".to_string()));
    }
    Ok(Sample {
        footprint: pages * page as u64,
        cpu_ns: ticks * 1_000_000_000 / hz as u64,
    })
}

/// Reads one process's memory footprint in bytes from the kernel, with no child process.
/// Blocking: run it off the runtime.
pub fn read_rss(pid: u32) -> Result<u64, RssError> {
    sample(pid).map(|sample| sample.footprint)
}

/// Reads one process's CPU percent since the previous reading and its memory footprint, with no
/// child process. The first reading of a process has no CPU number. Blocking: run it off the
/// runtime.
pub fn read_usage(pid: u32) -> Result<ProcessUsage, RssError> {
    let reading = sample(pid)?;
    let now = CpuMark {
        at: Instant::now(),
        cpu_ns: reading.cpu_ns,
    };
    let mut last = LAST_CPU
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let prev = last
        .as_ref()
        .filter(|(was, _)| *was == pid)
        .map(|(_, mark)| mark);
    let cpu_pct = cpu_percent(prev, &now);
    *last = Some((pid, now));
    Ok(ProcessUsage {
        cpu_pct,
        rss_bytes: Some(reading.footprint),
    })
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

    #[cfg(target_os = "macos")]
    fn child_count() -> usize {
        let pid = std::process::id() as libc::pid_t;
        let mut pids = [0 as libc::pid_t; 256];
        let bytes = std::mem::size_of_val(&pids) as libc::c_int;
        // SAFETY: the buffer is `bytes` long.
        let got = unsafe {
            libc::proc_listchildpids(pid, pids.as_mut_ptr().cast::<libc::c_void>(), bytes)
        };
        got.max(0) as usize
    }

    #[cfg(target_os = "linux")]
    fn child_count() -> usize {
        let Ok(tasks) = std::fs::read_dir("/proc/self/task") else {
            return 0;
        };
        tasks
            .flatten()
            .filter_map(|task| std::fs::read_to_string(task.path().join("children")).ok())
            .map(|children| children.split_whitespace().count())
            .sum()
    }

    #[test]
    fn l7_footprint_reading_grows_with_a_held_allocation() {
        let pid = std::process::id();
        let before = read_rss(pid).expect("footprint");
        let mut held = vec![0u8; 64 << 20];
        for page in held.chunks_mut(4096) {
            page[0] = 1;
        }
        let after = read_rss(pid).expect("footprint");
        assert!(held.iter().step_by(4096).all(|byte| *byte == 1));
        assert!(
            after >= before + (48 << 20),
            "rose from {before} to {after}"
        );
    }

    #[test]
    fn l7_no_ps_child_is_spawned() {
        let pid = std::process::id();
        let before = child_count();
        for _ in 0..10 {
            read_rss(pid).expect("footprint");
            read_usage(pid).expect("usage");
        }
        assert_eq!(child_count(), before);
    }

    #[test]
    fn l7_usage_reading_reports_cpu_and_memory_without_ps() {
        let pid = std::process::id();
        let first = read_usage(pid).expect("usage");
        assert!(first.rss_bytes.is_some_and(|bytes| bytes > 0));
        let spin = Instant::now();
        while spin.elapsed() < Duration::from_millis(150) {
            std::hint::black_box(0u64.wrapping_add(1));
        }
        let second = read_usage(pid).expect("usage");
        assert!(second.cpu_pct.is_some_and(|pct| pct > 0.0));
    }

    #[test]
    fn cpu_percent_is_cpu_time_over_wall_time() {
        let t0 = Instant::now();
        let prev = CpuMark {
            at: t0,
            cpu_ns: 1_000_000_000,
        };
        let now = CpuMark {
            at: t0 + Duration::from_secs(2),
            cpu_ns: 2_000_000_000,
        };
        assert_eq!(cpu_percent(None, &now), None);
        assert_eq!(cpu_percent(Some(&prev), &now), Some(50.0));
    }

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
