use huddle_testkit::temp_home::TempHome;
use std::{
    fs,
    path::{Path, PathBuf},
};

const PROVIDERS: &[&str] = &["zai", "kimi", "deepseek", "minimax", "qwen"];
const WORDS: &[&str] = &["setup", "remove", "usage"];

fn repo() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn command_text(provider: &str, word: &str) -> String {
    let path = repo().join(format!("plugin/commands/{provider}/{word}.md"));
    fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

fn front_matter(text: &str) -> &str {
    text.strip_prefix("---\n")
        .and_then(|rest| rest.split_once("\n---\n"))
        .expect("front matter")
        .0
}

fn markdown_files(dir: &Path, into: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).expect("read dir") {
        let path = entry.expect("entry").path();
        if path.is_dir() {
            markdown_files(&path, into);
        } else if path.extension().and_then(|e| e.to_str()) == Some("md") {
            into.push(path);
        }
    }
}

#[test]
fn k1_every_provider_has_setup_remove_and_usage_commands_in_the_huddle_plugin() {
    for provider in PROVIDERS {
        let dir = repo().join(format!("plugin/commands/{provider}"));
        let mut have: Vec<String> = fs::read_dir(&dir)
            .unwrap_or_else(|e| panic!("{}: {e}", dir.display()))
            .map(|e| e.expect("entry").file_name().to_string_lossy().into_owned())
            .collect();
        have.sort();
        assert_eq!(have, ["remove.md", "setup.md", "usage.md"], "{provider}");
        for word in WORDS {
            let text = command_text(provider, word);
            let head = front_matter(&text);
            assert!(head.contains("description:"), "{provider} {word}");
            assert!(
                head.contains("allowed-tools: Bash(sh:*)"),
                "{provider} {word}"
            );
            assert!(
                text.contains(&format!(
                    "sh \"${{CLAUDE_PLUGIN_ROOT}}/dist/run\" provider {provider} {word}"
                )),
                "{provider} {word} must call its word"
            );
            assert!(!text.contains("argument-hint"), "{provider} {word}");
            assert!(!text.contains("$ARGUMENTS"), "{provider} {word}");
        }
    }
}

#[test]
fn k1_every_plugin_root_mention_is_the_quoted_launcher() {
    let mut files = Vec::new();
    markdown_files(&repo().join("plugin"), &mut files);
    for file in files {
        let text = fs::read_to_string(&file).expect("read");
        for (at, _) in text.match_indices("${CLAUDE_PLUGIN_ROOT}") {
            let around = &text[at.saturating_sub(1)
                ..(at + "${CLAUDE_PLUGIN_ROOT}/dist/run\"".len()).min(text.len())];
            assert_eq!(
                around,
                "\"${CLAUDE_PLUGIN_ROOT}/dist/run\"",
                "{}",
                file.display()
            );
        }
    }
}

#[test]
fn k1_no_plugin_file_names_a_provider_command_without_the_huddle_prefix() {
    let mut files = Vec::new();
    markdown_files(&repo().join("plugin"), &mut files);
    for file in files {
        let text = fs::read_to_string(&file).expect("read");
        for provider in PROVIDERS {
            for word in WORDS {
                let bare = format!("/{provider}:{word}");
                for (at, _) in text.match_indices(&bare) {
                    assert!(
                        text[..at].ends_with("/huddle:") || text[..at].ends_with("huddle"),
                        "{}: {bare}",
                        file.display()
                    );
                }
            }
        }
    }
}

#[test]
fn k1_the_marketplace_lists_only_huddle() {
    let text = fs::read_to_string(repo().join(".claude-plugin/marketplace.json")).expect("read");
    let doc: serde_json::Value = serde_json::from_str(&text).expect("json");
    let names: Vec<&str> = doc["plugins"]
        .as_array()
        .expect("plugins")
        .iter()
        .map(|p| p["name"].as_str().expect("name"))
        .collect();
    assert_eq!(names, ["huddle"]);
}

#[test]
fn k1_no_plugins_folder_remains() {
    assert!(!repo().join("plugins").exists());
}

#[test]
fn t12_dist_run_ignores_a_relative_data_home() {
    use std::os::unix::fs::PermissionsExt;
    let home = TempHome::new();
    let bin_dir = home.root().join(".local/share/huddle/bin");
    fs::create_dir_all(&bin_dir).expect("bin dir");
    let fake = bin_dir.join("huddle");
    fs::write(&fake, "#!/bin/sh\necho \"ran $*\"\n").expect("fake huddle");
    fs::set_permissions(&fake, fs::Permissions::from_mode(0o755)).expect("chmod");
    let out = home
        .command("sh")
        .arg(repo().join("plugin/dist/run"))
        .arg("status")
        .env_remove("HUDDLE_DATA_HOME")
        .env("XDG_DATA_HOME", "relative-data")
        .env("HOME", home.root())
        .output()
        .expect("run dist/run");
    assert_eq!(
        String::from_utf8_lossy(&out.stdout),
        "ran status\n",
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
}

#[test]
fn t1_dist_run_names_the_installed_binary_in_the_data_folder() {
    let script = fs::read_to_string(repo().join("plugin/dist/run")).expect("plugin dist/run");
    for part in [
        "HUDDLE_DATA_HOME",
        "XDG_DATA_HOME",
        "$HOME/.local/share",
        "bin_dir=$data_home/bin",
    ] {
        assert!(script.contains(part), "{part}");
    }
}
