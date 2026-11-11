//! The dashboard page is one self-contained file: no framework, no build step,
//! no snapshot data ever parsed as markup.

const PAGE: &str = include_str!("../src/dashboard.html");

fn external_hosts(page: &str) -> Vec<String> {
    let mut hosts = Vec::new();
    for scheme in ["https://", "http://"] {
        for part in page.split(scheme).skip(1) {
            let host: String = part
                .chars()
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '.' || *c == '-' || *c == ':')
                .collect();
            hosts.push(host);
        }
    }
    hosts
}

#[test]
fn never_writes_markup_from_data() {
    assert!(!PAGE.contains("innerHTML"));
    assert!(!PAGE.contains("outerHTML"));
    assert!(!PAGE.contains("insertAdjacentHTML"));
    assert!(!PAGE.contains("document.write"));
}

#[test]
fn loads_no_external_script() {
    assert!(!PAGE.contains("<script src"));
    assert!(!PAGE.contains("<script type=\"module\" src"));
}

#[test]
fn only_font_hosts_are_external() {
    let allowed = ["fonts.googleapis.com", "fonts.gstatic.com", "www.w3.org"];
    for host in external_hosts(PAGE) {
        assert!(allowed.contains(&host.as_str()), "external host {host}");
    }
    assert!(PAGE.contains("fonts.googleapis.com"));
}

#[test]
fn listens_to_the_live_stream() {
    assert!(PAGE.contains("EventSource('/huddle-router/live')"));
    assert!(PAGE.contains("'snapshot'"));
}

#[test]
fn carries_the_logo_with_three_orbit_dots() {
    for cx in ["cx=\"15.29\"", "cx=\"56.43\"", "cx=\"23.11\""] {
        assert!(PAGE.contains(cx), "missing {cx}");
    }
}

#[test]
fn has_every_section_and_the_footer() {
    for text in [
        "Claude Code wiring",
        "Requests",
        "Errors",
        "Lanes",
        "Live requests",
        "In flight",
        "Delegated jobs",
        "No requests yet",
        "Memory only. Restarting the router clears this page.",
        "may be hung",
        "Reconnecting",
    ] {
        assert!(PAGE.contains(text), "missing {text}");
    }
}

#[test]
fn stays_small() {
    assert!(PAGE.len() < 40 * 1024, "{} bytes", PAGE.len());
}

#[test]
fn narrow_screens_cannot_widen_the_page() {
    assert!(PAGE.contains("name=\"viewport\""));
    assert!(PAGE.contains("minmax(min(100%,200px),1fr)"));
    assert!(PAGE.contains("minmax(min(100%,230px),1fr)"));
    assert!(PAGE.contains("@media (max-width:520px)"));
    assert!(PAGE.contains(".scroll{overflow-x:auto}"));
}
