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

#[test]
fn q4_a_sub_millisecond_router_hop_reads_under_one_ms() {
    assert!(PAGE.contains("r.rt==null?'—':r.rt<1?'&lt;1 ms':r.rt+' ms'"));
}

#[test]
fn t8_fetches_history_only_for_a_range_longer_than_the_live_one() {
    assert!(PAGE.contains("/huddle-router/history?view=buckets&range="));
    assert!(PAGE.contains("/huddle-router/history?view=totals&by=model&range="));
    assert!(PAGE.contains("function loadHistory(){if(liveRange())return"));
    assert!(PAGE.contains(
        "setInterval(()=>{if(document.visibilityState==='visible')loadHistory()},60000)"
    ));
    for label in ["15 min", "24 h", "7 d", "30 d"] {
        assert!(PAGE.contains(&format!("'{label}'")), "range {label}");
    }
    for page in ["overview", "models", "usage"] {
        let start = PAGE.find(&format!("PAGES.{page}={{")).expect("page");
        let body = &PAGE[start..start + 700];
        assert!(body.contains("rangeSeg()"), "{page} lacks the range switch");
    }
}

#[test]
fn t8_labels_claude_cost_as_api_equivalent_and_dates_the_prices() {
    assert!(PAGE.contains("API-equivalent"));
    assert!(
        PAGE.contains(
            "Claude plan usage is flat-rate; this is what the same tokens cost on the API"
        )
    );
    assert!(PAGE.contains("prices_retrieved"));
    assert!(PAGE.contains("claude_prices_retrieved"));
}

#[test]
fn t8_names_the_real_file_layout_and_no_agents_folder() {
    assert!(!PAGE.contains("~/.agents"), "~/.agents in the page");
    assert!(!PAGE.contains("events/"), "events/ in the page");
    for path in [
        "~/.config/huddle/providers.toml",
        "~/.config/huddle/harnesses.toml",
        "~/.config/huddle/config.toml",
        "~/.local/share/huddle/bin/",
        "~/.local/share/huddle/ca/huddle-ca.pem",
        "~/.local/state/huddle/huddle.db",
        "~/.local/state/huddle/router.log",
        "~/.local/state/huddle/run/",
        "~/.local/state/huddle/settings-ledger.toml",
        "~/.local/state/huddle/jobs/",
    ] {
        assert!(PAGE.contains(path), "missing {path}");
    }
}

#[test]
fn t8_remembers_the_range_and_shows_a_failed_fetch_without_dropping_data() {
    assert!(PAGE.contains("localStorage.setItem('huddle-range'"));
    assert!(PAGE.contains("localStorage.getItem('huddle-range')"));
    assert!(PAGE.contains("Could not load history. Showing the last data."));
}

#[test]
fn t9_fetches_30_day_totals_by_session_and_project_and_a_trend_per_sheet() {
    assert!(PAGE.contains("/huddle-router/history?view="));
    assert!(PAGE.contains("totals&by=session&range=30d"));
    assert!(PAGE.contains("totals&by=project&range=30d"));
    assert!(PAGE.contains("buckets&range=30d&'+q.k+'='+encodeURIComponent(q.id)"));
    assert!(PAGE.contains("function loadTotals(){if(!wantTotals())return"));
    assert!(
        PAGE.contains(
            "setInterval(()=>{if(document.visibilityState==='visible')loadTotals()},60000)"
        )
    );
    assert_eq!(
        PAGE.matches("fetch(u,{cache:'no-store'})").count(),
        1,
        "one shared fetch helper"
    );
}

#[test]
fn t9_tables_carry_the_three_30_day_columns() {
    for label in ["Requests (30 d)", "Tokens (30 d)", "Cost (30 d)"] {
        assert_eq!(PAGE.matches(label).count(), 1, "{label} defined once");
    }
    assert!(
        PAGE.contains("const SESS_COLS=[") && PAGE.contains("...histCols(s=>hOf(HT.ses,s.id))")
    );
    assert!(PAGE.contains("...histCols(p=>hOf(HT.prj,p.name))"));
}

#[test]
fn t9_session_and_project_sheets_carry_a_history_block() {
    for sheet in [" session(id){", " project(name){"] {
        let start = PAGE.find(sheet).expect("sheet");
        let end = start + PAGE[start..].find("\n request(").unwrap_or(9000).min(9000);
        assert!(PAGE[start..end].contains("histBlock("), "{sheet}");
    }
    assert!(PAGE.contains("function histBlock("));
    for part in [
        "Models used",
        "Cache hit rate",
        "First seen",
        "Last seen",
        "last 30 days",
        "No requests in the last 30 d",
    ] {
        assert!(PAGE.contains(part), "history block lacks {part}");
    }
    assert!(PAGE.contains("spark(histSeries("));
}

#[test]
fn t9_shows_loading_and_failed_states_and_reuses_the_cost_label() {
    assert!(PAGE.contains("HT.err"));
    assert!(
        PAGE.matches("Could not load history. Showing the last data.")
            .count()
            >= 1
    );
    assert!(PAGE.contains("t.api_equivalent?' '+apiq()"));
    assert_eq!(PAGE.matches("const FLAT=").count(), 1);
}

#[test]
fn t10_requests_and_errors_search_the_history_with_every_filter_and_paging() {
    assert!(PAGE.contains("const HPAGE=100"));
    assert!(PAGE.contains("HFIL=['model','lane','session','agent','project','status']"));
    assert!(PAGE.contains("HQ+'search&range='"));
    assert!(PAGE.contains("'&limit='+HPAGE+'&offset='"));
    assert!(PAGE.contains("encodeURIComponent("));
    assert!(PAGE.contains("data-hf="), "filter inputs");
    assert!(
        PAGE.contains("HRANGES=[...RANGES.filter(r=>r[0]!=='15m'),['90d','90 d']]"),
        "search offers 24 h to 90 d"
    );
    for status in ["'any'", "'ok'", "'error'", "'left'"] {
        assert!(PAGE.contains(status), "status {status}");
    }
}

#[test]
fn t10_errors_default_to_status_error_and_the_hash_holds_the_filters() {
    assert!(PAGE.contains("status:p==='errors'?'error':'any'"));
    assert!(PAGE.contains("split('?')[0]"), "route ignores the query");
    assert!(PAGE.contains("mode=history"));
    assert!(PAGE.contains("history.replaceState(null,'','#'+page+'?'+"));
    assert!(PAGE.contains("new URLSearchParams("));
}

#[test]
fn t10_fetches_on_filter_change_debounced_and_on_page_change_only() {
    assert!(PAGE.contains("setTimeout(hsearch,300)"));
    assert!(PAGE.contains("data-hp=\"prev\"") && PAGE.contains("data-hp=\"next\""));
    let start = PAGE.find("function hsearch(").expect("hsearch");
    let body = &PAGE[start..start + 700];
    assert!(!body.contains("setInterval"), "no polling in history mode");
    assert_eq!(PAGE.matches("setInterval(").count(), 4, "no new timer");
}

#[test]
fn t10_history_rows_carry_cost_and_open_the_request_sheet() {
    assert!(PAGE.contains("label:'Cost'"));
    assert!(PAGE.contains("hseg('hmode'") || PAGE.contains("seg('hmode'"));
    assert!(PAGE.contains("['live','Live'],['history','History']"));
    assert!(PAGE.contains("HS.rows.find("), "sheet finds search rows");
    assert!(PAGE.contains("Could not load history. Showing the last data."));
    assert!(PAGE.contains("No requests match these filters"));
    assert!(PAGE.contains("${HS.off+1}–${HS.off+n}"), "range text");
}
