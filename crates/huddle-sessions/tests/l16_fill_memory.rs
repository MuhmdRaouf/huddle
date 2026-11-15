//! L16: `usage_for` over a large log holds one line and the id set, never the file.

use std::alloc::{GlobalAlloc, Layout, System};
use std::fs::{self, File};
use std::io::{BufWriter, Write};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, UNIX_EPOCH};

use huddle_sessions::usage_for;
use huddle_testkit::temp_home::TempHome;

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

const SINCE_MS: u64 = 1_800_000_000_000;
const LOG_BYTES: usize = 120 * 1024 * 1024;
const LIMIT: usize = 16 * 1024 * 1024;

fn assistant(request_id: &str) -> String {
    format!(
        "{{\"type\":\"assistant\",\"requestId\":\"{request_id}\",\"timestamp\":\"2026-11-11T10:00:00.000Z\",\"cwd\":\"/w/huddle\",\"message\":{{\"model\":\"m\",\"stop_reason\":\"end_turn\",\"content\":[{{\"type\":\"text\",\"text\":\"{}\"}}],\"usage\":{{\"input_tokens\":5,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":3,\"output_tokens\":9}}}}}}\n",
        "x".repeat(900)
    )
}

#[test]
fn l16_a_fill_over_a_large_log_holds_one_line_not_the_file() {
    let home = TempHome::new();
    let project = home.root().join("projects/-w-huddle");
    fs::create_dir_all(&project).expect("mkdir");
    let path = project.join("big.jsonl");
    let wanted: Vec<String> = (0..5).map(|n| format!("req_wanted_{n}")).collect();
    let mut out = BufWriter::new(File::create(&path).expect("create"));
    let mut written = 0;
    let mut n = 0;
    while written < LOG_BYTES {
        let line = assistant(&format!("req_filler_{n}"));
        written += line.len();
        out.write_all(line.as_bytes()).expect("write");
        if n % 20_000 == 0
            && let Some(id) = wanted.get(n / 20_000)
        {
            out.write_all(assistant(id).as_bytes()).expect("write");
        }
        n += 1;
    }
    out.flush().expect("flush");
    drop(out);
    File::options()
        .write(true)
        .open(&path)
        .and_then(|f| f.set_modified(UNIX_EPOCH + Duration::from_millis(SINCE_MS + 10)))
        .expect("mtime");

    let base = CURRENT.load(Ordering::Relaxed);
    PEAK.store(base, Ordering::Relaxed);
    let found = usage_for(home.root(), &wanted, SINCE_MS);
    let delta = PEAK.load(Ordering::Relaxed).saturating_sub(base);

    assert_eq!(found.len(), 5, "every wanted id is found");
    assert!(
        delta < LIMIT,
        "peak heap growth {delta} bytes must stay under {LIMIT}"
    );
}
