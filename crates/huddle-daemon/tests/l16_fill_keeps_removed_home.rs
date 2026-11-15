//! L16: a fill that finds its home removed creates nothing.

use huddle_daemon::history_upkeep::fill;
use huddle_testkit::temp_home::TempHome;

#[test]
fn l16_a_fill_never_recreates_a_removed_home() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let state = root.join(".local/state/huddle");
    let data = root.join(".local/share/huddle");
    let db = state.join("huddle.db");
    let claude = root.join(".claude");
    std::fs::remove_dir_all(&root).expect("remove the home");

    let result = fill(&db, &claude, 1_800_000_000_000, 86_400_000);

    assert!(matches!(result, Ok(0)), "no database, nothing filled");
    assert!(!state.exists(), "state folder stays removed");
    assert!(!data.exists(), "data folder stays removed");
}

#[test]
fn l16_opening_an_existing_database_never_creates_a_folder_or_file() {
    let home = TempHome::new();
    let state = home.root().join("state/huddle");
    let db = state.join("huddle.db");

    assert!(huddle_history::History::open_existing(&db).is_err());

    assert!(!state.exists(), "no folder is created");
    assert!(!db.exists(), "no file is created");
}
