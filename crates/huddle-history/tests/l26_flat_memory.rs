//! L26: concurrent 90-day history calls keep the memory they hold flat.
//!
//! The test counts the bytes the calls hold, not the resident size: glibc gives each thread its
//! own malloc arena, so the resident size of ten fresh threads grows by the arenas, not by what
//! the code keeps.

mod common;

use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::thread;

use common::{DAY, db_path, row};
use huddle_history::{GroupBy, History, Range, Row};
use huddle_testkit::temp_home::TempHome;

const NOW: u64 = 1_800_000_000_000;
const ROWS: u64 = 55_000;
const CALLS: usize = 10;
const LIMIT: usize = 30 << 20;

static CURRENT: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);

struct Counting;

unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let ptr = unsafe { System.alloc(layout) };
        if !ptr.is_null() {
            let now = CURRENT.fetch_add(layout.size(), Ordering::Relaxed) + layout.size();
            PEAK.fetch_max(now, Ordering::Relaxed);
        }
        ptr
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        CURRENT.fetch_sub(layout.size(), Ordering::Relaxed);
        unsafe { System.dealloc(ptr, layout) };
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let grown = unsafe { System.realloc(ptr, layout, new_size) };
        if !grown.is_null() {
            let now = if new_size >= layout.size() {
                CURRENT.fetch_add(new_size - layout.size(), Ordering::Relaxed) + new_size
                    - layout.size()
            } else {
                CURRENT.fetch_sub(layout.size() - new_size, Ordering::Relaxed)
                    - (layout.size() - new_size)
            };
            PEAK.fetch_max(now, Ordering::Relaxed);
        }
        grown
    }
}

#[global_allocator]
static ALLOCATOR: Counting = Counting;

/// The bytes SQLite holds now. SQLite allocates through its own malloc, outside the counting
/// allocator.
fn sqlite_used() -> usize {
    usize::try_from(unsafe { rusqlite::ffi::sqlite3_memory_used() }).unwrap_or(0)
}

/// The most bytes SQLite has held since the last reset; `reset` starts a new window.
fn sqlite_peak(reset: bool) -> usize {
    usize::try_from(unsafe { rusqlite::ffi::sqlite3_memory_highwater(i32::from(reset)) })
        .unwrap_or(0)
}

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
    let warm = History::open_read(&path).expect("open read");
    warm.totals(range, GroupBy::Session).expect("totals");
    drop(warm);

    let rust_base = CURRENT.load(Ordering::Relaxed);
    PEAK.store(rust_base, Ordering::Relaxed);
    let sqlite_base = sqlite_used();
    sqlite_peak(true);
    thread::scope(|scope| {
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
    });
    let rust = PEAK.load(Ordering::Relaxed).saturating_sub(rust_base);
    let sqlite = sqlite_peak(false).saturating_sub(sqlite_base);
    let grew = rust + sqlite;
    println!("ten calls held {rust} bytes in Rust and {sqlite} bytes in SQLite at their peak");
    assert!(
        grew < LIMIT,
        "ten calls grew held memory by {grew} bytes ({rust} Rust, {sqlite} SQLite)"
    );
}
