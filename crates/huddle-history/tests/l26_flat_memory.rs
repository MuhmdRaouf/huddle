//! L26: concurrent 90-day history calls keep the process's memory flat.

mod common;

use std::process::Command;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::thread;
use std::time::Duration;

use common::{DAY, db_path, row};
use huddle_history::{GroupBy, History, Range, Row};
use huddle_testkit::temp_home::TempHome;

const NOW: u64 = 1_800_000_000_000;
const ROWS: u64 = 55_000;
const CALLS: usize = 10;
const LIMIT: u64 = 30 << 20;

fn generated(i: u64) -> Row {
    let mut r = row(
        NOW - 90 * DAY + i * (90 * DAY / ROWS),
        &format!("model-{}", i % 12),
    );
    r.session = Some(format!("session-{}", i % 3_000));
    r.project = Some(format!("project-{}", i % 40));
    r.tokens_in = Some(1000 + i % 500);
    r.tokens_out = Some(200 + i % 300);
    r
}

/// The resident size of this process in bytes, read from the kernel's table.
fn resident() -> u64 {
    let out = Command::new("ps")
        .args(["-o", "rss=", "-p", &std::process::id().to_string()])
        .output()
        .expect("ps");
    let kib: u64 = String::from_utf8_lossy(&out.stdout)
        .trim()
        .parse()
        .expect("rss");
    kib << 10
}

#[test]
fn l26_ten_concurrent_90_day_calls_stay_flat() {
    let home = TempHome::new();
    let path = db_path(&home);
    {
        let mut history = History::open(&path).expect("open");
        let rows: Vec<Row> = (0..ROWS).map(generated).collect();
        history.insert(&rows).expect("insert");
    }
    let range = Range {
        from_ms: NOW - 90 * DAY,
        to_ms: NOW + 1,
    };
    // One warm-up call settles the allocator before the baseline.
    let warm = History::open_read(&path).expect("open read");
    warm.totals(range, GroupBy::Session).expect("totals");
    drop(warm);

    let base = resident();
    let peak = AtomicU64::new(base);
    let done = AtomicBool::new(false);
    thread::scope(|scope| {
        scope.spawn(|| {
            while !done.load(Ordering::Relaxed) {
                peak.fetch_max(resident(), Ordering::Relaxed);
                thread::sleep(Duration::from_millis(5));
            }
        });
        let calls: Vec<_> = (0..CALLS)
            .map(|_| {
                scope.spawn(|| {
                    let reader = History::open_read(&path).expect("open read");
                    reader.totals(range, GroupBy::Session).expect("totals");
                    reader.buckets(range, DAY, None, None).expect("buckets");
                })
            })
            .collect();
        for call in calls {
            call.join().expect("call");
        }
        done.store(true, Ordering::Relaxed);
    });
    let grew = peak.load(Ordering::Relaxed).saturating_sub(base);
    assert!(
        grew < LIMIT,
        "ten calls grew resident memory by {grew} bytes"
    );
}
