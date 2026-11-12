use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

fn repo() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn read(rel: &str) -> String {
    fs::read_to_string(repo().join(rel)).unwrap_or_else(|e| panic!("{rel}: {e}"))
}

fn h2(text: &str) -> Vec<String> {
    let mut code = false;
    let mut out = Vec::new();
    for line in text.lines() {
        if line.starts_with("```") {
            code = !code;
        } else if !code && let Some(title) = line.strip_prefix("## ") {
            out.push(title.trim().to_string());
        }
    }
    out
}

#[test]
fn k9_readme_sections_follow_the_approved_order() {
    let text = read("README.md");
    assert_eq!(
        h2(&text),
        [
            "Quick start",
            "Installation",
            "Providers",
            "Delegation",
            "Dashboard",
            "Commands",
            "How it works",
            "Troubleshooting",
            "Development",
            "Support",
            "License",
        ]
    );
    let main = Command::new("git")
        .args(["show", "main:README.md"])
        .current_dir(repo())
        .output()
        .expect("git show");
    assert!(main.status.success(), "git show main:README.md failed");
    let top = |s: &str| s.lines().take(16).collect::<Vec<_>>().join("\n");
    assert_eq!(top(&text), top(&String::from_utf8_lossy(&main.stdout)));
}

const OLD: &[&str] = &["-plugin-cc", "/zai:", "/<p>:", "zai:glm"];

fn assert_no_old_names(rel: &str, text: &str) {
    for old in OLD {
        assert!(!text.contains(old), "{rel} still names {old}");
    }
}

fn markdown_files(dir: &Path, out: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).expect("read dir").flatten() {
        let path = entry.path();
        if path.is_dir() {
            markdown_files(&path, out);
        } else if path.extension().is_some_and(|e| e == "md") {
            out.push(path);
        }
    }
}

#[test]
fn k9_no_doc_names_a_provider_plugin_or_an_old_command() {
    for rel in ["README.md", "AGENTS.md", "CLAUDE.md"] {
        assert_no_old_names(rel, &read(rel));
    }
    let mut files = Vec::new();
    for dir in ["docs", "plugin"] {
        markdown_files(&repo().join(dir), &mut files);
    }
    for file in files {
        let rel = file.strip_prefix(repo()).expect("under repo");
        assert_no_old_names(&rel.display().to_string(), &read(&rel.to_string_lossy()));
    }
    let changelog = read("CHANGELOG.md");
    let current = changelog.split("\n## 0.0.5").next().expect("split");
    assert_no_old_names("CHANGELOG.md (0.0.6)", current);
}

#[test]
fn k9_the_catalog_has_no_key_file() {
    let source = read("crates/huddle-catalog/src/providers.rs");
    assert!(!source.contains("key_file"), "the catalog names a key file");
    let tests = read("crates/huddle-catalog/tests/catalog.rs");
    assert!(
        !tests.contains("key_file"),
        "a catalog test names a key file"
    );
}
