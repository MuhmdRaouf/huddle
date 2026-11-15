//! L40: reading an event's `type` costs no allocation; the delta feed path stays flat.

use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;

use huddle_router::sse::SseUsageTracker;

thread_local! {
    static COUNT: Cell<usize> = const { Cell::new(0) };
}

struct Counting;

unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        COUNT.with(|count| count.set(count.get() + 1));
        unsafe { System.alloc(layout) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        COUNT.with(|count| count.set(count.get() + 1));
        unsafe { System.realloc(ptr, layout, new_size) }
    }
}

#[global_allocator]
static ALLOCATOR: Counting = Counting;

fn chunk() -> Vec<u8> {
    let event = "data: {\"a\":\"x\",\"b\":\"y\",\"c\":\"z\",\"type\":\"content_block_delta\",\"delta\":{\"text\":\"t\"}}\n\n";
    event.repeat(50).into_bytes()
}

#[test]
fn l40_reading_the_type_of_a_delta_event_allocates_nothing() {
    let chunk = chunk();
    let mut tracker = SseUsageTracker::new();
    tracker.feed(&chunk);
    COUNT.with(|count| count.set(0));
    tracker.feed(&chunk);
    assert_eq!(COUNT.with(Cell::get), 0, "the second feed allocates");
    assert!(
        tracker.first_token_seen(),
        "the delta events mark the first token"
    );
}
