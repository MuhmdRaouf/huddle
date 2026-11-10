use std::fs;
use std::path::PathBuf;

fn command_text() -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../plugin/commands/setup.md");
    fs::read_to_string(path).expect("plugin/commands/setup.md exists")
}

#[test]
fn setup_command_installs_then_reports_through_the_bootstrap() {
    let text = command_text();
    assert!(text.starts_with("---\ndescription: "), "{text}");
    assert!(text.contains("\nallowed-tools: Bash(sh:*)\n"));
    assert!(text.contains("\nargument-hint: \"[harness]\"\n"));
    assert!(text.contains("!`sh \"${CLAUDE_PLUGIN_ROOT}/dist/run\" setup --install 2>&1`"));
    assert!(text.contains("!`sh \"${CLAUDE_PLUGIN_ROOT}/dist/run\" setup $ARGUMENTS 2>&1`"));
    assert!(text.contains("restart"), "says when to restart");
}
