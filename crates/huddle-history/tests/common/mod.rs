#![allow(dead_code)]

use std::path::PathBuf;

use huddle_history::{Filter, History, Page, Range, Row};
use huddle_testkit::temp_home::TempHome;

pub const DAY: u64 = 86_400_000;

pub fn db_path(home: &TempHome) -> PathBuf {
    home.root().join("scratch/history/huddle.db")
}

pub fn open(home: &TempHome) -> History {
    History::open(&db_path(home)).expect("open")
}

pub fn row(at_ms: u64, model: &str) -> Row {
    Row {
        at_ms,
        model: model.to_string(),
        lane: "zai".to_string(),
        status: 200,
        dur_ms: 100,
        ..Row::default()
    }
}

pub fn all() -> Range {
    Range {
        from_ms: 0,
        to_ms: u64::MAX / 2,
    }
}

pub fn page(limit: u64, offset: u64) -> Page {
    Page { limit, offset }
}

pub fn everything(history: &History) -> Vec<Row> {
    history
        .search(Filter::new(all()), page(500, 0))
        .expect("search")
        .rows
}
