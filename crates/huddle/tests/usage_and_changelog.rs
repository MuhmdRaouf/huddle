use std::fs;
use std::path::PathBuf;

fn read(rel: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join(rel);
    fs::read_to_string(path).expect("read")
}

#[test]
fn usage_names_the_setup_router_and_uninstall_words() {
    let text = read("docs/USAGE.md");
    for must in [
        "huddle setup --install",
        "huddle router status",
        "huddle uninstall",
        "HUDDLE_ROUTER_PORT",
    ] {
        assert!(text.contains(must), "USAGE lacks {must}");
    }
}

#[test]
fn the_changelog_has_a_section_for_the_version_in_version_file() {
    let version = read("VERSION");
    let header = format!("## {}", version.trim());
    let text = read("CHANGELOG.md");
    assert!(text.contains(&header), "CHANGELOG lacks {header}");
    assert!(
        text.contains("SHA256SUMS"),
        "CHANGELOG says how to verify a download"
    );
}
