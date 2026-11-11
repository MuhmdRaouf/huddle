use std::fs;
use std::path::PathBuf;

fn plugin(rel: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../plugin")
        .join(rel);
    fs::read_to_string(path).expect("read plugin file")
}

#[test]
fn the_run_command_names_every_check_flag() {
    let text = plugin("commands/run.md");
    for flag in ["--setup", "--gate", "--scope", "--forbid"] {
        assert!(text.contains(flag), "{flag}: {text}");
    }
}

#[test]
fn the_skill_says_a_verdict_is_evidence_and_accept_reruns_the_gates() {
    let text = plugin("skills/delegate/SKILL.md");
    assert!(text.contains("A verdict is what huddle measured"), "{text}");
    assert!(
        text.contains("Accept re-runs the job's setup and gates in the folder"),
        "{text}"
    );
}
