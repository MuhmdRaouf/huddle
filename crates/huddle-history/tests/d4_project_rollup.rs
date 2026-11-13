mod common;

use std::collections::BTreeMap;

use common::*;
use huddle_history::{GroupBy, Row};
use huddle_testkit::temp_home::TempHome;

fn in_project(at_ms: u64, model: &str, project: &str) -> Row {
    Row {
        project: Some(project.to_string()),
        tokens_in: Some(10),
        tokens_out: Some(1),
        ..row(at_ms, model)
    }
}

#[test]
fn d4_history_totals_roll_old_folder_names_into_their_repo() {
    let home = TempHome::new();
    let mut history = open(&home);
    history
        .insert(&[
            in_project(10, "a", "huddle"),
            in_project(20, "a", "dv2-d5"),
            in_project(30, "b", "dv2-d5"),
            in_project(40, "a", "notes"),
        ])
        .expect("insert");
    let aliases = BTreeMap::from([("dv2-d5".to_string(), "huddle".to_string())]);

    let totals = history
        .totals_with_aliases(all(), GroupBy::Project, &aliases)
        .expect("totals");
    let keys: Vec<&str> = totals.iter().map(|t| t.key.as_str()).collect();
    assert_eq!(keys, ["huddle", "notes"]);
    let huddle = &totals[0];
    assert_eq!(
        (huddle.n, huddle.tok_in, huddle.first_ms, huddle.last_ms),
        (3, 30, 10, 30)
    );
    let models: Vec<(&str, u64)> = huddle
        .models
        .iter()
        .map(|m| (m.model.as_str(), m.n))
        .collect();
    assert_eq!(models, [("a", 2), ("b", 1)]);

    let by_lane = history
        .totals_with_aliases(all(), GroupBy::Lane, &aliases)
        .expect("lane totals");
    assert_eq!(by_lane.len(), 1, "aliases touch only project keys");
}
