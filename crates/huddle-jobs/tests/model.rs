use huddle_jobs::model::*;

const STATES: [JobState; 7] = [
    JobState::Queued,
    JobState::Running,
    JobState::AwaitingReview,
    JobState::Completed,
    JobState::Failed,
    JobState::Accepted,
    JobState::Cancelled,
];
const EVENTS: [JobEvent; 6] = [
    JobEvent::Started,
    JobEvent::Finished,
    JobEvent::Failed,
    JobEvent::Returned,
    JobEvent::Accepted,
    JobEvent::Cancelled,
];

fn expected(state: JobState, access: Access, event: JobEvent) -> Option<JobState> {
    use JobEvent as E;
    use JobState as S;
    match (state, event) {
        (S::Queued, E::Started) => Some(S::Running),
        (S::Queued, E::Failed) => Some(S::Failed),
        (S::Queued, E::Cancelled) => Some(S::Cancelled),
        (S::Running, E::Finished) => Some(if access == Access::Write {
            S::AwaitingReview
        } else {
            S::Completed
        }),
        (S::Running, E::Failed) => Some(S::Failed),
        (S::Running, E::Cancelled) => Some(S::Cancelled),
        (S::AwaitingReview, E::Returned) => Some(S::Queued),
        (S::AwaitingReview, E::Accepted) => Some(S::Accepted),
        (S::AwaitingReview, E::Cancelled) => Some(S::Cancelled),
        (S::Failed, E::Returned) => Some(S::Queued),
        (S::Failed, E::Cancelled) => Some(S::Cancelled),
        _ => None,
    }
}

#[test]
fn the_transition_table_is_total() {
    for state in STATES {
        for access in [Access::Readonly, Access::Exec, Access::Write] {
            for event in EVENTS {
                let got = next_state(state, access, event);
                match expected(state, access, event) {
                    Some(to) => assert_eq!(got, Ok(to), "{state:?} {access:?} {event:?}"),
                    None => assert_eq!(
                        got,
                        Err(IllegalTransition { from: state, event }),
                        "{state:?} {event:?}"
                    ),
                }
            }
        }
    }
}

#[test]
fn terminal_states_refuse_every_event() {
    for state in [JobState::Completed, JobState::Accepted, JobState::Cancelled] {
        assert!(state.is_terminal());
        for event in EVENTS {
            assert!(next_state(state, Access::Write, event).is_err());
        }
    }
    for state in [
        JobState::Queued,
        JobState::Running,
        JobState::AwaitingReview,
        JobState::Failed,
    ] {
        assert!(!state.is_terminal());
    }
}

#[test]
fn an_illegal_move_says_what_happened() {
    let err = next_state(JobState::Accepted, Access::Write, JobEvent::Returned).unwrap_err();
    assert_eq!(
        err.to_string(),
        "the job was already accepted, so it cannot be returned"
    );
}

#[test]
fn every_state_has_its_own_words() {
    let words = [
        "is queued",
        "is running",
        "awaits review",
        "has completed",
        "has failed",
        "was already accepted",
        "was already cancelled",
    ];
    for (state, word) in STATES.iter().zip(words) {
        assert_eq!(state.words(), word);
    }
    assert_eq!(JobState::AwaitingReview.label(), "awaiting review");
    assert_eq!(JobState::AwaitingReview.as_str(), "awaiting_review");
}

#[test]
fn wrong_state_text_names_what_is_allowed() {
    let text = wrong_state_text(
        "261109-k3m9qx",
        JobState::Accepted,
        &[JobState::AwaitingReview, JobState::Failed],
    );
    assert_eq!(
        text,
        "job 261109-k3m9qx was already accepted; this only works on a job that awaits review or has failed"
    );
}

fn sample_job() -> Job {
    Job {
        id: "261109-k3m9qx".into(),
        harness: "opencode".into(),
        title: "Rename foo".into(),
        task: "Rename foo to bar".into(),
        access: Access::Write,
        effort: None,
        state: JobState::Queued,
        repo_root: "/repo".into(),
        base_sha: "0123456789012345678901234567890123456789".into(),
        base_branch: Some("main".into()),
        dir: "/repo".into(),
        start_tree: None,
        dirty_at_start: false,
        attempts: vec![],
        failure: None,
        decision: None,
        created_at: "2026-11-09T10:00:00.000Z".into(),
        updated_at: "2026-11-09T10:00:00.000Z".into(),
        version: 0,
    }
}

#[test]
fn t3_job_toml_keeps_key_order_and_omits_unset_optionals() {
    let job = sample_job();
    let text = toml::to_string(&job).unwrap();
    assert!(
        text.starts_with("id = \"261109-k3m9qx\"\nharness = \"opencode\""),
        "{text}"
    );
    for unset in [
        "effort",
        "failure",
        "decision",
        "startTree",
        "dirtyAtStart",
        "branch",
    ] {
        assert!(!text.contains(unset), "{unset} is written: {text}");
    }
    let order = [
        "id =",
        "harness =",
        "title =",
        "task =",
        "access =",
        "state =",
        "repoRoot =",
        "baseSha =",
        "baseBranch =",
        "dir =",
        "attempts =",
        "createdAt =",
        "updatedAt =",
        "version =",
    ];
    let mut last = 0;
    for key in order {
        let at = text.find(key).unwrap();
        assert!(at >= last, "{key} out of order");
        last = at;
    }
    let back: Job = toml::from_str(&text).unwrap();
    assert_eq!(back, job);
}

#[test]
fn an_attempt_keeps_its_outcome_and_report() {
    let attempt = Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: Some("s-1".into()),
        started_at: "t0".into(),
        ended_at: Some("t1".into()),
        outcome: Some(AttemptOutcome::Crashed {
            exit_code: Some(1),
            signal: None,
            stderr_tail: "boom".into(),
        }),
        report: Some(Report {
            summary: "s".into(),
            files: vec![FileNote {
                path: "a".into(),
                why: "w".into(),
            }],
            open_items: vec![],
        }),
        report_text: None,
    };
    let text = toml::to_string(&attempt).unwrap();
    assert!(text.contains("kind = \"crashed\""), "{text}");
    assert!(text.contains("exitCode = 1"), "{text}");
    assert!(!text.contains("signal"), "{text}");
    let back: Attempt = toml::from_str(&text).unwrap();
    assert_eq!(back, attempt);
}

#[test]
fn t3_an_outcome_with_no_exit_code_or_signal_survives_a_toml_round_trip() {
    let mut job = sample_job();
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "t0".into(),
        ended_at: Some("t1".into()),
        outcome: Some(AttemptOutcome::Crashed {
            exit_code: None,
            signal: None,
            stderr_tail: "boom".into(),
        }),
        report: None,
        report_text: None,
    });
    let back: Job = toml::from_str(&toml::to_string(&job).unwrap()).unwrap();
    assert_eq!(back, job);
}

#[test]
fn an_unknown_outcome_kind_does_not_parse() {
    assert!(toml::from_str::<AttemptOutcome>("kind = \"mystery\"").is_err());
    assert!(toml::from_str::<AttemptOutcome>("kind = \"crashed\"").is_err());
}

#[test]
fn task_title_is_the_first_non_blank_line_collapsed_and_cut() {
    assert_eq!(
        task_title("\n  Rename   foo \t to bar\nmore"),
        "Rename foo to bar"
    );
    assert_eq!(task_title("   \n\t"), "(no title)");
    let long: String = "a".repeat(100);
    let title = task_title(&long);
    assert_eq!(title.chars().count(), 72);
    assert!(title.ends_with('…'));
}

#[test]
fn pending_attempt_is_the_last_one_with_no_outcome() {
    let mut job = sample_job();
    assert_eq!(job.pending_attempt(), None);
    job.attempts.push(Attempt {
        n: 1,
        kind: AttemptKind::Initial,
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "t".into(),
        ended_at: None,
        outcome: Some(AttemptOutcome::Completed),
        report: None,
        report_text: None,
    });
    assert_eq!(job.pending_attempt(), None);
    job.attempts.push(Attempt {
        n: 2,
        kind: AttemptKind::ReviewFix,
        prompt: "p".into(),
        feedback: Some("f".into()),
        session_id: None,
        started_at: "t".into(),
        ended_at: None,
        outcome: None,
        report: None,
        report_text: None,
    });
    assert_eq!(job.pending_attempt(), Some(1));
}
