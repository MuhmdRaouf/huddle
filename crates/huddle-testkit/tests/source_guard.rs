use std::fs;
use std::path::{Path, PathBuf};

const LIVE_PORTS: &[&str] = &["4835"];

// A live port is only a problem where a test dials or binds it; in an assertion's expected text it
// is data.
const DIAL_WORDS: &[&str] = &[
    "connect(",
    "bind(",
    "listen(",
    "TcpStream",
    "TcpListener",
    "HUDDLE_ROUTER_PORT",
];

const FORBIDDEN: &[(&str, &str)] = &[
    ("env::set_var", "process-wide environment changes"),
    ("env::remove_var", "process-wide environment changes"),
    ("env::vars(", "the owner's whole environment"),
    ("env::vars_os(", "the owner's whole environment"),
];

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .unwrap()
        .to_path_buf()
}

fn rust_files(dir: &Path, found: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            if path.file_name().is_some_and(|name| name == "target") {
                continue;
            }
            rust_files(&path, found);
        } else if path.extension().is_some_and(|ext| ext == "rs") {
            found.push(path);
        }
    }
}

// The lines of `text` that are test code: all of a file under tests/, the rest of a file from
// its `#[cfg(test)]` line on.
fn test_lines<'a>(path: &Path, text: &'a str) -> Vec<(usize, &'a str)> {
    let in_tests_dir = path.components().any(|part| part.as_os_str() == "tests");
    let start = if in_tests_dir {
        0
    } else {
        match text.lines().position(|line| line.trim() == "#[cfg(test)]") {
            Some(at) => at,
            None => return Vec::new(),
        }
    };
    text.lines()
        .enumerate()
        .skip(start)
        .map(|(at, line)| (at + 1, line))
        .collect()
}

fn violations(path: &Path, text: &str) -> Vec<String> {
    let lines: Vec<&str> = text.lines().collect();
    let mut found = Vec::new();
    for (number, line) in test_lines(path, text) {
        let excused = line.contains("guard-allow:")
            || number >= 2 && lines[number - 2].contains("guard-allow:");
        if excused {
            continue;
        }
        let mut flag = |needle: &str, why: &str| {
            found.push(format!(
                "{}:{number}: {needle} ({why}); use the test kit, or add a `// guard-allow: <reason>` comment on this or the line above",
                path.display()
            ));
        };
        for (needle, why) in FORBIDDEN {
            if line.contains(needle) {
                flag(needle, why);
            }
        }
        let dials = DIAL_WORDS.iter().any(|word| line.contains(word));
        for port in LIVE_PORTS {
            if dials && line.contains(port) {
                flag(port, "a live port this test would reach");
            }
        }
    }
    found
}

#[test]
fn no_test_reaches_live_ports_or_the_process_environment() {
    let mut files = Vec::new();
    rust_files(&repo_root().join("crates"), &mut files);
    let testkit = repo_root().join("crates/huddle-testkit");
    let mut problems = Vec::new();
    for path in files.iter().filter(|path| !path.starts_with(&testkit)) {
        let text = fs::read_to_string(path).unwrap();
        problems.extend(violations(path, &text));
    }
    assert!(problems.is_empty(), "{}", problems.join("\n"));
}

#[test]
fn the_scanner_sees_a_live_port_in_a_test_and_honours_an_allow_comment() {
    let test = Path::new("crates/x/tests/a.rs");
    assert_eq!(
        violations(test, "let s = TcpStream::connect(\"127.0.0.1:4835\");\n").len(),
        1
    );
    assert_eq!(
        violations(test, "cmd.env(\"HUDDLE_ROUTER_PORT\", \"4835\");\n").len(),
        1
    );
    assert!(
        violations(
            test,
            "// guard-allow: subject\nTcpStream::connect(\"127.0.0.1:4835\");\n"
        )
        .is_empty()
    );
    assert!(violations(test, "assert_eq!(url, \"http://127.0.0.1:4835\");\n").is_empty());
    let src = Path::new("crates/x/src/a.rs");
    assert!(violations(src, "pub const P: u16 = 4835;\n").is_empty());
    assert_eq!(
        violations(src, "#[cfg(test)]\nfn t() { bind(\"127.0.0.1:4835\"); }\n").len(),
        1
    );
}
