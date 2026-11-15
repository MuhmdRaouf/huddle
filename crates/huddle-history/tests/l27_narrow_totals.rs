mod common;

use common::*;
use huddle_history::{GroupBy, Narrow, Row};
use huddle_testkit::temp_home::TempHome;

fn seeded(home: &TempHome) -> huddle_history::History {
    let mut history = open(home);
    let rows: Vec<Row> = (0..60u64)
        .map(|i| Row {
            session: Some(format!("s{}", i % 3)),
            project: Some(format!("p{}", i % 2)),
            tokens_in: Some(10),
            ..row(1_000 + i, "m")
        })
        .collect();
    history.insert(&rows).expect("insert");
    history
}

#[test]
fn l27_one_session_totals_scan_only_that_session() {
    let home = TempHome::new();
    let history = seeded(&home);
    let narrow = Narrow {
        sessions: vec!["s1".into()],
        projects: Vec::new(),
    };
    let one = history
        .totals_narrow(all(), GroupBy::Session, 0, &narrow)
        .expect("totals");
    let everyone = history.totals(all(), GroupBy::Session).expect("totals");
    let kept: Vec<_> = everyone.iter().filter(|t| t.key == "s1").cloned().collect();
    assert_eq!(one, kept, "the same row the unfiltered answer holds for s1");
    // Every row the query reads lands in a total, so the sums are the rows read.
    assert_eq!(
        one.iter().map(|t| t.n).sum::<u64>(),
        20,
        "only s1's 20 rows"
    );
}

#[test]
fn l27_one_project_totals_filter_in_sql() {
    let home = TempHome::new();
    let history = seeded(&home);
    let narrow = Narrow {
        sessions: Vec::new(),
        projects: vec!["p0".into()],
    };
    let one = history
        .totals_narrow(all(), GroupBy::Project, 0, &narrow)
        .expect("totals");
    assert_eq!(one.len(), 1);
    assert_eq!((one[0].key.as_str(), one[0].n), ("p0", 30));
    let none = history
        .totals_narrow(
            all(),
            GroupBy::Project,
            0,
            &Narrow {
                sessions: Vec::new(),
                projects: vec!["nope".into()],
            },
        )
        .expect("totals");
    assert!(none.is_empty());
}
