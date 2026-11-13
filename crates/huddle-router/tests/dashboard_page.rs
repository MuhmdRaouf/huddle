//! The dashboard page is one self-contained page, joined from the files in `src/dashboard/`: no framework, no build step, nothing loaded
//! from outside the router, and no snapshot data ever parsed as markup outside one helper.

use serde_json::Value;

use huddle_router::dashboard::PAGE;
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

/// Every object key in the value, except the model names that key `buckets.models` and the
/// provider ids that key `providers`.
fn contract_keys(value: &Value, under_models: bool, keys: &mut Vec<String>) {
    match value {
        Value::Object(map) => {
            for (key, child) in map {
                if !under_models {
                    keys.push(key.clone());
                }
                contract_keys(
                    child,
                    !under_models
                        && ((key == "models" && is_bucket_map(child)) || key == "providers"),
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
    // Plugin rows carry `set_up` for the CLI; the page derives a provider's status from live facts.
    let unread = ["set_up"];
    for key in keys.iter().filter(|key| !unread.contains(&key.as_str())) {
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
    assert!(PAGE.len() < 200 * 1024, "{} bytes", PAGE.len());
}

#[test]
fn q4_a_sub_millisecond_router_hop_reads_under_one_ms() {
    assert!(PAGE.contains("r.rt==null?'—':r.rt<1?'&lt;1 ms':r.rt+' ms'"));
}

#[test]
fn t8_fetches_history_only_for_a_range_longer_than_the_live_one() {
    assert!(PAGE.contains("/huddle-router/history?view=buckets&'+rangeQuery()"));
    assert!(PAGE.contains("/huddle-router/history?view=totals&by=model&'+rangeQuery()"));
    assert!(PAGE.contains("function loadHistory(){if(liveRange())return"));
    assert!(PAGE.contains(
        "setInterval(()=>{if(document.visibilityState==='visible')loadHistory()},60000)"
    ));
}

#[test]
fn t8_labels_claude_cost_as_api_equivalent_and_dates_the_prices() {
    assert!(PAGE.contains("API price"));
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
    assert!(PAGE.contains("totals&by=session&'+rangeQuery()"));
    assert!(PAGE.contains("totals&by=project&'+rangeQuery()"));
    assert!(PAGE.contains("buckets&'+rangeQuery()+'&'+q.k+'='+encodeURIComponent(q.id)"));
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
    for label in [
        "Requests (${rangeShort()})",
        "Tokens (${rangeShort()})",
        "API price (${rangeShort()})",
    ] {
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
        let end =
            (start + PAGE[start..].find("\n request(").unwrap_or(9000).min(9000)).min(PAGE.len());
        assert!(PAGE[start..end].contains("histBlock("), "{sheet}");
    }
    assert!(PAGE.contains("function histBlock("));
    for part in [
        "Models used",
        "Cache hit rate",
        "First seen",
        "Last seen",
        "${rangeText()}",
        "No requests in ${rangeText()}",
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
    assert!(PAGE.contains("const costCell=t=>costText(t.cost_usd,t.priced)"));
    assert_eq!(PAGE.matches("const FLAT=").count(), 1);
}

#[test]
fn t10_requests_and_errors_search_the_history_with_every_filter_and_paging() {
    assert!(PAGE.contains("const HPAGE=100"));
    assert!(PAGE.contains("HFIL=['model','lane','session','agent','project','status']"));
    assert!(PAGE.contains("HQ+'search&'+rangeQuery()"));
    assert!(PAGE.contains("'&limit='+HPAGE+'&offset='"));
    assert!(PAGE.contains("encodeURIComponent("));
    assert!(PAGE.contains("data-hf="), "filter inputs");
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
    assert!(!PAGE.contains("['bad','Missing']"), "no red Missing pill");
}

#[test]
fn k13_the_next_step_is_the_huddle_setup_command() {
    let page = providers_page();
    assert!(page.contains("label:'Status and next step'"), "column");
    assert!(page.contains("cmd(x.next)"), "command chip");
    assert!(FIXTURE.contains("/huddle:kimi:setup"));
    assert!(page.contains("label:'Time to first token'"));
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
    assert!(line.contains("provSetUp()"), "{line}");
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
        "errors:liveRange()",
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
    let anthropic = items.find("Anthropic is not answering").expect("the item");
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

#[test]
fn b4_phone_tables_stack_their_cells() {
    let phone = page_between(
        "@media (max-width:600px){",
        "@media (prefers-reduced-motion",
    );
    for rule in [
        "table.fold,table.fold tbody,table.fold tr,table.fold td{display:block",
        "table.fold thead{display:none}",
        "table.fold td[data-label]::before{content:attr(data-label)",
        "table.fold td.hide-sm{display:block}",
    ] {
        assert!(phone.contains(rule), "missing phone rule {rule}\n{phone}");
    }
    let table = page_between("function table(", "\n\n");
    assert!(table.contains("data-label=\"${esc(c.label)}\""), "{table}");
    assert!(table.contains("opt.stack?' class=\"fold\"':''"), "{table}");
    let harn = page_between("PAGES.harnesses=", "const wirePill");
    assert!(harn.contains("stack:1"), "the Harnesses table stacks");
    let overview = page_between("PAGES.overview=", "PAGES.requests");
    assert!(overview.contains("stack:1"), "the Overview table stacks");
}

const PARTS: [&str; 19] = [
    include_str!("../src/dashboard/style.css"),
    include_str!("../src/dashboard/index.html"),
    include_str!("../src/dashboard/core.js"),
    include_str!("../src/dashboard/pages/needs-you.js"),
    include_str!("../src/dashboard/pages/overview.js"),
    include_str!("../src/dashboard/pages/sessions.js"),
    include_str!("../src/dashboard/pages/agents.js"),
    include_str!("../src/dashboard/pages/requests.js"),
    include_str!("../src/dashboard/pages/errors.js"),
    include_str!("../src/dashboard/pages/models.js"),
    include_str!("../src/dashboard/pages/providers.js"),
    include_str!("../src/dashboard/pages/usage.js"),
    include_str!("../src/dashboard/pages/projects.js"),
    include_str!("../src/dashboard/pages/worktrees.js"),
    include_str!("../src/dashboard/pages/jobs.js"),
    include_str!("../src/dashboard/pages/harnesses.js"),
    include_str!("../src/dashboard/pages/router.js"),
    include_str!("../src/dashboard/drawers.js"),
    include_str!("../src/dashboard/boot.js"),
];

#[test]
fn d0_the_served_page_is_the_joined_parts() {
    let mut at = 0;
    for (i, part) in PARTS.iter().enumerate() {
        assert_eq!(PAGE.matches(part).count(), 1, "part {i} appears once");
        let found = PAGE[at..].find(part);
        assert!(found.is_some(), "part {i} comes after the one before it");
        at += found.unwrap_or(0) + part.len();
    }
    let old = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src/dashboard.html");
    assert!(!old.exists(), "no leftover dashboard.html");
}

fn fixture() -> Value {
    serde_json::from_str(FIXTURE).expect("fixture parses")
}

fn badge_line(page: &str) -> &'static str {
    PAGE.lines()
        .find(|l| l.contains(&format!("case '{page}':")))
        .unwrap_or_else(|| panic!("{page} badge"))
}

#[test]
fn d6_live_and_history_split_tokens_the_same() {
    assert!(PAGE.contains("const tokSplit="), "one token helper");
    for site in [
        "function buildRequests",
        "function buildModels",
        "function histRows",
        "function overviewKpis",
    ] {
        let start = PAGE.find(site).unwrap_or_else(|| panic!("{site}"));
        let body = &PAGE[start..PAGE.len().min(start + 2500)];
        assert!(
            body.contains("tokSplit("),
            "{site} must split tokens with tokSplit"
        );
    }
    assert!(
        !PAGE.contains("tin:n0(x.tokens_in)+n0(x.cache_read)+n0(x.cache_write)"),
        "tokens_in already holds the cache tokens"
    );
    assert!(!PAGE.contains("fresh:n0(x.tokens_in)"));
    let state = fixture();
    let rows = state["recent"]
        .as_array()
        .expect("recent")
        .iter()
        .chain(state["models"].as_array().expect("models"));
    for row in rows {
        let total = row["tokens_in"].as_u64().unwrap_or(0);
        let cached =
            row["cache_read"].as_u64().unwrap_or(0) + row["cache_write"].as_u64().unwrap_or(0);
        assert!(
            total >= cached,
            "tokens_in holds the cache tokens in the router's real shape: {row}"
        );
    }
}

#[test]
fn d6_rates_are_per_second() {
    assert!(PAGE.contains("const rate="), "one rate formatter");
    assert!(
        PAGE.contains("SNAP.per_second"),
        "the page reads the router's per_second"
    );
    let requests = badge_line("requests");
    assert!(
        requests.contains("per_second") || requests.contains("PS"),
        "{requests}"
    );
    assert!(requests.contains("/s"), "{requests}");
    assert!(!requests.contains("/m<"), "{requests}");
    assert!(!PAGE.contains("small:'/min'"));
    assert!(!PAGE.contains("per minute"));
    let state = fixture();
    assert_eq!(
        state["per_second"].as_array().expect("per_second").len(),
        60
    );
}

#[test]
fn d6_agent_rows_count_their_requests() {
    assert!(
        PAGE.contains("r.s.id+':'+a.id===r.agentId"),
        "match the router's <session>:<agent> id"
    );
    let state = fixture();
    let sessions = state["sessions"].as_array().expect("sessions");
    for row in state["recent"].as_array().expect("recent") {
        let Some(agent) = row["agent"].as_str() else {
            continue;
        };
        let matched = sessions.iter().any(|s| {
            s["agents"].as_array().is_some_and(|agents| {
                agents.iter().any(|a| {
                    format!(
                        "{}:{}",
                        s["id"].as_str().unwrap_or(""),
                        a["id"].as_str().unwrap_or("")
                    ) == agent
                })
            })
        });
        assert!(matched, "recent agent {agent} is <session>:<agent id>");
    }
}

#[test]
fn d6_each_badge_counts_its_page() {
    for page in [
        "sessions",
        "agents",
        "requests",
        "errors",
        "models",
        "providers",
        "projects",
        "worktrees",
        "jobs",
        "harnesses",
    ] {
        assert!(
            badge_line(page).contains("bdg("),
            "{page} badge goes through the titled helper"
        );
    }
    assert!(
        PAGE.contains("title=\"${title}\""),
        "the helper writes the title"
    );
    assert!(badge_line("sessions").contains("sessShown().length"));
    assert!(badge_line("agents").contains("AGENTS.length"));
    assert!(!badge_line("agents").contains("type!=='main'"));
    assert!(badge_line("jobs").contains("JOBS.length"));
    assert!(badge_line("errors").contains("!==200"));
}

#[test]
fn d2_one_range_control_in_the_top_bar() {
    let start = PAGE.find("<header class=\"topbar\">").expect("top bar");
    let bar = &PAGE[start..start + PAGE[start..].find("</header>").expect("end")];
    assert!(
        bar.contains("id=\"rangebar\""),
        "the range control sits in the top bar"
    );
    assert_eq!(PAGE.matches("id=\"rangebar\"").count(), 1);
    assert!(
        !PAGE.contains("rangeSeg("),
        "no page carries its own range switch"
    );
    assert!(
        !PAGE.contains("data-hf=\"range\""),
        "the request search has none either"
    );
    assert!(!PAGE.contains("HS.range"));
    for label in [
        "15 min", "1 hour", "24 hours", "7 days", "30 days", "90 days", "Custom",
    ] {
        assert!(PAGE.contains(label), "preset {label}");
    }
    for subtitle in ["'last 7 days'", "' to '"] {
        assert!(PAGE.contains(subtitle), "the subtitle reads {subtitle}");
    }
    assert!(PAGE.contains("localStorage.setItem('huddle-range-custom'"));
    assert!(PAGE.contains("function rangeQuery(){"));
    for page in ["overview", "models", "usage"] {
        let at = PAGE.find(&format!("PAGES.{page}={{")).expect("page");
        assert!(
            PAGE[at..at + 700].contains("rangeText()"),
            "{page} names the range"
        );
    }
}

#[test]
fn d2_custom_picker_stops_at_kept_history() {
    assert!(PAGE.contains("/huddle-router/history?view=meta"));
    assert!(PAGE.contains("HIST.keep"));
    assert!(PAGE.contains("type=\"datetime-local\""));
    assert_eq!(
        PAGE.matches("type=\"datetime-local\"").count(),
        2,
        "from and to"
    );
    assert!(PAGE.contains("min=\"${stamp(earliest())}\""));
    assert!(PAGE.contains("max=\"${stamp(Date.now())}\""));
    assert!(PAGE.contains("clamped_from_ms"));
    assert!(
        PAGE.contains("RANGE_MIN[k]/1440>HIST.keep?' disabled'"),
        "presets past the kept history are off"
    );
}

const PROVIDERS_JS: &str = include_str!("../src/dashboard/pages/providers.js");

#[test]
fn d8_status_follows_live_traffic() {
    let core = include_str!("../src/dashboard/core.js");
    let start = core.find("function provStatus(").expect("status");
    let body = &core[start..start + 1100];
    for word in [
        "['bad','Out of quota']",
        "['bad','Failing']",
        "['bad','Needs a fix']",
        "['ok','Working']",
        "['off','Not set up']",
    ] {
        assert!(body.contains(word), "{word}: {body}");
    }
    assert!(core.contains("const FAIL_RATE=0.2"), "20 % threshold");
    assert!(body.contains("x.err/x.n>FAIL_RATE"), "{body}");
    assert!(
        !PROVIDERS_JS.contains("'Built in'"),
        "Anthropic gets a live status"
    );
    assert!(
        body.contains("x.n||x.laneOn") || body.contains("x.laneOn||x.n"),
        "traffic or an enabled lane without a key needs a fix: {body}"
    );
    let order = PROVIDERS_JS.find("const ordered=").expect("order");
    assert!(
        PROVIDERS_JS[order..].contains("x.id==='anthropic'"),
        "Anthropic first"
    );
}

#[test]
fn d8_quota_and_reset_show_per_provider() {
    for read in [
        "SNAP.providers",
        ".quota",
        ".windows",
        ".used_pct",
        ".resets_ms",
        ".balance",
        ".amount",
        ".currency",
        ".unverified",
        "quota unknown",
        "resets ",
        "kind==='none'",
        "label:'Plan or balance'",
    ] {
        let page_and_shared = format!("{PROVIDERS_JS}{}", include_str!("../src/dashboard/core.js"));
        assert!(page_and_shared.contains(read), "{read}");
    }
    assert!(
        PROVIDERS_JS.contains("quota used up, resets"),
        "429 quota reads as a reset"
    );
    for column in [
        "label:'Provider'",
        "label:'Requests'",
        "label:'Errors'",
        "label:'Time to first token'",
        "label:'Tokens'",
        "label:'API price'",
        "label:'Status and next step'",
        "servedCell(x)",
    ] {
        assert!(PROVIDERS_JS.contains(column), "{column}");
    }
    for kpi in ["k:'Set up'", "k:'Requests'", "k:'Errors'"] {
        assert!(PROVIDERS_JS.contains(kpi), "{kpi}");
    }
    assert!(
        PROVIDERS_JS.contains("by=lane"),
        "history totals by provider"
    );
    let state = fixture();
    let quota = &state["providers"]["zai"]["quota"];
    assert!(quota["windows"][0]["resets_ms"].is_number());
}

#[test]
fn d8_a_failing_provider_is_never_greyed() {
    assert!(
        PROVIDERS_JS.contains("x.status[0]==='off'&&!x.n"),
        "only a quiet, unset provider folds into the grey rest"
    );
    assert!(
        !PROVIDERS_JS.contains("live.filter(x=>x.status[0]!=='off')"),
        "status alone no longer decides greying"
    );
    assert!(
        PROVIDERS_JS.contains("quota used up"),
        "429 quota is a cause, not a rate limit"
    );
    assert!(PROVIDERS_JS.contains("causes("), "errors group by cause");
}

const SESSIONS_JS: &str = include_str!("../src/dashboard/pages/sessions.js");
const AGENTS_JS: &str = include_str!("../src/dashboard/pages/agents.js");
const DRAWERS_JS: &str = include_str!("../src/dashboard/drawers.js");
const CORE_JS: &str = include_str!("../src/dashboard/core.js");
const PROJECTS_JS: &str = include_str!("../src/dashboard/pages/projects.js");
const WORKTREES_JS: &str = include_str!("../src/dashboard/pages/worktrees.js");
const JOBS_JS: &str = include_str!("../src/dashboard/pages/jobs.js");

#[test]
fn d9_sessions_use_the_new_states() {
    for label in [
        "Needs you",
        "Working",
        "Background",
        "Your turn",
        "Done",
        "Idle",
    ] {
        assert!(SESSIONS_JS.contains(label), "Sessions names {label}");
    }
    for key in [
        "needs_you",
        "working",
        "background",
        "your_turn",
        "done",
        "idle",
    ] {
        assert!(
            SESSIONS_JS.contains(&format!("['{key}',")),
            "a filter chip for {key}"
        );
    }
    assert!(!SESSIONS_JS.contains("'Waiting'"), "no Waiting chip");
    assert!(
        SESSIONS_JS.contains("re-reads"),
        "a cold cache says what the next message re-reads"
    );
    let state = fixture();
    let states: Vec<&str> = state["sessions"]
        .as_array()
        .expect("sessions")
        .iter()
        .filter_map(|s| s["state"].as_str())
        .collect();
    for want in [
        "needs_you",
        "working",
        "background",
        "your_turn",
        "done",
        "idle",
    ] {
        assert!(states.contains(&want), "the fixture has a {want} session");
    }
}

#[test]
fn d9_sessions_group_by_repo() {
    assert!(SESSIONS_JS.contains("data-seg=\"sessgrp\"") || SESSIONS_JS.contains("'sessgrp'"));
    assert!(SESSIONS_JS.contains("Group by repo"));
    assert!(SESSIONS_JS.contains("s.repo"), "groups on the repo key");
    assert!(
        CORE_JS.contains("repo:x.repo"),
        "the snapshot's repo key is read"
    );
    for s in fixture()["sessions"].as_array().expect("sessions") {
        assert!(s["repo"].is_string(), "every fixture session has a repo");
    }
}

#[test]
fn t2_sessions_list_follows_the_range() {
    assert!(
        SESSIONS_JS.contains("const sessShown="),
        "one helper decides which sessions the range lists"
    );
    assert!(
        SESSIONS_JS.contains("ACTIVE_STATES.includes(skey(s))"),
        "a live session (needs you, working, background) stays listed"
    );
    assert!(
        SESSIONS_JS.contains("hOf(HT.ses,s.id).n>0"),
        "a session with a request inside the range stays listed"
    );
    assert!(
        SESSIONS_JS.contains("!HT.ses||HT.err?SESS:"),
        "before the totals load, or when they fail, every session is listed"
    );
    assert!(
        SESSIONS_JS.contains("k==='all'?sessShown().length"),
        "the All chip counts the shown sessions"
    );
    assert!(
        SESSIONS_JS.contains("sessShown().filter(s=>skey(s)===k)"),
        "each state chip counts the shown sessions"
    );
    assert!(
        SESSIONS_JS.contains("const rows=sessShown().filter"),
        "the table lists the shown sessions"
    );
    assert!(
        !SESSIONS_JS.contains("const rows=SESS.filter"),
        "the table does not list every session"
    );
    let badge = badge_line("sessions");
    assert!(
        badge.contains("sessShown().length"),
        "the sidebar badge counts what the list shows"
    );
    assert!(
        badge.contains("rangeText()"),
        "the badge names the selected range"
    );
    assert!(
        !badge.contains("last 24 hours"),
        "the badge does not hard-code 24 hours"
    );
    assert!(
        SESSIONS_JS.contains("empty:'No sessions match'"),
        "an empty range shows the page's empty state"
    );
}

#[test]
fn d9_no_column_clips_at_1440() {
    let at = SESSIONS_JS.find("const SESS_TBL_COLS=[").expect("columns");
    let cols = &SESSIONS_JS[at..at + SESSIONS_JS[at..].find("];").expect("end")];
    let keys: Vec<&str> = cols
        .split("{k:'")
        .skip(1)
        .filter_map(|c| c.split('\'').next())
        .collect();
    assert!(keys.len() + 2 <= 9, "at most nine columns: {keys:?}");
    for k in ["state", "title", "model", "ctx"] {
        let line = cols
            .lines()
            .find(|l| l.contains(&format!("{{k:'{k}'")))
            .expect(k);
        assert!(!line.contains(",sm:1"), "{k} stays on a phone");
    }
    for k in ["cache", "errors", "last"] {
        let line = cols
            .lines()
            .find(|l| l.contains(&format!("{{k:'{k}'")))
            .expect(k);
        assert!(line.contains(",sm:1"), "{k} drops on a phone");
    }
    for c in ["hideOnPhone(HC[0])", "hideOnPhone(HC[2]"] {
        assert!(cols.contains(c), "{c}: requests and cost drop on a phone");
    }
    assert!(!cols.contains("Subagents"), "no wide subagent column");
}

#[test]
fn d9_agents_list_subagents_and_background() {
    for label in [
        "Main",
        "Subagent",
        "Background",
        "Ran for",
        "Requests",
        "Tokens",
    ] {
        assert!(AGENTS_JS.contains(label), "Agents names {label}");
    }
    assert!(
        AGENTS_JS.contains("30*MIN"),
        "a row quiet for 30 minutes reads Idle"
    );
    assert!(CORE_JS.contains("o.bg="), "background tasks become rows");
    assert!(
        CORE_JS.contains("s.agents.concat(s.bg)"),
        "the page lists them"
    );
    let background = fixture()["sessions"]
        .as_array()
        .expect("sessions")
        .iter()
        .any(|s| s["open_tasks"].as_u64().unwrap_or(0) > 0);
    assert!(background, "the fixture has a session with open tasks");
}

#[test]
fn d9_session_drawer_retries_history_and_shows_reasons() {
    assert!(
        CORE_JS.contains("data-retry"),
        "a failed history load can retry"
    );
    assert!(CORE_JS.contains("[data-retry]"));
    assert!(
        DRAWERS_JS.contains("histBlock('session:'"),
        "history in the global range"
    );
    assert!(
        DRAWERS_JS.contains("'reason'"),
        "recent requests carry the reason"
    );
    assert!(
        DRAWERS_JS.contains("s.agents.concat(s.bg)"),
        "agents and background tasks with counts"
    );
}

const MODELS_JS: &str = include_str!("../src/dashboard/pages/models.js");
const USAGE_JS: &str = include_str!("../src/dashboard/pages/usage.js");

#[test]
fn d11_models_show_api_price_and_unpriced() {
    for column in [
        "label:'Provider'",
        "label:'Error rate'",
        "label:'Time to first token'",
        "label:'Speed'",
        "label:'Tokens'",
        "label:'Cache hit'",
        "label:'API price'",
        "label:'Price per 1M tokens'",
        "label:'Sessions'",
        "label:'Last used'",
    ] {
        assert!(MODELS_JS.contains(column), "{column}");
    }
    assert!(
        MODELS_JS.contains("noPrice") && CORE_JS.contains(">no price<"),
        "an unpriced model says so"
    );
    assert!(
        MODELS_JS.contains("priced"),
        "reads the router's priced flag"
    );
    assert!(
        MODELS_JS.contains("by=model"),
        "cost comes from history totals"
    );
    assert!(
        !MODELS_JS.contains("Est. cost") && !MODELS_JS.contains("rangeCols("),
        "no estimated-cost column left over"
    );
    assert!(
        MODELS_JS.contains("tokSplit") || MODELS_JS.contains("x.fresh"),
        "token split"
    );
}

#[test]
fn d11_usage_shows_every_providers_plan() {
    let page = USAGE_JS;
    assert!(page.contains("Plans and quota"), "section title");
    assert!(page.contains("provQuota("), "reads providers[id].quota");
    assert!(
        page.contains("quotaBody("),
        "same plan and balance text as Providers"
    );
    assert!(page.contains("PL.rows"), "every provider the plugin lists");
    assert!(page.contains("'anthropic'"), "Claude is a row too");
    assert!(page.contains("Not set up"), "an unset provider says so");
    assert!(!page.contains("PLAN.h5"), "the Claude-only box is gone");
    for helper in [
        "function quotaBody(",
        "function provQuota",
        "const provQuota",
    ] {
        assert!(
            !PROVIDERS_JS.contains(helper),
            "{helper} lives in core.js, shared with Usage"
        );
    }
    let core = include_str!("../src/dashboard/core.js");
    assert!(core.contains("function quotaBody(") && core.contains("const provQuota="));
}

#[test]
fn d11_cost_per_day_by_provider() {
    assert!(USAGE_JS.contains("by=day"), "history totals by day");
    assert!(USAGE_JS.contains("Cost per day"), "section title");
    assert!(
        USAGE_JS.contains("laneOfModel("),
        "each model's cost goes to its provider"
    );
    assert!(
        USAGE_JS.contains("class=\"daycol"),
        "one stacked column per day"
    );
    assert!(USAGE_JS.contains("API price"), "labelled as the API price");
    assert!(
        USAGE_JS.contains("cache_saved_usd"),
        "cache savings in dollars"
    );
    assert!(
        USAGE_JS.contains("Saved by the cache"),
        "cache savings card"
    );
}

#[test]
fn d11_matrix_follows_the_range() {
    assert!(USAGE_JS.contains("by=project"), "history totals by repo");
    assert!(
        USAGE_JS.contains("rangeQuery()"),
        "the global range drives it"
    );
    assert!(
        !USAGE_JS.contains("last 15 min, total tokens"),
        "the subtitle names the picked range"
    );
    assert!(
        !include_str!("../src/dashboard/pages/projects.js").contains("function matrix("),
        "the matrix lives on Usage"
    );
    assert!(
        USAGE_JS.contains("function matrix("),
        "Usage owns the matrix"
    );
}

const REQUESTS_JS: &str = include_str!("../src/dashboard/pages/requests.js");
const ERRORS_JS: &str = include_str!("../src/dashboard/pages/errors.js");

#[test]
fn d10_failures_show_their_reason() {
    for word in [
        "function reasonChip(",
        "quota used up",
        "overloaded",
        "429",
        "529",
        "label:'Provider'",
        "label:'Router'",
        "r.up",
        "r.agentId",
        "label:'API price'",
    ] {
        assert!(REQUESTS_JS.contains(word), "{word}");
    }
    assert!(
        REQUESTS_JS.contains("lanesWithTraffic()"),
        "the filter lists only providers with traffic"
    );
    assert!(
        REQUESTS_JS.contains("loadReqCost("),
        "live rows take their price from the 15 minute history"
    );
    assert!(
        REQUESTS_JS.contains("rangeQuery()"),
        "history follows the global range"
    );
    assert!(
        REQUESTS_JS.contains("liveRange()?'live':'history'"),
        "a range past 15 minutes opens the history list"
    );
}

#[test]
fn d10_errors_group_by_provider_and_reason() {
    for word in [
        "function errGroups(",
        "r.lane+'|'+",
        "label:'Provider'",
        "label:'Count'",
        "label:'First seen'",
        "label:'Last seen'",
        "label:'Sessions hit'",
        "label:'Fix'",
        "quota used up, resets",
        "resetAt(",
        "quotaState(",
        "Error rate per provider",
        "<details",
        "What each cause means",
        "status=error",
        "function errSeries(",
        "loadErrors",
    ] {
        assert!(ERRORS_JS.contains(word), "{word}");
    }
    assert!(
        !ERRORS_JS.contains("g.def[3]"),
        "no per-status table with a counts-as-error column"
    );
}

#[test]
fn d10_live_tail_pauses_while_scrolled() {
    for word in [
        "const TAIL=",
        "function tailRows(",
        "data-tail=\"jump\"",
        "addEventListener('scroll'",
        "scrollY",
        " new</button>",
    ] {
        assert!(REQUESTS_JS.contains(word), "{word}");
    }
    assert!(
        REQUESTS_JS.contains("r.t<=TAIL.cut"),
        "rows newer than the pause point stay out of the list"
    );
}

#[test]
fn d7_cards_come_first_in_a_full_grid() {
    let overview = page_between("PAGES.overview={", "after:()=>{bindChart()");
    let header = overview.find("head('Overview'").expect("header");
    let kpis = overview.find("reg('kpis'").expect("kpis");
    let needs = overview.find("secH('Needs you'").expect("needs you");
    let chart = overview.find("secH('Traffic'").expect("traffic");
    let provs = overview.find("secH('Providers'").expect("providers");
    let sess = overview.find("secH('Top sessions'").expect("top sessions");
    assert!(header < kpis && kpis < needs && needs < chart && chart < provs);
    assert!(chart < sess, "the working sessions close the page");
    let cards = page_between("function overviewKpis()", "const CH=");
    assert!(cards.contains("kpis k8"), "eight cards in one grid class");
    assert!(cards.contains(".slice(0,8)"), "never more than eight");
    for label in [
        "'Sessions'",
        "'Requests'",
        "'Tokens'",
        "'Cost'",
        "'Time to first token'",
        "'Errors'",
    ] {
        assert!(cards.contains(label), "card {label}");
    }
    assert!(cards.contains("API price"), "the cost card says API price");
    assert!(
        cards.contains("planCards()"),
        "plan cards come from the quota"
    );
    assert!(cards.contains("fillers"), "a short row fills to eight");
    let css = include_str!("../src/dashboard/style.css");
    assert!(css.contains(".kpis.k8{grid-template-columns:repeat(4,"));
}

#[test]
fn d7_needs_you_shows_five_then_show_all() {
    let list = page_between("function attnList()", "function overviewKpis");
    assert!(list.contains(".slice(0,5)"), "at most five items");
    assert!(list.contains("Show all"), "a Show all link");
    assert!(list.contains("#needs-you"), "it opens the Needs you page");
    let overview = page_between("PAGES.overview={", "after:()=>{bindChart()");
    assert!(
        overview.contains("['needs-you','Show all']"),
        "the header link too"
    );
}

#[test]
fn d7_needs_you_page_groups_items() {
    assert!(PAGE.contains("PAGES['needs-you']={"), "the page");
    assert!(PAGE.contains("['needs-you','Needs you']"), "the nav entry");
    let nav = page_between("const NAV=[", "const bdg=");
    let monitor = nav.find("'Monitor'").expect("monitor");
    let usage = nav.find("'Usage'").expect("usage");
    assert!(
        nav.find("['needs-you','Needs you']")
            .is_some_and(|i| i > monitor && i < usage)
    );
    assert!(PAGE.contains("case 'needs-you':"), "the badge");
    let page = page_between("PAGES['needs-you']={", "\n\n");
    for group in [
        "Questions",
        "Providers",
        "Router",
        "Jobs to review",
        "Setup checks",
        "Context",
    ] {
        assert!(PAGE.contains(&format!("'{group}'")), "group {group}");
    }
    assert!(page.contains("NEED_GROUPS"), "items group by kind");
    let items = page_between("function attnItems(){", "const unsetProviders");
    for part in ["since:", "why:", "act:"] {
        assert!(items.contains(part), "each item carries {part}");
    }
}

#[test]
fn d7_turn_ended_sessions_are_not_needs_you() {
    let state = fixture();
    let states: Vec<&str> = state["sessions"]
        .as_array()
        .expect("sessions")
        .iter()
        .filter_map(|s| s["state"].as_str())
        .collect();
    assert!(states.contains(&"your_turn") && states.contains(&"needs_you"));
    let items = page_between("function attnItems(){", "const unsetProviders");
    assert!(items.contains("s.state==='needs_you'"));
    assert!(!items.contains("your_turn"), "a turn that ended is no item");
    assert!(!items.contains("sstate("), "the folded state is not used");
    assert!(!items.contains("429"), "rate limits are not asks");
    assert!(!items.contains("stale"), "stale worktrees are not asks");
    assert!(items.contains("ctx>=90"), "context over 90 percent");
    assert!(items.contains("'working'") && items.contains("'background'"));
}

#[test]
fn d7_overview_providers_use_the_providers_page_status() {
    let core = include_str!("../src/dashboard/core.js");
    assert!(
        core.contains("function provStatus("),
        "one shared helper in core.js"
    );
    let providers = include_str!("../src/dashboard/pages/providers.js");
    assert!(
        !providers.contains("function provStatus("),
        "the Providers page uses it"
    );
    let overview = include_str!("../src/dashboard/pages/overview.js");
    assert!(
        overview.contains("provStatus("),
        "the mini panel calls the helper"
    );
    assert!(!PAGE.contains("Built in"), "Anthropic gets a live status");
    assert!(
        core.contains("RT.path.state==='failing'"),
        "a failing Anthropic path reads Failing"
    );
}

/// The page is one script joined from many files: a name defined twice stops the whole page, so
/// every top-level `const`, `let` and `function` name, and every `PAGES.<page>`, appears once.
#[test]
fn the_joined_page_defines_each_name_once() {
    let mut seen = std::collections::HashSet::new();
    for line in PAGE.lines() {
        let Some((kind, rest)) = ["const ", "let ", "function ", "PAGES."]
            .iter()
            .find_map(|kw| {
                line.strip_prefix(kw)
                    .map(|rest| (kw.starts_with('P'), rest))
            })
        else {
            continue;
        };
        let name: String = rest
            .chars()
            .take_while(|c| c.is_alphanumeric() || *c == '_' || *c == '$')
            .collect();
        if !name.is_empty() {
            assert!(seen.insert((kind, name.clone())), "{name} is defined twice");
        }
    }
}

#[test]
fn d12_projects_are_repos() {
    assert!(
        CORE_JS.contains("repo:w.repo"),
        "a worktree's repo key is read"
    );
    assert!(
        CORE_JS.contains("s.repo||s.project"),
        "projects group on the repo key"
    );
    for label in [
        "Active sessions",
        "Worktrees",
        "Requests",
        "Tokens",
        "Cost",
        "Top model",
        "Last activity",
    ] {
        let found = PROJECTS_JS.contains(label)
            || PROJECTS_JS.contains("histCols")
            || CORE_JS.contains(label);
        assert!(found, "Projects shows {label}");
    }
    assert!(PROJECTS_JS.contains("Active sessions"));
    assert!(
        PROJECTS_JS.contains("stale"),
        "the worktree cell counts stale ones"
    );
    assert!(
        PROJECTS_JS.contains("histTop"),
        "top model comes from the range totals"
    );
    assert!(
        DRAWERS_JS.contains("Branches"),
        "the project drawer lists branches"
    );
    for w in fixture()["worktrees"].as_array().expect("worktrees") {
        assert!(w["repo"].is_string(), "every fixture worktree has a repo");
    }
}

#[test]
fn d12_worktrees_filter_stale() {
    for chip in ["['stale',", "['dirty',"] {
        assert!(WORKTREES_JS.contains(chip), "a filter chip {chip}");
    }
    assert!(WORKTREES_JS.contains("Stale"));
    assert!(WORKTREES_JS.contains("Uncommitted"));
    assert!(
        WORKTREES_JS.contains("worktree remove"),
        "the command that removes a worktree"
    );
    assert!(WORKTREES_JS.contains("rgrp"), "grouped by repo");
    assert!(WORKTREES_JS.contains("w.repo"), "grouped on the repo key");
    assert!(
        CORE_JS.contains("5*DAY"),
        "stale means no session and five days old"
    );
    let trees = fixture()["worktrees"].clone();
    let stale = trees
        .as_array()
        .expect("worktrees")
        .iter()
        .filter(|w| w["kind"] == "agent" && w["used_by"].is_null())
        .count();
    assert!(stale >= 1, "the fixture has a stale worktree");
}

#[test]
fn d12_finished_jobs_stay_visible() {
    assert!(JOBS_JS.contains("Outcome"), "an outcome column");
    assert!(JOBS_JS.contains("Review"), "a review state column");
    assert!(JOBS_JS.contains("Ran for"), "a duration column");
    assert!(JOBS_JS.contains("Harness"));
    assert!(
        JOBS_JS.contains("jobsInRange"),
        "finished jobs are kept for the global range"
    );
    for state in ["accepted", "cancelled", "completed", "failed"] {
        assert!(JOBS_JS.contains(state), "the page names {state}");
    }
    let states: Vec<String> = fixture()["jobs"]
        .as_array()
        .expect("jobs")
        .iter()
        .filter_map(|j| j["state"].as_str().map(str::to_string))
        .collect();
    for want in ["accepted", "cancelled"] {
        assert!(
            states.iter().any(|s| s == want),
            "the fixture has a {want} job"
        );
    }
}

#[test]
fn d13_harness_failures_say_why() {
    let state = fixture();
    let rows = state["harnesses"]["rows"].as_array().expect("rows");
    let failing = rows
        .iter()
        .find(|row| row["status"] == "fail")
        .expect("a failing row");
    assert!(
        failing["why"].as_str().is_some_and(|why| !why.is_empty()),
        "a failing harness carries the first line of its error"
    );
    assert!(
        state["harnesses"]["checked_ms"].is_u64(),
        "last checked time"
    );
    let start = PAGE.find("PAGES.harnesses=").expect("harnesses page");
    let body = &PAGE[start..PAGE.len().min(start + 4000)];
    assert!(body.contains("h.why"), "the row shows why it fails");
    assert!(
        body.contains("checked_ms") || body.contains("HARN.checked"),
        "{body}"
    );
    assert!(
        body.contains("Last checked"),
        "the page names the last check time"
    );
    assert!(!body.contains("sm:1"), "no column hides at 1440 px");
}

#[test]
fn d13_router_shows_rate_overhead_and_phases() {
    let state = fixture();
    let phases = &state["router"]["path"]["by_phase"];
    for phase in ["dns", "connect", "tls", "answer"] {
        assert!(phases[phase].is_u64(), "by_phase.{phase}");
    }
    let start = PAGE.find("const rateNow=").expect("router page");
    let body = &PAGE[start..PAGE.len().min(start + 9000)];
    assert!(body.contains("/s"), "requests per second");
    assert!(body.contains("PS"), "reads per_second");
    assert!(body.contains("rt"), "reads router_ms");
    assert!(body.contains("p95"), "overhead p95");
    assert!(
        body.contains("by_phase") || body.contains("RT.phases"),
        "phases"
    );
    for label in ["DNS", "Connect", "TLS", "Answer"] {
        assert!(body.contains(label), "phase label {label}");
    }
    assert!(body.contains("RT.sup"), "supervision shown");
    assert!(
        body.contains("dashboard's own"),
        "note on the dashboard's cost"
    );
    assert!(!body.contains("idle cost near zero"));
}

#[test]
fn d13_polling_pauses_on_a_hidden_tab() {
    let start = PAGE.find("function getState").expect("getState");
    let body = &PAGE[start..PAGE.len().min(start + 400)];
    assert!(
        body.contains("visibilityState"),
        "the poll skips a hidden tab"
    );
    assert!(
        PAGE.contains("addEventListener('visibilitychange'"),
        "a tab that turns visible refreshes at once"
    );
    let tick = PAGE
        .find("setInterval(()=>update(SNAP),2000)")
        .map(|at| &PAGE[at.saturating_sub(60)..at + 60])
        .unwrap_or("");
    assert!(
        tick.contains("visibilityState") || !PAGE.contains("setInterval(()=>update(SNAP),2000)"),
        "the repaint tick pauses on a hidden tab: {tick}"
    );
}

#[test]
fn c1_cost_cells_say_api_price_once_in_the_header_and_never_per_cell() {
    assert!(!PAGE.contains("API-equivalent"), "no per-cell label");
    assert!(!PAGE.contains("apiq("), "no per-cell label helper");
    assert!(CORE_JS.contains("label:'API price'"), "shared cost column");
    assert!(
        CORE_JS.contains("label(){return `API price ("),
        "the history cost column"
    );
    assert!(!CORE_JS.contains("Est. cost"), "{}", "no estimate wording");
    assert!(CORE_JS.contains("const noPrice="), "one shared 'no price'");
    assert!(
        MODELS_JS.contains("noPrice"),
        "Models reads the shared 'no price'"
    );
    assert!(
        CORE_JS.contains("priced:t.priced!==false"),
        "history rows carry the priced flag"
    );
    assert!(
        USAGE_JS.contains("FLAT"),
        "the table note explains the flat plan once"
    );
}

#[test]
fn c1_page_subtitles_name_the_global_range() {
    for (name, source) in [
        ("sessions", SESSIONS_JS),
        ("projects", PROJECTS_JS),
        ("jobs", JOBS_JS),
    ] {
        let head = source
            .lines()
            .find(|l| l.contains("head('"))
            .unwrap_or_else(|| panic!("{name} head"));
        assert!(head.contains("${rangeText()}"), "{name}: {head}");
        assert!(!head.contains("last 24 hours"), "{name}: {head}");
    }
}

#[test]
fn c1_the_providers_badge_counts_the_status_the_page_shows() {
    let line = PAGE
        .lines()
        .find(|l| l.contains("case 'providers':"))
        .expect("providers badge");
    assert!(line.contains("provSetUp()"), "{line}");
    assert!(!line.contains("set_up===true"), "{line}");
    let at = PROVIDERS_JS.find("const provSetUp=").expect("provSetUp");
    let def = &PROVIDERS_JS[at..PROVIDERS_JS.len().min(at + 300)];
    for status in ["Working", "Out of quota", "Failing"] {
        assert!(def.contains(status), "{status}: {def}");
    }
}

#[test]
fn c1_the_errors_legend_shows_the_range_average() {
    assert!(ERRORS_JS.contains("Range average"), "the legend says so");
    assert!(
        ERRORS_JS.contains("S.tot[l]"),
        "the legend reads the range totals"
    );
    assert!(!ERRORS_JS.contains("latest bucket"));
}

#[test]
fn c1_history_reasons_feed_providers_and_errors_in_longer_ranges() {
    assert!(PROVIDERS_JS.contains("by=reason"), "providers load reasons");
    assert!(ERRORS_JS.contains("by=reason"), "errors load reasons");
    assert!(
        PROVIDERS_JS.contains("HP.reason"),
        "history causes come from the loaded reasons"
    );
}
