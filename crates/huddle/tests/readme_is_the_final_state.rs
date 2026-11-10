use std::fs;
use std::path::PathBuf;

fn read(rel: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join(rel);
    fs::read_to_string(path).expect("read")
}

const RETIRED: &[&str] = &[
    "dashboard",
    "channel",
    "passkey",
    "Sign in",
    "sign in",
    "phone",
    "kanban",
    "Kanban",
    "gateway",
    "the hub",
    "bun ",
    "Playwright",
];

/// Words of the earlier stack and of the work that replaced it.
const EARLIER_STACK: &[&str] = &[
    "golang",
    "go.mod",
    "go.sum",
    "gofmt",
    "staticcheck",
    "cmd/huddle",
    "internal/",
    "shared knowledge",
    "web/",
    "Rust huddle",
    "rewrit",
    "parity",
    "frozen",
];

const HEADINGS: &[&str] = &[
    "What it does",
    "Architecture",
    "How it works",
    "Quick start",
    "Layout",
    "Automation",
    "Development",
    "Versioning",
    "Support",
    "License",
];

const SUB_HEADINGS: &[&str] = &[
    "Routing",
    "Providers",
    "Delegation",
    "Setup and recovery",
    "`ci.yml`",
    "`release.yml`",
];

fn assert_clean(rel: &str) {
    let text = read(rel);
    for word in RETIRED {
        assert!(!text.contains(word), "{rel} still says {word:?}");
    }
}

/// True when `word` stands alone: no letter, digit or underscore on either side.
fn has_word(text: &str, word: &str) -> bool {
    let alone = |c: Option<char>| c.is_none_or(|c| !(c.is_alphanumeric() || c == '_'));
    text.match_indices(word).any(|(at, _)| {
        alone(text[..at].chars().next_back()) && alone(text[at + word.len()..].chars().next())
    })
}

/// True when the text names a board card: `RS-` followed by a capital letter and a digit.
fn names_a_card(text: &str) -> bool {
    text.match_indices("RS-").any(|(at, _)| {
        let mut rest = text[at + 3..].chars();
        matches!(
            (rest.next(), rest.next()),
            (Some(a), Some(b)) if a.is_ascii_uppercase() && b.is_ascii_digit()
        )
    })
}

fn headings(text: &str, marker: &str) -> Vec<String> {
    let mut code = false;
    let mut out = Vec::new();
    for line in text.lines() {
        if line.starts_with("```") {
            code = !code;
        } else if !code && let Some(title) = line.strip_prefix(marker) {
            out.push(title.trim().to_string());
        }
    }
    out
}

#[test]
fn the_readme_holds_the_house_sections_in_order() {
    let text = read("README.md");
    assert_eq!(headings(&text, "## "), HEADINGS);
    let subs = headings(&text, "### ");
    for sub in SUB_HEADINGS {
        assert!(subs.iter().any(|s| s == sub), "README lacks ### {sub}");
    }
}

#[test]
fn the_readme_opens_with_the_banner_and_the_sponsor_button() {
    let text = read("README.md");
    assert!(
        text.trim_start()
            .starts_with("<p align=\"center\">\n  <img src=\"assets/banner.svg\""),
        "README does not open with the banner"
    );
    assert!(text.contains("https://github.com/sponsors/MuhmdRaouf"));
    assert!(text.contains("Built with ❤️ for the Claude Code community."));
}

#[test]
fn the_readme_describes_the_install_flow_and_the_two_jobs() {
    let text = read("README.md");
    for must in [
        "/plugin install huddle@muhmdraouf",
        "/huddle:setup",
        "Restart Claude Code once",
        "/huddle:run",
        "/huddle:result",
        "huddle uninstall",
    ] {
        assert!(text.contains(must), "README lacks {must}");
    }
    assert_clean("README.md");
}

#[test]
fn the_readme_names_no_earlier_stack() {
    let text = read("README.md");
    let lower = text.to_lowercase();
    for word in EARLIER_STACK {
        assert!(
            !lower.contains(&word.to_lowercase()),
            "README still says {word:?}"
        );
    }
    for word in ["go", "hub", "bun"] {
        assert!(!has_word(&lower, word), "README still says {word:?}");
    }
    assert!(!names_a_card(&text), "README names a card");
}

#[test]
fn the_plugin_readme_is_the_final_state_too() {
    assert_clean("plugin/README.md");
    assert!(read("plugin/README.md").lines().count() <= 60);
}
