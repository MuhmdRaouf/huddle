use huddle_jobs::git::ChangeSet;
use huddle_jobs::model::*;
use huddle_jobs::render::*;

const NOW: u64 = 1_800_000_000_000;

fn iso_minus(minutes: u64) -> String {
    huddle_jobs::id::iso_ms(NOW - minutes * 60_000)
}

fn job(state: JobState) -> Job {
    Job {
        id: "261109-k3m9qx".into(),
        harness: "opencode".into(),
        title: "Rename foo to bar".into(),
        task: "Rename foo to bar".into(),
        access: Access::Write,
        effort: None,
        state,
        repo_root: "/repo".into(),
        base_sha: "0123456789012345678901234567890123456789".into(),
        base_branch: Some("main".into()),
        dir: "/repo".into(),
        start_tree: None,
        dirty_at_start: false,
        attempts: vec![],
        failure: None,
        decision: None,
        created_at: iso_minus(3),
        updated_at: iso_minus(1),
        version: 4,
    }
}

fn attempt(n: u32, outcome: Option<AttemptOutcome>) -> Attempt {
    Attempt {
        n,
        kind: if n == 1 {
            AttemptKind::Initial
        } else {
            AttemptKind::ReviewFix
        },
        prompt: "p".into(),
        feedback: None,
        session_id: None,
        started_at: "t".into(),
        ended_at: None,
        outcome,
        report: None,
        report_text: None,
    }
}

#[test]
fn ages_are_rounded_to_a_unit_and_never_negative() {
    assert_eq!(ago(NOW, &huddle_jobs::id::iso_ms(NOW)), "just now");
    assert_eq!(ago(NOW, &huddle_jobs::id::iso_ms(NOW + 5_000)), "just now");
    assert_eq!(ago(NOW, &iso_minus(1)), "1m ago");
    assert_eq!(ago(NOW, &iso_minus(90)), "1h ago");
    assert_eq!(ago(NOW, &iso_minus(60 * 48)), "2d ago");
}

#[test]
fn durations_truncate_tenths_and_never_show_ten_point_zero() {
    assert_eq!(duration_text(0), "0.0s");
    assert_eq!(duration_text(9_999), "9.9s");
    assert_eq!(duration_text(10_000), "10s");
    assert_eq!(duration_text(59_000), "59s");
    assert_eq!(duration_text(125_000), "2m05s");
    assert_eq!(duration_text(3_723_000), "1h02m");
}

#[test]
fn one_line_collapses_and_cuts_by_characters() {
    assert_eq!(
        one_line("Now I will\n  update  the tests", 160),
        "Now I will update the tests"
    );
    assert_eq!(one_line("abc", 2), "a…");
    assert_eq!(one_line("abc", 0), "");
    assert_eq!(one_line("héllo", 10), "héllo");
}

#[test]
fn outcome_texts_say_how_the_harness_ended() {
    assert_eq!(outcome_text(&AttemptOutcome::Completed), "completed");
    assert_eq!(
        outcome_text(&AttemptOutcome::Crashed {
            exit_code: Some(1),
            signal: None,
            stderr_tail: "boom\n at x".into()
        }),
        "crashed (exit 1): boom at x"
    );
    assert_eq!(
        outcome_text(&AttemptOutcome::Crashed {
            exit_code: None,
            signal: Some("SIGKILL".into()),
            stderr_tail: String::new()
        }),
        "killed by SIGKILL"
    );
    assert_eq!(
        outcome_text(&AttemptOutcome::Error {
            message: "worker died".into()
        }),
        "failed: worker died"
    );
    assert_eq!(outcome_text(&AttemptOutcome::Timeout), "timed out");
    assert_eq!(outcome_text(&AttemptOutcome::Stopped), "stopped");
}

#[test]
fn an_empty_repo_prints_the_one_line_hint() {
    assert_eq!(
        status_table(&[], NOW),
        "No huddle jobs in this repository yet. /huddle:run <harness> <task> starts one."
    );
}

#[test]
fn the_status_table_has_a_row_per_job_with_a_failed_reason() {
    let mut a = job(JobState::Running);
    a.title =
        "A very long title that goes past the sixty character limit of the task column".into();
    let mut b = job(JobState::Failed);
    b.id = "261109-aaaaaa".into();
    b.failure = Some("worker died".into());
    let table = status_table(&[a, b], NOW);
    let lines: Vec<&str> = table.lines().collect();
    assert_eq!(
        lines[0],
        "ID             HARNESS   STATE                 ACCESS  AGE     TASK"
    );
    assert_eq!(
        lines[1],
        "261109-k3m9qx  opencode  running               write   3m ago  A very long title that goes past the sixty character limit …"
    );
    assert_eq!(
        lines[2],
        "261109-aaaaaa  opencode  failed (worker died)  write   3m ago  Rename foo to bar"
    );
}

#[test]
fn job_detail_shows_attempts_log_and_the_next_command() {
    let mut j = job(JobState::Running);
    j.attempts.push(attempt(1, None));
    let text = job_detail(&j, &["line one".to_string(), "line two".to_string()], NOW);
    let want = [
        "huddle job 261109-k3m9qx: Rename foo to bar",
        "running · opencode · write · created 3m ago · updated 1m ago",
        "folder /repo (branch main, base 0123456)",
        "Attempts:",
        "  #1 (initial): running",
        "Log (last 4 lines):",
        "  line one",
        "  line two",
        "Cancel: /huddle:cancel 261109-k3m9qx",
    ]
    .join("\n");
    assert_eq!(text, want);
}

#[test]
fn job_detail_of_a_new_job_has_no_attempts_and_no_log() {
    let mut j = job(JobState::AwaitingReview);
    j.attempts.push(attempt(1, Some(AttemptOutcome::Completed)));
    let text = job_detail(&j, &[], NOW);
    assert!(text.contains("  #1 (initial): completed"));
    assert!(text.ends_with("Result: /huddle:result 261109-k3m9qx"));
    let empty = job_detail(&job(JobState::Queued), &[], NOW);
    assert!(empty.contains("Attempts: none yet"));
}

#[test]
fn the_result_packet_shows_the_report_changes_diff_and_next_step() {
    let mut j = job(JobState::AwaitingReview);
    let mut a = attempt(1, Some(AttemptOutcome::Completed));
    a.report = Some(Report {
        summary: "Renamed foo".into(),
        files: vec![FileNote {
            path: "src/a.rs".into(),
            why: "rename".into(),
        }],
        open_items: vec![],
    });
    j.attempts.push(a);
    let view = ReviewView {
        changes: ChangeSet {
            added: vec!["new.rs".into()],
            modified: vec!["src/a.rs".into()],
            deleted: vec![],
            untracked: vec![],
        },
        diff_stat: Some(" src/a.rs | 2 +-\n 1 file changed".into()),
        diff: Some("+bar\n-foo\n".into()),
    };
    let text = result_packet(&j, &view);
    let want = [
        "huddle job 261109-k3m9qx: Rename foo to bar",
        "awaiting review · opencode · write · attempt 1 (initial)",
        "folder /repo (branch main, base 0123456)",
        "",
        "Report",
        "  Renamed foo",
        "  Files:",
        "  - src/a.rs: rename",
        "  Open items: none",
        "",
        "Changes (2):",
        "  A new.rs",
        "  M src/a.rs",
        "",
        "Diff stat",
        "   src/a.rs | 2 +-",
        "   1 file changed",
        "",
        "Diff",
        "```diff",
        "+bar",
        "-foo",
        "```",
        "",
        "Next",
        "  /huddle:result 261109-k3m9qx --diff reads the diff, then --accept lands it or --return \"<feedback>\" sends it back.",
    ]
    .join("\n");
    assert_eq!(text, want);
}

#[test]
fn a_failed_job_shows_why_and_a_free_text_report() {
    let mut j = job(JobState::Failed);
    j.failure = Some("worker died".into());
    let mut a = attempt(
        1,
        Some(AttemptOutcome::Error {
            message: "worker died".into(),
        }),
    );
    a.report_text = Some("plain words\nsecond line".into());
    j.attempts.push(a);
    let text = result_packet(
        &j,
        &ReviewView {
            changes: ChangeSet::default(),
            diff_stat: None,
            diff: None,
        },
    );
    assert!(
        text.contains("failed (worker died) · opencode · write · attempt 1 (initial)"),
        "{text}"
    );
    assert!(
        text.contains("\n\nHarness: failed: worker died\n"),
        "{text}"
    );
    assert!(
        text.contains("\n\nReport (free text)\n  plain words\n  second line\n"),
        "{text}"
    );
    assert!(text.contains("\n\nChanges: none\n"), "{text}");
    assert!(text.contains("the harness never finished (worker died): nothing to accept. /huddle:result 261109-k3m9qx --return \"<feedback>\" runs it again in the same folder, or /huddle:cancel 261109-k3m9qx drops it."), "{text}");
}

#[test]
fn next_steps_cover_every_state_and_grammar_uses_an_for_exec() {
    let mut exec = job(JobState::Completed);
    exec.access = Access::Exec;
    assert_eq!(
        next_step(&exec),
        "nothing left to do: the job finished (an exec job: nothing to merge)."
    );
    let mut ro = job(JobState::Completed);
    ro.access = Access::Readonly;
    assert_eq!(
        next_step(&ro),
        "nothing left to do: the job finished (a readonly job: nothing to merge)."
    );
    assert_eq!(
        next_step(&job(JobState::Accepted)),
        "nothing left to do: the job was accepted."
    );
    assert_eq!(
        next_step(&job(JobState::Cancelled)),
        "nothing left to do: the job was cancelled."
    );
    assert_eq!(
        next_step(&job(JobState::Running)),
        "it is running; /huddle:status 261109-k3m9qx shows it, /huddle:cancel 261109-k3m9qx stops it."
    );
}

#[test]
fn a_result_with_more_than_fifty_changes_is_cut() {
    let mut j = job(JobState::AwaitingReview);
    j.attempts.push(attempt(1, Some(AttemptOutcome::Completed)));
    let modified: Vec<String> = (0..55).map(|n| format!("f{n}.rs")).collect();
    let text = result_packet(
        &j,
        &ReviewView {
            changes: ChangeSet {
                modified,
                ..ChangeSet::default()
            },
            diff_stat: None,
            diff: None,
        },
    );
    assert!(text.contains("Changes (55):"));
    assert!(text.contains("  and 5 more"));
    assert_eq!(text.matches("  M f").count(), 50);
}

#[test]
fn a_diff_longer_than_the_cap_is_cut_with_a_note() {
    assert_eq!(cap_diff("abcdef", 10), "abcdef");
    assert_eq!(
        cap_diff("abcdefghij", 4),
        "abcd\n[diff truncated: 6 more characters]\n"
    );
}

#[test]
fn no_renderer_writes_an_escape_sequence() {
    let text = status_table(&[job(JobState::Running)], NOW);
    assert!(!text.contains('\u{1b}'));
}

#[test]
fn t3_the_folder_line_says_when_the_folder_was_dirty_at_start() {
    let mut job = job(JobState::AwaitingReview);
    job.dirty_at_start = true;
    let text = job_detail(&job, &[], NOW);
    assert!(
        text.contains("it had uncommitted changes at start, so the diff includes them"),
        "{text}"
    );
}
