//! The dashboard page is one self-contained file: no framework, no build step, nothing loaded
//! from outside the router, and no snapshot data ever parsed as markup outside one helper.

use serde_json::Value;

const PAGE: &str = include_str!("../src/dashboard.html");
const FIXTURE: &str = include_str!("fixtures/state-v2.json");

const PAGE_IDS: [&str; 14] = [
    "overview",
    "sessions",
    "agents",
    "requests",
    "errors",
    "models",
    "providers",
    "usage",
    "projects",
    "worktrees",
    "jobs",
    "plugins",
    "harnesses",
    "router",
];

/// Every object key in the value, except the model names that key `buckets.models`.
fn contract_keys(value: &Value, under_models: bool, keys: &mut Vec<String>) {
    match value {
        Value::Object(map) => {
            for (key, child) in map {
                if !under_models {
                    keys.push(key.clone());
                }
                contract_keys(
                    child,
                    key == "models" && !under_models && is_bucket_map(child),
                    keys,
                );
            }
        }
        Value::Array(items) => items
            .iter()
            .for_each(|item| contract_keys(item, false, keys)),
        _ => {}
    }
}

/// `buckets.models` maps a model name to an array; `models` and `rows` elsewhere are arrays.
fn is_bucket_map(value: &Value) -> bool {
    value.is_object()
}

#[test]
fn loads_nothing_from_outside_the_router() {
    assert!(!PAGE.contains("http://"), "http:// in the page");
    assert!(!PAGE.contains("https://"), "https:// in the page");
    assert!(!PAGE.contains("<script src"));
    assert!(!PAGE.contains("<link"));
    assert!(!PAGE.contains("@import"));
    assert!(!PAGE.contains("Geist"));
    assert!(
        PAGE.contains("-apple-system, BlinkMacSystemFont")
            || PAGE.contains("-apple-system,BlinkMacSystemFont")
    );
}

#[test]
fn writes_markup_in_one_helper_that_the_data_cannot_reach_unescaped() {
    assert_eq!(
        PAGE.matches("innerHTML").count(),
        1,
        "innerHTML outside setHtml"
    );
    assert!(PAGE.contains("function setHtml(el,html){el.innerHTML=html}"));
    for word in ["outerHTML", "insertAdjacentHTML", "document.write"] {
        assert!(!PAGE.contains(word), "{word}");
    }
    for entity in ["&amp;", "&lt;", "&gt;", "&quot;", "&#39;"] {
        assert!(PAGE.contains(entity), "esc misses {entity}");
    }
}

#[test]
fn carries_all_fourteen_pages() {
    for id in PAGE_IDS {
        assert!(PAGE.contains(&format!("['{id}','")), "nav entry {id}");
        assert!(PAGE.contains(&format!("PAGES.{id}={{")), "page {id}");
    }
    for group in ["Monitor", "Usage", "Workspace", "System"] {
        assert!(PAGE.contains(&format!("['{group}',[")), "group {group}");
    }
}

#[test]
fn reads_every_key_of_the_state_contract() {
    let state: Value = serde_json::from_str(FIXTURE).expect("fixture parses");
    let mut keys = Vec::new();
    contract_keys(&state, false, &mut keys);
    keys.sort();
    keys.dedup();
    for key in keys {
        let read = PAGE.contains(&format!(".{key}")) || PAGE.contains(&format!("'{key}'"));
        assert!(read, "the page never reads {key}");
    }
}

#[test]
fn follows_the_live_feed_and_falls_back_to_polling() {
    assert!(PAGE.contains("EventSource('/huddle-router/live')"));
    assert!(PAGE.contains("'snapshot'"));
    assert!(PAGE.contains("fetch('/huddle-router/state'"));
    assert!(PAGE.contains("setInterval(getState,2000)"));
}

#[test]
fn shows_the_unknown_states_and_never_invents_data() {
    for text in [
        "Checking…",
        "Reconnecting",
        "Memory only. Restarting the router clears this page.",
    ] {
        assert!(PAGE.contains(text), "missing {text}");
    }
    for word in ["Math.random", "seed", "sample"] {
        assert!(!PAGE.contains(word), "leftover {word}");
    }
}

#[test]
fn hides_the_plan_tiles_without_plan_data() {
    assert!(PAGE.contains("if(PLAN){"));
    assert!(PAGE.contains("PLAN?`<section>"));
}

#[test]
fn carries_the_logo_with_three_orbit_dots() {
    for cx in ["cx=\"15.29\"", "cx=\"56.43\"", "cx=\"23.11\""] {
        assert!(PAGE.contains(cx), "missing {cx}");
    }
}

#[test]
fn keeps_the_admin_shell() {
    for id in [
        "id=\"drawer\"",
        "id=\"pal\"",
        "id=\"scrim\"",
        "id=\"rcard\"",
        "data-theme-set=\"dark\"",
        "name=\"viewport\"",
    ] {
        assert!(PAGE.contains(id), "missing {id}");
    }
    assert!(PAGE.contains("localStorage.setItem('huddle-admin-theme',t)"));
    assert!(PAGE.contains("@media (max-width:600px)"));
}

#[test]
fn stays_under_the_size_limit() {
    assert!(PAGE.len() < 140 * 1024, "{} bytes", PAGE.len());
}
