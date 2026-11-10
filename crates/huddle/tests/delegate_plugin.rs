use std::fs;
use std::path::PathBuf;

fn plugin(rel: &str) -> String {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    fs::read_to_string(root.join("plugin").join(rel)).unwrap_or_else(|e| panic!("{rel}: {e}"))
}

fn front_matter(text: &str) -> &str {
    let rest = text.strip_prefix("---\n").expect("front matter");
    let end = rest.find("\n---\n").expect("closing fence");
    &rest[..end]
}

#[test]
fn run_tells_claude_to_run_the_word_through_dist_run() {
    let text = plugin("commands/run.md");
    let fm = front_matter(&text);
    assert!(fm.contains("allowed-tools: Bash(sh:*)"), "{fm}");
    assert!(
        !fm.contains("disable-model-invocation"),
        "Claude invokes run itself"
    );
    assert!(
        text.contains(r#"sh "${CLAUDE_PLUGIN_ROOT}/dist/run" run <harness>"#),
        "{text}"
    );
}

#[test]
fn status_result_and_cancel_are_user_only_and_run_their_word() {
    for (file, word) in [
        ("commands/status.md", "status"),
        ("commands/result.md", "result"),
        ("commands/cancel.md", "cancel"),
    ] {
        let text = plugin(file);
        let fm = front_matter(&text);
        assert!(
            fm.contains("disable-model-invocation: true"),
            "{file}: {fm}"
        );
        assert!(fm.contains("allowed-tools: Bash(sh:*)"), "{file}: {fm}");
        let call = format!(r#"!`sh "${{CLAUDE_PLUGIN_ROOT}}/dist/run" {word} $ARGUMENTS 2>&1`"#);
        assert!(text.contains(&call), "{file} must run its word: {text}");
    }
}

#[test]
fn status_and_result_hand_back_verbatim() {
    assert!(plugin("commands/status.md").contains("Do not summarize or condense it."));
    assert!(plugin("commands/result.md").contains("Do not summarize or condense it."));
}

#[test]
fn the_delegate_agent_is_a_thin_verbatim_forwarder() {
    let text = plugin("agents/delegate.md");
    let fm = front_matter(&text);
    assert!(fm.contains("name: delegate"));
    assert!(fm.contains("tools: Bash"));
    assert!(text.contains("exactly as it is"), "{text}");
    assert!(text.contains("Never turn a failed delegation into your own attempt."));
}

#[test]
fn the_delegate_skill_says_how_to_accept_or_return() {
    let text = plugin("skills/delegate/SKILL.md");
    let fm = front_matter(&text);
    assert!(fm.contains("name: delegate"));
    assert!(text.contains("result <id> --accept"));
    assert!(text.contains("result <id> --return"));
    assert!(text.contains("Never turn a failed or incomplete job into your own implementation"));
}
