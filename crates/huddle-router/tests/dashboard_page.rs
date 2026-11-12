//! The dashboard page is one self-contained file: no framework, no build step, nothing loaded
//! from outside the router, and no snapshot data ever parsed as markup outside one helper.

use serde_json::Value;

const PAGE: &str = include_str!("../src/dashboard.html");
const FIXTURE: &str = include_str!("fixtures/state-v2.json");

const PAGE_IDS: [&str; 13] = [
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
    assert!(PAGE.contains("function setHtml(el,html){el.innerHTML=html;"));
    for word in ["outerHTML", "insertAdjacentHTML", "document.write"] {
        assert!(!PAGE.contains(word), "{word}");
    }
    for entity in ["&amp;", "&lt;", "&gt;", "&quot;", "&#39;"] {
        assert!(PAGE.contains(entity), "esc misses {entity}");
    }
}

#[test]
fn carries_all_thirteen_pages() {
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
        "$XDG_RUNTIME_DIR/huddle/",
        "~/.cache/huddle/",
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

#[test]
fn k11_sidebar_badges_read_as_ratios_with_the_good_part_green() {
    let providers = PAGE
        .lines()
        .find(|l| l.contains("case 'providers':"))
        .expect("providers badge");
    assert!(providers.contains("/${"), "{providers}");
    assert!(providers.contains("class=\"good\""), "{providers}");
    assert!(!providers.contains("badge warn"), "{providers}");
    let harnesses = PAGE
        .lines()
        .find(|l| l.contains("case 'harnesses':"))
        .expect("harnesses badge");
    assert!(harnesses.contains("HARN.length"), "{harnesses}");
    assert!(harnesses.contains("class=\"good\""), "{harnesses}");
    assert!(PAGE.contains(".badge .good{color:var(--ok)}"));
    assert!(harnesses.contains("/${HARN.length}"), "{harnesses}");
}

#[test]
fn k11_the_providers_table_has_no_installed_column() {
    assert!(!PAGE.contains("label:'Installed'"));
    assert!(!PAGE.contains("<b>Installed</b>"));
    assert!(PAGE.contains("label:'Provider'"));
    assert!(PAGE.contains("label:'Status'"));
}

fn providers_page() -> &'static str {
    let start = PAGE.find("PAGES.providers={").expect("providers page");
    let end = PAGE.find("PAGES.usage={").expect("usage page");
    &PAGE[start..end]
}

#[test]
fn k13_one_providers_page_and_no_plugins_page() {
    assert!(!PAGE.contains("['plugins','"), "nav entry");
    assert!(!PAGE.contains("PAGES.plugins"), "page");
    assert!(PAGE.contains("['providers','Providers']"));
    assert!(
        PAGE.contains("h==='plugins'?'providers'"),
        "old links open Providers"
    );
    assert_eq!(PAGE_IDS.len(), 13);
}

#[test]
fn k13_not_set_up_is_neutral_never_red() {
    assert!(PAGE.contains("function provStatus("));
    let start = PAGE.find("function provStatus(").expect("status");
    let body = &PAGE[start..start + 700];
    assert!(body.contains("['off','Not set up']"), "{body}");
    assert!(body.contains("['bad','Needs a fix']"), "{body}");
    assert!(body.contains("['ok','Working']"), "{body}");
    assert!(body.contains("['ok','Built in']"), "{body}");
    assert!(!PAGE.contains("['bad','Missing']"), "no red Missing pill");
}

#[test]
fn k13_the_next_step_is_the_huddle_setup_command() {
    let page = providers_page();
    assert!(page.contains("label:'Next step'"), "column");
    assert!(page.contains("cmd(x.next)"), "command chip");
    assert!(FIXTURE.contains("/huddle:kimi:setup"));
    assert!(page.contains(">1st token</span>"));
    assert!(page.contains("label:'Requests'"));
    assert!(page.contains("label:'Errors'"));
    assert!(
        page.contains("row:x=>'provider:'+x.id"),
        "row opens the drawer"
    );
    assert!(PAGE.contains("provider(id){"), "provider drawer");
}

#[test]
fn k13_no_plugin_cc_text_and_no_marketplace_chip() {
    assert!(!PAGE.contains("-plugin-cc"));
    assert!(!PAGE.contains("/plugin marketplace update"));
    assert!(PAGE.contains("Where your model requests go, and what each provider still needs."));
    assert!(
        PAGE.contains("kpis k3"),
        "three tiles fit a three-column grid"
    );
}

#[test]
fn k13_the_providers_badge_reads_set_up_of_five() {
    let line = PAGE
        .lines()
        .find(|l| l.contains("case 'providers':"))
        .expect("providers badge");
    assert!(line.contains("set_up===true"), "{line}");
    assert!(line.contains("class=\"good\""), "{line}");
    assert!(line.contains("/${"), "{line}");
}

/// The text of the page between two markers.
fn page_between(from: &str, to: &str) -> &'static str {
    let start = PAGE.find(from).unwrap_or_else(|| panic!("{from}"));
    let end = start + PAGE[start..].find(to).unwrap_or_else(|| panic!("{to}"));
    &PAGE[start..end]
}

#[test]
fn k14_filter_counts_redraw_with_each_snapshot() {
    for (page, next, region) in [
        ("PAGES.sessions={", "const AG_COLS", "reg('sess-f'"),
        ("PAGES.agents={", "PAGES.requests={", "reg('agents-f'"),
        ("PAGES.worktrees={", "PAGES.jobs={", "reg('wt-f'"),
        ("PAGES.harnesses={", "PAGES.router={", "reg('harn-f'"),
    ] {
        let body = page_between(page, next);
        assert!(body.contains(region), "{page} counts sit in {region}");
    }
}

#[test]
fn k14_memory_footer_only_on_live_pages() {
    assert!(PAGE.contains("const readsMemory=()=>"));
    assert!(PAGE.contains("(readsMemory()?`<div class=\"foot\">"));
    let rule = page_between("const readsMemory=()=>", ";\n");
    for live in [
        "overview:liveRange()",
        "sessions:true",
        "agents:true",
        "requests:HS.mode==='live'",
        "errors:HS.mode==='live'",
    ] {
        assert!(rule.contains(live), "{live}");
    }
    for disk in [
        "providers",
        "harnesses",
        "router",
        "jobs",
        "projects",
        "worktrees",
    ] {
        assert!(!rule.contains(disk), "{disk} reads disk or settings");
    }
}

#[test]
fn k14_optional_providers_fold_into_one_quiet_row() {
    let items = page_between("function attnItems()", "const unsetProviders");
    assert!(items.contains("x.status[1]==='Needs a fix'"));
    assert!(
        !items.contains("is not set up"),
        "an unset provider is no item"
    );
    let list = page_between("function attnList()", "function overviewKpis");
    assert!(list.contains("not set up (optional)"));
    assert!(list.contains("href=\"#providers\""));
    assert!(list.contains("Nothing needs you."));
    let quiet = list.find("const quiet=").expect("quiet row");
    let rows = list.find("const rows=").expect("real rows");
    assert!(rows < quiet && list.rfind("+quiet").is_some_and(|i| i > quiet));
}

#[test]
fn k14_needs_you_comes_before_the_kpis() {
    let overview = page_between("PAGES.overview={", "after:()=>{bindChart()");
    let needs = overview.find("secH('Needs you'").expect("needs you");
    let kpis = overview.find("reg('kpis'").expect("kpis");
    let chart = overview.find("secH('Traffic'").expect("chart");
    assert!(needs < kpis && needs < chart);
    let header = overview.find("head('Overview'").expect("header");
    assert!(header < needs);
}

/// Relative luminance of `#rrggbb` per WCAG 2.
fn luminance(hex: &str) -> f64 {
    let channel = |i: usize| {
        let c = f64::from(u8::from_str_radix(&hex[1 + 2 * i..3 + 2 * i], 16).expect("hex")) / 255.0;
        if c <= 0.03928 {
            c / 12.92
        } else {
            ((c + 0.055) / 1.055).powf(2.4)
        }
    };
    0.2126 * channel(0) + 0.7152 * channel(1) + 0.0722 * channel(2)
}

fn contrast(a: &str, b: &str) -> f64 {
    let (x, y) = (luminance(a), luminance(b));
    (x.max(y) + 0.05) / (x.min(y) + 0.05)
}

/// The value of a custom property in the light `:root` block.
fn light_token(name: &str) -> &'static str {
    let root = page_between(":root{", "}");
    let at = root
        .find(&format!("{name}:"))
        .unwrap_or_else(|| panic!("{name}"))
        + name.len()
        + 1;
    &root[at..at + 7]
}

#[test]
fn k14_light_theme_faint_and_accent_meet_4_5_to_1() {
    let (page, card) = (light_token("--page"), light_token("--surface"));
    let faint = light_token("--faint");
    assert!(contrast(faint, page) >= 4.5, "faint on the page");
    assert!(contrast(faint, card) >= 4.5, "faint on a card");
    let accent = light_token("--accent");
    assert!(
        contrast(accent, light_token("--accent-bg")) >= 4.5,
        "accent on its tint"
    );
    assert!(contrast(accent, card) >= 4.5, "accent on a card");
}

#[test]
fn k15_sortable_headers_are_buttons_with_aria_sort() {
    assert!(PAGE.contains("<button type=\"button\" class=\"sortbtn\" data-sort="));
    assert!(PAGE.contains("aria-sort=\"${on?(st.d>0?'ascending':'descending'):'none'}\""));
    assert!(!PAGE.contains("<th class=\"${c.num?'num ':''}${c.c?'c ':''}${c.sm?'hide-sm ':''}${c.v?'sortable':''}\"${c.v?` data-sort"));
}

#[test]
fn k15_rows_open_with_enter_and_the_drawer_traps_and_returns_focus() {
    assert!(PAGE.contains("` tabindex=\"0\" data-open=\"${esc(o)}\"`"));
    assert!(PAGE.contains("e.target.matches('tr[data-open]')"));
    assert!(PAGE.contains("e.key==='Enter'||e.key===' '"));
    assert!(PAGE.contains("role=\"dialog\" aria-modal=\"true\""));
    assert!(
        PAGE.contains("$('#drawer').focus("),
        "focus moves into the drawer"
    );
    assert!(
        PAGE.contains("function trapTab("),
        "Tab stays in the drawer"
    );
    assert!(
        PAGE.contains("if(back)back.focus("),
        "focus returns to the row"
    );
    assert!(PAGE.contains("if(e.key==='Escape'){if(DR)closeDrawer()"));
}

#[test]
fn k15_redraw_skips_unchanged_regions() {
    let refresh = page_between("function refresh(){", "\nconst head=");
    assert!(refresh.contains("patch(el,fn())"), "{refresh}");
    assert!(!refresh.contains("setHtml(el,fn())"), "{refresh}");
    assert!(PAGE.contains("if(LASTHTML.get(el)===html)return false;"));
    assert!(
        PAGE.contains("n.focus({preventScroll:true})"),
        "focus comes back"
    );
    assert!(PAGE.contains("setSelectionRange("), "selection comes back");
}

#[test]
fn k15_one_name_for_time_to_first_token() {
    assert!(!PAGE.contains("TTFT"), "no TTFT in the page");
    assert!(!PAGE.contains(">First token"), "one name for one idea");
    assert!(PAGE.contains("title=\"Time to first token\">1st token"));
    let harnesses = page_between("PAGES.harnesses=", "const wirePill");
    for word in ["Flags</b>", "Access</b>", "Report</b>"] {
        assert!(harnesses.contains(word), "legend for {word}");
    }
    assert!(!PAGE.contains("decrypts"), "plain words on the Router page");
    assert!(PAGE.contains("checks the traffic to Anthropic"));
}

#[test]
fn k15_no_inline_styles_left() {
    assert_eq!(PAGE.matches("style=\"").count(), 0);
    assert!(PAGE.contains("n.style.cssText=n.dataset.css"));
}

#[test]
fn k15_phone_command_chips_wrap() {
    let phone = page_between(
        "@media (max-width:600px){",
        "@media (prefers-reduced-motion",
    );
    assert!(phone.contains(".cmd{white-space:normal"), "{phone}");
}

#[test]
fn k18_a_failing_path_is_the_first_needs_you_item() {
    let state: Value = serde_json::from_str(FIXTURE).expect("fixture parses");
    assert_eq!(state["router"]["path"]["state"], "failing");
    let items = page_between("function attnItems(){", "const unsetProviders");
    let anthropic = items.find("Anthropic is not answering:").expect("the item");
    let first_other = items.find("SESS.filter").expect("session items");
    assert!(anthropic < first_other, "the failing path leads the list");
    assert!(items.contains("requests failed)"));
    assert!(items.contains("cmd('huddle router log')"));
    assert!(PAGE.contains("Anthropic recovered at "));
    assert!(
        PAGE.contains("30*MIN"),
        "the recovery line lasts 30 minutes"
    );
}

#[test]
fn k18_the_router_page_lists_recent_events() {
    let state: Value = serde_json::from_str(FIXTURE).expect("fixture parses");
    assert!(
        state["router"]["events"]
            .as_array()
            .is_some_and(|e| !e.is_empty())
    );
    let router = page_between("PAGES.router={", "/* ---------- drawers");
    assert!(router.contains("secH('Recent events'"));
    assert!(router.contains("No events since the router started."));
    assert!(router.contains("RT.events"));
}

#[test]
fn k18_command_chips_break_only_after_a_colon() {
    let cmd = page_between("const cmd=c=>", "\n");
    assert!(cmd.contains("<wbr>"), "{cmd}");
    let phone = page_between(
        "@media (max-width:600px){",
        "@media (prefers-reduced-motion",
    );
    let chip = phone
        .lines()
        .find(|l| l.contains(".cmd{"))
        .expect("phone chip rule");
    assert!(!chip.contains("anywhere"), "{chip}");
    assert!(chip.contains("overflow-wrap:normal"), "{chip}");
    assert!(
        phone.contains(".stword{"),
        "the Status cell has a short phone form"
    );
}
