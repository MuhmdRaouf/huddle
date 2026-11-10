use std::fs;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

/// Finds `crates/x/...rs:test_name` references in a document.
fn rust_refs(text: &str) -> Vec<(String, String)> {
    let mut found = Vec::new();
    for raw in
        text.split(|c: char| c.is_whitespace() || c == '|' || c == '`' || c == ',' || c == ')')
    {
        if let Some(rest) = raw.strip_prefix("crates/")
            && let Some((path, name)) = rest.split_once(".rs:")
        {
            let test = name
                .trim_matches(|c: char| !c.is_alphanumeric() && c != '_')
                .to_string();
            found.push((format!("crates/{path}.rs"), test));
        }
    }
    found
}

fn assert_refs_exist(doc: &str) {
    let root = repo_root();
    let text = fs::read_to_string(root.join(doc)).expect("read doc");
    let refs = rust_refs(&text);
    assert!(!refs.is_empty(), "{doc} names no Rust test");
    for (path, test) in refs {
        let source = fs::read_to_string(root.join(&path))
            .unwrap_or_else(|_| panic!("{doc}: missing {path}"));
        assert!(
            source.contains(&format!("fn {test}")),
            "{doc}: no test {test} in {path}"
        );
    }
}

#[test]
fn every_test_the_test_plan_names_exists() {
    assert_refs_exist("docs/TESTPLAN.md");
}

#[test]
fn every_guard_an_incident_names_exists() {
    assert_refs_exist("docs/INCIDENTS.md");
}

#[test]
fn every_relative_markdown_link_resolves() {
    let root = repo_root();
    let mut stack = vec![root.clone()];
    while let Some(dir) = stack.pop() {
        for entry in fs::read_dir(&dir).expect("read").flatten() {
            let path = entry.path();
            let name = entry.file_name().to_string_lossy().into_owned();
            if name == ".git" || name == "target" || name == "node_modules" || name == ".agents" {
                continue;
            }
            if path.is_dir() {
                stack.push(path);
            } else if name.ends_with(".md") {
                check_links(&path);
            }
        }
    }
}

fn check_links(file: &Path) {
    let text = fs::read_to_string(file).expect("read");
    let dir = file.parent().expect("parent");
    let mut rest = text.as_str();
    while let Some(at) = rest.find("](") {
        rest = &rest[at + 2..];
        let Some(end) = rest.find(')') else { break };
        let target = &rest[..end];
        rest = &rest[end..];
        if target.starts_with("http") || target.starts_with('#') || target.starts_with("mailto:") {
            continue;
        }
        let path = target.split('#').next().unwrap_or(target);
        if path.is_empty() {
            continue;
        }
        assert!(
            dir.join(path).exists(),
            "{}: broken link {target}",
            file.display()
        );
    }
}
