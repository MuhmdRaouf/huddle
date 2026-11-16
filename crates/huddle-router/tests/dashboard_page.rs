//! The dashboard page is one self-contained page, joined from the files in `src/dashboard/`: no framework, no build step, nothing loaded
//! from outside the router, and no snapshot data ever parsed as markup outside one helper.

use serde_json::Value;

use huddle_router::dashboard::PAGE;
use huddle_testkit::temp_home::TempHome;
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
fn polls_the_state_at_the_chosen_interval() {
    assert!(PAGE.contains("fetch(stateUrl()"));
    assert!(PAGE.contains("'/huddle-router/state'"));
    assert!(PAGE.contains("state:getState"));
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
    assert!(PAGE.contains("function refreshHistory(manual){"));
    let timer = PAGE
        .split("function refreshHistory(manual){")
        .nth(1)
        .expect("refreshHistory");
    assert!(timer[..300].contains("loadHistory()"));
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
    assert!(PAGE.contains("totals&by=session&models=0&'+sessionIds()+rangeQuery()"));
    assert!(PAGE.contains("totals&by=project&'+rangeQuery()"));
    assert!(PAGE.contains("buckets&'+rangeQuery()+'&'+q.k+'='+encodeURIComponent(q.id)"));
    assert!(PAGE.contains("function loadTotals(){if(!wantTotals())return"));
    let timer = PAGE
        .split("function refreshHistory(manual){")
        .nth(1)
        .expect("refreshHistory");
    assert!(timer[..300].contains("loadTotals()"));
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
    assert_eq!(
        PAGE.matches("setInterval(").count(),
        2,
        "the repaint tick and the refresh clock"
    );
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
    assert!(badge_line("errors").contains("failed_15m"));
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
        "15 min", "1 hour", "24 hours", "7 days", "30 days", "90 days",
    ] {
        assert!(PAGE.contains(label), "preset {label}");
    }
    assert!(
        PAGE.contains("Absolute time range"),
        "the panel holds From and To"
    );
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
    assert!(
        PAGE.contains("dtField('from'") && PAGE.contains("dtField('to'"),
        "from and to"
    );
    assert!(
        PAGE.contains("lo=startOfDay(earliest())"),
        "days before the kept history are off"
    );
    assert!(PAGE.contains("hi=Date.now()"), "days after now are off");
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
        "label:'Cost per 1M total tokens'",
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
        "label:'Sessions hit (newest 500)'",
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

#[test]
fn e9_dashboard_has_no_native_select() {
    assert!(!PAGE.contains("<select"), "no native select");
    for kind in ["date", "time", "datetime-local"] {
        assert!(
            !PAGE.contains(&format!("type=\"{kind}\"")),
            "no native {kind} input"
        );
    }
}

#[test]
fn e9_range_panel_keeps_its_values() {
    let ranges = PAGE.find("const RANGES=[").expect("ranges");
    let list = &PAGE[ranges..ranges + PAGE[ranges..].find("];").expect("end")];
    let labels: Vec<&str> = [
        "15 min", "1 hour", "24 hours", "7 days", "30 days", "90 days",
    ]
    .into_iter()
    .collect();
    let mut at = 0;
    for label in &labels {
        let found = list[at..].find(label).expect(label);
        at += found + label.len();
    }
    assert!(
        !PAGE.contains("{k:'custom',t:'Custom',sep:1}"),
        "the range has no Custom entry: the panel holds the absolute range"
    );
    assert!(
        !PAGE.contains("MENUS.range="),
        "the range is a panel, not a menu"
    );
    assert!(
        PAGE.contains("setRange(q.dataset.rq)"),
        "a quick range goes through setRange"
    );
    assert!(
        PAGE.contains("RANGE_MIN[k]/1440>HIST.keep"),
        "presets past the kept history stay off"
    );
}

#[test]
fn e9_menu_follows_the_listbox_pattern() {
    for part in [
        "aria-haspopup=\"listbox\"",
        "aria-expanded",
        "role=\"listbox\"",
        "role=\"option\"",
        "aria-selected",
        "aria-activedescendant",
        "'Home'",
        "'End'",
        "'Escape'",
        "'Tab'",
        "prefers-reduced-motion",
    ] {
        assert!(PAGE.contains(part), "{part}");
    }
    assert!(
        PAGE.contains("MENUS.hstatus="),
        "the status filter is a page menu"
    );
    assert!(
        PAGE.contains("dtField('from','rfrom'") && PAGE.contains("dtField('to','rto'"),
        "own from and to fields"
    );
    assert!(PAGE.contains("data-day="), "own calendar");
}

#[test]
fn j1_models_page_reads_ttft_from_totals() {
    let rows = PAGE
        .split("function histRows(){")
        .nth(1)
        .and_then(|s| s.split("const RM=").next())
        .expect("histRows");
    assert!(rows.contains("t.ttft_p95") && rows.contains("t.ttft_p50"));
    assert!(
        !rows.contains("Math.max(l.p95"),
        "a range p95 is never the max of bucket p95s"
    );
    assert!(PAGE.contains("p95=live?wp(r=>r.p95):num(HIST.tot&&HIST.tot.ttft_p95)"));
}

#[test]
fn j3_models_label_says_total_tokens() {
    assert!(PAGE.contains("label:'Cost per 1M total tokens'"), "column");
    assert!(!PAGE.contains("Price per 1M tokens"), "old label is gone");
    assert!(
        PAGE.contains("title=\"Cache reads and writes included\""),
        "the cost per 1M value explains itself"
    );
    assert!(
        MODELS_JS.contains("x.cost/(x.tin+x.out)*1e6"),
        "the cost arithmetic is untouched"
    );
}

#[test]
fn j3_errors_label_says_newest_500() {
    assert!(PAGE.contains("label:'Sessions hit (newest 500)'"), "column");
    assert!(
        ERRORS_JS.contains("g.sessions==null?NA:g.sessions"),
        "the session arithmetic is untouched"
    );
}

#[test]
fn k3_sessions_page_asks_slim_and_sheet_asks_full() {
    assert!(PAGE.contains("totals&by=session&models=0&'+sessionIds()+rangeQuery()"));
    assert!(PAGE.contains("totals&by=session&session='+encodeURIComponent(q.id)+'&'+rangeQuery()"));
}

/// The page's zone helpers: the script between the `tz:start` and `tz:end` markers.
fn zone_block() -> String {
    PAGE.split("/* tz:start */")
        .nth(1)
        .and_then(|s| s.split("/* tz:end */").next())
        .expect("the zone helpers sit between tz:start and tz:end")
        .to_string()
}

/// What a page test fails with when bun is missing: a page test never passes without running.
const BUN_MISSING: &str = "bun is not installed: run `mise run setup`";

/// `bun` looked up in the `path` directories (the process `PATH` when `None`).
fn bun_command(path: Option<&std::ffi::OsStr>) -> Result<std::process::Command, String> {
    let path = path
        .map(std::ffi::OsStr::to_os_string)
        .or_else(|| std::env::var_os("PATH"))
        .unwrap_or_default();
    std::env::split_paths(&path)
        .map(|dir| dir.join("bun"))
        .find(|candidate| candidate.is_file())
        .map(std::process::Command::new)
        .ok_or_else(|| BUN_MISSING.to_string())
}

fn bun() -> std::process::Command {
    bun_command(None).unwrap_or_else(|message| panic!("{message}"))
}

#[test]
fn n1_page_tests_fail_without_bun() {
    let empty = TempHome::new();
    let message = bun_command(Some(empty.root().as_os_str())).expect_err("no bun in an empty PATH");
    assert!(message.contains("mise run setup"), "{message}");
}

/// Runs the zone helpers in bun, in the machine zone UTC+2 (no daylight saving), with the given
/// storage stubs.
fn run_zone_script(body: &str) -> Value {
    use std::io::Write;
    use std::process::Stdio;
    let block = serde_json::to_string(&zone_block()).expect("block");
    let script = format!(
        "const BLOCK={block};\n\
         const num=v=>typeof v==='number'&&isFinite(v)?v:null;\n\
         const make=ls=>new Function('localStorage','num',BLOCK+';return {{TZ,tzSet,tzOff,tzLabel,tzQuery,clock,when,dayText,dtText,shortDay,startOfDay,zmake}}')(ls,num);\n\
         const store=v=>({{getItem:()=>v,setItem:(k,x)=>{{store.saved=[k,x]}}}});\n\
         {body}"
    );
    let mut child = bun()
        .args(["run", "-"])
        .env("TZ", "Etc/GMT-2")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start bun");
    child
        .stdin
        .take()
        .expect("bun stdin")
        .write_all(script.as_bytes())
        .expect("write script");
    let out = child.wait_with_output().expect("bun");
    assert!(
        out.status.success(),
        "bun failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).expect("json out")
}

#[test]
fn k1_page_formats_times_by_the_switch() {
    let v = run_zone_script(
        "const t=Date.UTC(2026,10,14,23,30,15);\n\
         const f=z=>({clock:z.clock(t),when:z.when(t),day:z.dayText(t),dt:z.dtText(t),short:z.shortDay(t),\
           label:z.tzLabel(),query:z.tzQuery(),dayStart:z.startOfDay(t),made:z.zmake(2026,10,15,0,0),dash:z.clock(null)});\n\
         console.log(JSON.stringify({local:f(make(store('local'))),utc:f(make(store('utc')))}));",
    );
    let day14 = 1_794_614_400_000u64; // 2026-11-14T00:00:00Z
    let local = &v["local"];
    assert_eq!(local["clock"], "01:30:15");
    assert_eq!(local["when"], "15 Nov 01:30");
    assert_eq!(local["day"], "15 Nov 2026");
    assert_eq!(local["dt"], "15 Nov 2026, 01:30");
    assert_eq!(local["short"], "Nov 15");
    assert_eq!(local["label"], "Local (UTC+2)");
    assert_eq!(local["query"], "tz_offset_min=120");
    assert_eq!(local["dayStart"], day14 + 86_400_000 - 2 * 3_600_000);
    assert_eq!(local["made"], local["dayStart"]);
    assert_eq!(local["dash"], "—");
    let utc = &v["utc"];
    assert_eq!(utc["clock"], "23:30:15");
    assert_eq!(utc["when"], "14 Nov 23:30");
    assert_eq!(utc["day"], "14 Nov 2026");
    assert_eq!(utc["short"], "Nov 14");
    assert_eq!(utc["label"], "UTC");
    assert_eq!(utc["query"], "tz_offset_min=0");
    assert_eq!(utc["dayStart"], day14);
}

#[test]
fn k1_switch_reaches_every_view_and_refetches() {
    let query = PAGE
        .split("function rangeQuery(){")
        .nth(1)
        .and_then(|s| s.split('}').next())
        .expect("rangeQuery");
    assert!(
        query.contains("tzQuery()"),
        "every history fetch carries the offset"
    );
    let set = PAGE
        .split("function setTz(")
        .nth(1)
        .and_then(|s| s.split("\nfunction ").next())
        .expect("setTz");
    assert!(
        set.contains("tzSet(") && set.contains("applyRange()") && set.contains("renderTzBar()"),
        "switching saves, re-renders the open page and re-fetches: {set}"
    );
    assert!(!PAGE.contains("tzbar"), "the control has left the header");
    assert!(
        PAGE.contains("id=\"tzfoot\""),
        "the control sits in the time panel's footer"
    );
    assert!(
        PAGE.contains("data-tz=") && !PAGE.contains("MENUS.tz="),
        "the control is two buttons in the footer"
    );
    for native in [
        "toLocaleDateString",
        "toTimeString",
        "toDateString",
        "getHours(",
        "getDate(",
    ] {
        assert!(
            !PAGE.contains(native),
            "{native} reads the machine zone, not the switch"
        );
    }
}

#[test]
fn k1_switch_is_remembered() {
    let v = run_zone_script(
        "const a=make(store('utc'));\n\
         const b=make(store('local'));\n\
         const c=make(store(null));\n\
         const bad=make({getItem:()=>{throw new Error('blocked')},setItem:()=>{throw new Error('blocked')}});\n\
         bad.tzSet(true);\n\
         const d=make(store('local'));d.tzSet(true);\n\
         console.log(JSON.stringify({stored_utc:a.TZ.utc,stored_local:b.TZ.utc,empty:c.TZ.utc,blocked:bad.TZ.utc,saved:store.saved}));",
    );
    assert_eq!(v["stored_utc"], true);
    assert_eq!(v["stored_local"], false);
    assert_eq!(v["empty"], false);
    assert_eq!(
        v["blocked"], true,
        "the choice holds for this page even when storage throws"
    );
    assert_eq!(v["saved"], serde_json::json!(["huddle-tz", "utc"]));
}

/// The refresh scheduler: the script between the `rf:start` and `rf:end` markers.
fn refresh_block() -> String {
    PAGE.split("/* rf:start */")
        .nth(1)
        .and_then(|s| s.split("/* rf:end */").next())
        .expect("the refresh scheduler sits between rf:start and rf:end")
        .to_string()
}

/// Runs the scheduler in bun on a fake clock. `body` sees `make(storeValue, opts)` which returns
/// `{rf, calls, clock, tab}`; `clock.advance(ms)` runs the timers due.
fn run_refresh_script(body: &str) -> Value {
    use std::io::Write;
    use std::process::Stdio;
    let block = serde_json::to_string(&refresh_block()).expect("block");
    let script = format!(
        "const BLOCK={block};\n\
         const store=v=>({{getItem:()=>v,setItem:(k,x)=>{{store.saved=[k,x]}}}});\n\
         function make(ls,opts){{opts=opts||{{}};\n\
          const clock={{t:0,timers:[],advance(ms){{const end=this.t+ms;for(;;){{let next=null;for(const x of this.timers)if(x.on&&(next===null||x.at<next.at))next=x;\n\
            if(!next||next.at>end)break;this.t=next.at;next.at+=next.ms;next.fn()}}this.t=end}}}};\n\
          const calls={{state:0,history:0,changed:0}};const tab={{visible:true}};const streams=[];\n\
          const env={{store:ls,floor:opts.floor,visible:()=>tab.visible,every:(fn,ms)=>{{const x={{fn,ms,at:clock.t+ms,on:true}};clock.timers.push(x);return x}},cancel:h=>{{h.on=false}},\n\
           state:()=>{{calls.state++;return opts.state?opts.state():undefined}},history:()=>{{calls.history++}},\n\
           open:cb=>{{const h={{cb,closed:false,close(){{h.closed=true}}}};streams.push(h);return h}},\n\
           later:(fn,ms)=>{{const x={{fn:()=>{{x.on=false;fn()}},ms,at:clock.t+ms,on:true}};clock.timers.push(x);x.ms=1e12;return x}},changed:()=>{{calls.changed++}}}};\n\
          const m=new Function('env',BLOCK+';return rfMake(env)')(env);return {{rf:m,calls,clock,tab,streams}}}}\n\
         (async()=>{{{body}}})();"
    );
    let mut child = bun()
        .args(["run", "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start bun");
    child
        .stdin
        .take()
        .expect("bun stdin")
        .write_all(script.as_bytes())
        .expect("write script");
    let out = child.wait_with_output().expect("bun");
    assert!(
        out.status.success(),
        "bun failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).expect("json out")
}

#[test]
fn k4_refresh_picker_lists_the_intervals() {
    let v = run_refresh_script(
        "const a=make(store(null));\n\
         console.log(JSON.stringify({keys:a.rf.choices().map(c=>c.k),labels:a.rf.choices().map(c=>c.t),def:a.rf.key()}));",
    );
    assert_eq!(
        v["keys"],
        serde_json::json!([
            "off", "rt", "1s", "5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h"
        ])
    );
    assert_eq!(
        v["labels"],
        serde_json::json!([
            "Off", "Realtime", "1s", "5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h"
        ])
    );
    assert_eq!(v["def"], "1s");
}

#[test]
fn k4_header_holds_the_range_refresh_now_and_refresh_picker_group() {
    let range = PAGE.find("id=\"rangebar\"").expect("range bar");
    let group = PAGE.find("id=\"refbar\"").expect("refresh group");
    assert!(range < group, "the refresh group follows the range picker");
    assert!(
        PAGE.contains("MENUS.refresh="),
        "the picker is the page's own menu"
    );
    assert!(PAGE.contains("id=\"refnow\""), "a refresh-now button");
    assert!(PAGE.contains("aria-label=\"Refresh now\""));
    assert!(!PAGE.contains("<select"), "no native select");
    assert!(!PAGE.contains("type=\"time\"") && !PAGE.contains("datetime-local"));
    assert!(
        !PAGE.contains("setInterval(getState,2000)"),
        "the interval is the picker's"
    );
    for fixed in ["loadHistory()},60000", "loadTotals()},60000"] {
        assert!(
            !PAGE.contains(fixed),
            "{fixed}: one rule replaces the fixed times"
        );
    }
    assert!(!PAGE.contains("<6e4"), "no per-panel cache time is left");
}

#[test]
fn k4_state_follows_the_interval() {
    let v = run_refresh_script(
        "const five=make(store('5s'));five.rf.start();five.clock.advance(20000);\n\
         const off=make(store('off'));off.rf.start();off.clock.advance(600000);\n\
         const def=make(store(null));def.rf.start();def.clock.advance(3000);\n\
         console.log(JSON.stringify({five:five.calls.state,off:off.calls.state,def:def.calls.state}));",
    );
    assert_eq!(v["five"], 4, "5s: a fetch every 5,000 ms");
    assert_eq!(v["off"], 0, "Off: nothing on the timer");
    assert_eq!(v["def"], 3, "1s is the default");
}

#[test]
fn l25_a_long_range_refreshes_every_60_seconds() {
    let v = run_refresh_script(
        "const run=(k,floor,ms)=>{const a=make(store(k),{floor});a.rf.start();a.clock.advance(ms);return a.calls.history};\n\
         console.log(JSON.stringify({\
          short:run('5s',()=>5000,60000),long:run('5s',()=>60000,120000),\
          longRt:run('rt',()=>60000,120000),shortRt:run('rt',()=>5000,60000),\
          longMin:run('1m',()=>60000,180000),\
          histMs:(()=>{const a=make(store('5s'),{floor:()=>60000});return a.rf.histMs()})()}));",
    );
    assert_eq!(v["short"], 12);
    assert_eq!(v["long"], 2);
    assert_eq!(v["longRt"], 2);
    assert_eq!(v["shortRt"], 12);
    assert_eq!(v["longMin"], 3);
    assert_eq!(v["histMs"], 60_000);
}

#[test]
fn k4_history_never_faster_than_five_seconds() {
    let v = run_refresh_script(
        "const run=(k,ms)=>{const a=make(store(k));a.rf.start();a.clock.advance(ms);return [a.calls.state,a.calls.history]};\n\
         console.log(JSON.stringify({s1:run('1s',60000),s5:run('5s',60000),m1:run('1m',180000),off:run('off',60000)}));",
    );
    assert_eq!(
        v["s1"],
        serde_json::json!([60, 12]),
        "1s: state each second, history each 5 s"
    );
    assert_eq!(v["s5"], serde_json::json!([12, 12]));
    assert_eq!(v["m1"], serde_json::json!([3, 3]), "1m: once a minute");
    assert_eq!(v["off"], serde_json::json!([0, 0]));
}

#[test]
fn k4_refresh_now_fetches_everything_once() {
    let v = run_refresh_script(
        "const a=make(store('off'));a.rf.start();a.rf.now();\n\
         const b=make(store('1m'));b.rf.start();b.rf.now();b.rf.now();\n\
         console.log(JSON.stringify({off:[a.calls.state,a.calls.history],twice:[b.calls.state,b.calls.history]}));",
    );
    assert_eq!(
        v["off"],
        serde_json::json!([1, 1]),
        "one click, one fetch per endpoint, also with Off"
    );
    assert_eq!(v["twice"], serde_json::json!([2, 2]));
}

#[test]
fn k4_one_request_per_endpoint_in_flight() {
    let v = run_refresh_script(
        "let release=null;\n\
         const a=make(store('1s'),{state:()=>new Promise(r=>{release=r})});a.rf.start();a.clock.advance(5000);\n\
         const during=a.calls.state;release();await new Promise(r=>setTimeout(r,0));a.clock.advance(1000);\n\
         console.log(JSON.stringify({during,after:a.calls.state}));",
    );
    assert_eq!(v["during"], 1, "no second /state while the first is open");
    assert_eq!(
        v["after"], 2,
        "the next tick fetches again once it answered"
    );
}

#[test]
fn k4_hidden_tab_pauses_and_catches_up() {
    let v = run_refresh_script(
        "const a=make(store('1s'));a.rf.start();a.clock.advance(5000);\n\
         const before=[a.calls.state,a.calls.history];\n\
         a.tab.visible=false;a.rf.visibility(false);a.clock.advance(120000);\n\
         const hidden=[a.calls.state,a.calls.history];\n\
         a.tab.visible=true;a.rf.visibility(true);\n\
         const back=[a.calls.state,a.calls.history];\n\
         a.clock.advance(1000);\n\
         const off=make(store('off'));off.rf.start();off.tab.visible=false;off.rf.visibility(false);off.tab.visible=true;off.rf.visibility(true);\n\
         console.log(JSON.stringify({before,hidden,back,next:a.calls.state,off:[off.calls.state,off.calls.history]}));",
    );
    assert_eq!(v["hidden"], v["before"], "a hidden tab fetches nothing");
    assert_eq!(
        v["back"],
        serde_json::json!([
            v["before"][0].as_i64().unwrap() + 1,
            v["before"][1].as_i64().unwrap() + 1
        ]),
        "one fetch of each when it shows again"
    );
    assert_eq!(
        v["next"],
        v["back"][0].as_i64().unwrap() + 1,
        "the timer runs again"
    );
    assert_eq!(
        v["off"],
        serde_json::json!([0, 0]),
        "Off has nothing to catch up on"
    );
}

#[test]
fn k4_choice_is_remembered() {
    let v = run_refresh_script(
        "const a=make(store('30s'));\n\
         const b=make(store('nonsense'));\n\
         const c=make(store(null));c.rf.set('10s');const saved=store.saved;\n\
         const bad=make({getItem:()=>{throw new Error('blocked')},setItem:()=>{throw new Error('blocked')}});\n\
         const blockedStart=bad.rf.key();bad.rf.set('5m');\n\
         console.log(JSON.stringify({stored:a.rf.key(),junk:b.rf.key(),saved,afterSet:c.rf.key(),blockedStart,blockedSet:bad.rf.key()}));",
    );
    assert_eq!(v["stored"], "30s", "survives a reload");
    assert_eq!(v["junk"], "1s");
    assert_eq!(v["saved"], serde_json::json!(["huddle-refresh", "10s"]));
    assert_eq!(v["afterSet"], "10s");
    assert_eq!(v["blockedStart"], "1s", "a throwing localStorage leaves 1s");
    assert_eq!(v["blockedSet"], "5m", "the choice holds for this page");
}

#[test]
fn k4_changing_the_choice_restarts_the_timers() {
    let v = run_refresh_script(
        "const a=make(store('1s'));a.rf.start();a.clock.advance(3000);a.rf.set('off');a.clock.advance(60000);\n\
         const stopped=a.calls.state;a.rf.set('5s');a.clock.advance(10000);\n\
         console.log(JSON.stringify({stopped,after:a.calls.state}));",
    );
    assert_eq!(v["stopped"], 3);
    assert_eq!(v["after"], 5, "5s adds two fetches in 10 s");
}

#[test]
fn k5_refresh_now_is_disabled_in_realtime() {
    let v = run_refresh_script(
        "const a=make(store('rt'));a.rf.start();\n\
         const on={disabled:a.rf.nowDisabled(),title:a.rf.nowTitle()};\n\
         const before=[a.calls.state,a.calls.history];a.rf.now();const click=[a.calls.state,a.calls.history];\n\
         a.rf.set('5s');\n\
         const off={disabled:a.rf.nowDisabled(),title:a.rf.nowTitle()};a.rf.now();\n\
         console.log(JSON.stringify({on,before,click,off,after:[a.calls.state,a.calls.history]}));",
    );
    assert_eq!(
        v["on"],
        serde_json::json!({"disabled": true, "title": "Realtime is on"})
    );
    assert_eq!(v["click"], v["before"], "a click fetches nothing");
    assert_eq!(
        v["off"],
        serde_json::json!({"disabled": false, "title": "Refresh now"})
    );
    assert_eq!(
        v["after"],
        serde_json::json!([
            v["before"][0].as_i64().unwrap() + 1,
            v["before"][1].as_i64().unwrap() + 1
        ]),
        "refresh-now works again after choosing 5s"
    );
}

#[test]
fn k5_realtime_opens_one_stream_and_leaving_closes_it() {
    let v = run_refresh_script(
        "const a=make(store('rt'));a.rf.start();\n\
         const opened=a.streams.length;a.streams[0].cb.ok();\n\
         a.clock.advance(60000);\n\
         const polled=a.calls.state,hist=a.calls.history;\n\
         a.rf.start();const restarted=[a.streams.length,a.streams[0].closed];\n\
         a.rf.set('off');const left=a.streams[a.streams.length-1].closed;\n\
         const b=make(store('rt'));b.rf.start();b.streams[0].cb.ok();b.tab.visible=false;b.rf.visibility(false);const hid=b.streams[0].closed;\n\
         console.log(JSON.stringify({opened,polled,hist,restarted,left,hid,label:a.rf.label()}));",
    );
    assert_eq!(v["opened"], 1, "one stream per tab");
    assert_eq!(v["polled"], 0, "no /state poll while the stream is live");
    assert_eq!(v["hist"], 12, "history panels follow the 5 s rule");
    assert_eq!(
        v["restarted"],
        serde_json::json!([2, true]),
        "a restart closes the old stream first"
    );
    assert_eq!(v["left"], true, "leaving Realtime closes the stream");
    assert_eq!(v["hid"], true, "a hidden tab closes the stream");
}

#[test]
fn k5_page_falls_back_to_polling_and_returns() {
    let v = run_refresh_script(
        "const a=make(store('rt'));a.rf.start();\n\
         const label0=a.rf.label();\n\
         a.streams[0].cb.fail();const label1=a.rf.label(),polls1=a.calls.state;\n\
         a.clock.advance(1000);a.streams[1].cb.fail();\n\
         a.clock.advance(2000);a.streams[2].cb.fail();\n\
         const polling0=a.calls.state;a.clock.advance(3000);const polling=a.calls.state-polling0;\n\
         const tries=a.streams.length;\n\
         a.clock.advance(4000);a.clock.advance(8000);a.clock.advance(10000);const delays=a.streams.length;\n\
         a.streams[a.streams.length-1].cb.ok();const back=a.rf.label();\n\
         const after0=a.calls.state;a.clock.advance(5000);\n\
         console.log(JSON.stringify({label0,label1,polls1,polling,tries,delays,back,stopped:a.calls.state-after0,changed:a.calls.changed>0}));",
    );
    assert_eq!(v["label0"], "Realtime");
    assert_eq!(v["label1"], "Realtime (reconnecting)");
    assert_eq!(v["polls1"], 0, "one failure does not poll yet");
    assert_eq!(
        v["polling"], 3,
        "after three failed tries the 1 s poll runs"
    );
    assert!(
        v["tries"].as_i64().unwrap() >= 3,
        "each failure schedules another try"
    );
    assert!(
        v["delays"].as_i64().unwrap() > v["tries"].as_i64().unwrap(),
        "tries go on while it is down"
    );
    assert_eq!(
        v["back"], "Realtime",
        "a success drops the reconnecting label"
    );
    assert_eq!(v["stopped"], 0, "a success stops the poll");
    assert_eq!(v["changed"], true, "the picker is told to repaint");
}

#[test]
fn k5_retry_delay_grows_up_to_ten_seconds() {
    let v = run_refresh_script(
        "const a=make(store('rt'));a.rf.start();const seen=[];\n\
         for(let i=0;i<7;i++){const n=a.streams.length;a.streams[n-1].cb.fail();\n\
          let waited=0;while(a.streams.length===n&&waited<20000){a.clock.advance(250);waited+=250}seen.push(waited)}\n\
         console.log(JSON.stringify({seen}));",
    );
    let seen: Vec<i64> = v["seen"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_i64().unwrap())
        .collect();
    assert!(
        seen.windows(2).all(|w| w[1] >= w[0]),
        "delays only grow: {seen:?}"
    );
    assert!(
        seen.iter().all(|d| *d <= 10_000),
        "no delay past 10 s: {seen:?}"
    );
    assert_eq!(
        *seen.last().unwrap(),
        10_000,
        "the delay settles at 10 s: {seen:?}"
    );
}

#[test]
fn k5_page_wires_the_stream_to_the_picker() {
    assert!(
        PAGE.contains("EventSource("),
        "the page opens the event stream"
    );
    assert!(PAGE.contains("/huddle-router/live"));
    assert!(PAGE.contains("Realtime is on"));
    assert!(PAGE.contains("aria-disabled"));
}

#[test]
fn l27_the_sessions_page_asks_for_its_listed_ids_only() {
    let core = include_str!("../src/dashboard/core.js");
    assert!(
        core.contains("'totals&by=session&models=0&'+sessionIds()+rangeQuery()"),
        "the session totals call names the listed ids"
    );
    assert!(
        core.contains("'sessions='+ids.map(encodeURIComponent).join(',')"),
        "ids are joined into one sessions list"
    );
}

fn marked_block(start: &str, end: &str) -> String {
    PAGE.split(start)
        .nth(1)
        .and_then(|s| s.split(end).next())
        .unwrap_or_else(|| panic!("the page holds a block between {start} and {end}"))
        .to_string()
}

/// Runs the time-picker code in bun in the machine zone UTC+2, with a stub page around it.
/// `body` sees `make(zone, saved)` where `zone` is `local` or `utc` and `saved` holds the stored
/// `huddle-range` and `huddle-range-custom` values; an optional third `raw` storage replaces the
/// in-memory store, for a store that throws. `make` returns the picker's functions plus
/// `els` (the stub elements by selector), `keys` (the document's keydown handlers), `saved` (what
/// was stored) and `log` (focus and render calls).
fn run_picker_script(body: &str) -> Value {
    use std::io::Write;
    use std::process::Stdio;
    let code = [
        zone_block(),
        marked_block("/* rs:start */", "/* rs:end */"),
        marked_block("/* rp:start */", "/* rp:end */"),
    ]
    .join("\n");
    let code = serde_json::to_string(&code).expect("block");
    let script = format!(
        "const CODE={code};\n\
         const num=v=>typeof v==='number'&&isFinite(v)?v:null;\n\
         const esc=s=>String(s==null?'':s).replace(/[&<>\"']/g,c=>({{'&':'&amp;','<':'&lt;','>':'&gt;','\"':'&quot;',\"'\":'&#39;'}}[c]));\n\
         function make(zone,saved,raw){{\n\
          const mem=Object.assign({{'huddle-tz':zone}},saved||{{}});const savedKeys={{}};\n\
          const localStorage=raw||{{getItem:k=>k in mem?mem[k]:null,setItem:(k,v)=>{{savedKeys[k]=v;mem[k]=v}}}};\n\
          const els={{}},keys=[],log=[];\n\
          const $=s=>els[s]||null,$$=()=>[];\n\
          const document={{addEventListener:(t,f)=>{{if(t==='keydown')keys.push(f)}}}};\n\
          const setHtml=(el,h)=>{{el.html=h}};\n\
          const stubs={{$,$$,document,setHtml,esc,num,localStorage,MENUS:{{}},MENU:null,CHEVRON:'',ic:()=>'',menuHtml:()=>'',menuDrop(){{}},\n\
           render(){{log.push('render')}},loadHistory(){{log.push('history')}},CH:{{focus:null,metric:'req'}},HT:{{}},DR:null,HS:{{mode:'live'}},page:'overview',\n\
           hget:()=>new Promise(()=>{{}})}};\n\
          const names=Object.keys(stubs);\n\
          const api=new Function(...names,CODE+';return {{HIST,RANGES,RANGE_MIN,DT,earliest,rangeButtonText,rangeText,rangeQuery,rangeBar,renderRangeBar,setRange,applyCustom,initPick,rangeClick,recentRanges,renderTzBar,shiftedRange,zoomedRange}}')(...names.map(n=>stubs[n]));\n\
          els['#rangebar']={{html:''}};els['#mb-range']={{focus(){{log.push('focus mb-range')}}}};els['#rfrom']={{focus(){{log.push('focus rfrom')}}}};els['#rerr']={{textContent:'',html:''}};\n\
          return Object.assign(api,{{els,keys,log,saved:savedKeys}})}}\n\
         (async()=>{{{body}}})();"
    );
    let mut child = bun()
        .args(["run", "-"])
        .env("TZ", "Etc/GMT-2")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start bun");
    child
        .stdin
        .take()
        .expect("bun stdin")
        .write_all(script.as_bytes())
        .expect("write script");
    let out = child.wait_with_output().expect("bun");
    assert!(
        out.status.success(),
        "bun failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).expect("json out")
}

#[test]
fn l37_the_top_bar_has_one_time_button_and_no_range_menu() {
    assert_eq!(PAGE.matches("id=\"mb-range\"").count(), 1);
    assert!(PAGE.contains("aria-haspopup=\"dialog\""));
    assert!(PAGE.contains("aria-controls=\"rpanel\""));
    assert!(PAGE.contains("role=\"dialog\" aria-label=\"Time range\""));
    assert!(
        !PAGE.contains("MENUS.range"),
        "the range is no listbox menu"
    );
    assert!(!PAGE.contains("{k:'custom',t:'Custom',sep:1}"));
    assert!(
        !PAGE.contains("rcancel"),
        "Esc and an outside click close it"
    );
    assert!(PAGE.contains("Absolute time range"));
}

#[test]
fn l37_the_button_reads_the_range() {
    let v = run_picker_script(
        "const out={};\n\
         for(const zone of ['local','utc']){const m=make(zone);const labels={};\n\
          for(const [k] of m.RANGES){m.HIST.range=k;labels[k]=m.rangeButtonText()}\n\
          const f=Date.UTC(2026,10,14,10,0),t=Date.UTC(2026,10,14,12,30);m.HIST.range='custom';m.HIST.from=f;m.HIST.to=t;\n\
          out[zone]={labels,custom:m.rangeButtonText(),text:m.rangeText()}}\n\
         console.log(JSON.stringify(out));",
    );
    let labels = serde_json::json!({
        "15m": "Last 15 minutes", "1h": "Last hour", "24h": "Last 24 hours",
        "7d": "Last 7 days", "30d": "Last 30 days", "90d": "Last 90 days"
    });
    assert_eq!(v["local"]["labels"], labels);
    assert_eq!(v["utc"]["labels"], labels);
    assert_eq!(v["local"]["custom"], "14 Nov 12:00 to 14 Nov 14:30");
    assert_eq!(v["utc"]["custom"], "14 Nov 10:00 to 14 Nov 12:30");
    assert_eq!(v["utc"]["text"], v["utc"]["custom"]);
}

#[test]
fn l37_a_quick_range_is_marked_and_applies() {
    let v = run_picker_script(
        "const m=make('utc',{'huddle-range':'7d'});m.HIST.keep=10;\n\
         m.HIST.picking=true;m.renderRangeBar();const html=m.els['#rangebar'].html;\n\
         const rq=[...html.matchAll(/<button[^>]*data-rq=\"([^\"]+)\"[^>]*>/g)].map(x=>({k:x[1],cur:x[0].includes('aria-current=\"true\"'),off:x[0].includes('disabled')}));\n\
         const click=k=>m.rangeClick({target:{closest:s=>s==='[data-rq]'?{dataset:{rq:k},disabled:false}:null}});\n\
         click('24h');\n\
         console.log(JSON.stringify({rq,range:m.HIST.range,stored:m.saved['huddle-range'],query:m.rangeQuery(),picking:m.HIST.picking,custom:m.saved['huddle-range-custom']??null}));",
    );
    let rq = v["rq"].as_array().expect("quick ranges");
    let keys: Vec<&str> = rq.iter().map(|r| r["k"].as_str().expect("key")).collect();
    assert_eq!(keys, ["15m", "1h", "24h", "7d", "30d", "90d"]);
    let current: Vec<&str> = rq
        .iter()
        .filter(|r| r["cur"] == true)
        .map(|r| r["k"].as_str().expect("key"))
        .collect();
    assert_eq!(current, ["7d"], "only the current range is marked");
    let off: Vec<&str> = rq
        .iter()
        .filter(|r| r["off"] == true)
        .map(|r| r["k"].as_str().expect("key"))
        .collect();
    assert_eq!(off, ["30d", "90d"], "ranges past the kept history are off");
    assert_eq!(v["range"], "24h");
    assert_eq!(v["stored"], "24h");
    assert_eq!(v["query"], "range=24h&tz_offset_min=0");
    assert_eq!(v["picking"], false);
    assert!(v["custom"].is_null());
}

#[test]
fn l37_apply_sends_the_same_custom_query() {
    let v = run_picker_script(
        "const m=make('local');const now=Date.now();\n\
         m.initPick();m.DT.from=now-3*3600e3;m.DT.to=now-3600e3;\n\
         const apply=()=>m.rangeClick({target:{closest:s=>s==='#rapply'?{}:null}});\n\
         apply();\n\
         const ok={range:m.HIST.range,stored:m.saved['huddle-range'],custom:JSON.parse(m.saved['huddle-range-custom']),query:m.rangeQuery(),from:m.DT.from,to:m.DT.to};\n\
         const n=make('local');n.HIST.picking=true;n.initPick();\n\
         n.DT.from=now-1000;n.DT.to=now-5000;n.rangeClick({target:{closest:s=>s==='#rapply'?{}:null}});\n\
         const after={html:n.els['#rerr'].html,range:n.HIST.range,stored:n.saved['huddle-range']??null};\n\
         n.DT.from=n.earliest()-864e5;n.DT.to=now;n.rangeClick({target:{closest:s=>s==='#rapply'?{}:null}});\n\
         const early={html:n.els['#rerr'].html,range:n.HIST.range,stored:n.saved['huddle-range']??null,picking:n.HIST.picking};\n\
         console.log(JSON.stringify({ok,after,early}));",
    );
    let ok = &v["ok"];
    assert_eq!(ok["range"], "custom");
    assert_eq!(ok["stored"], "custom");
    assert_eq!(ok["custom"]["from"], ok["from"]);
    assert_eq!(ok["custom"]["to"], ok["to"]);
    let query = format!("from={}&to={}&tz_offset_min=120", ok["from"], ok["to"]);
    assert_eq!(ok["query"], query.as_str());
    assert!(
        v["after"]["html"]
            .as_str()
            .expect("html")
            .contains("start that is before the end")
    );
    assert_eq!(v["after"]["range"], "15m");
    assert!(v["after"]["stored"].is_null());
    assert!(
        v["early"]["html"]
            .as_str()
            .expect("html")
            .contains("keeps 90 days")
    );
    assert_eq!(v["early"]["range"], "15m");
    assert!(v["early"]["stored"].is_null());
    assert_eq!(v["early"]["picking"], true);
}

#[test]
fn l37_a_saved_range_from_the_last_release_still_loads() {
    let v = run_picker_script(
        "const a=make('local',{'huddle-range':'7d'});\n\
         const b=make('local',{'huddle-range':'custom','huddle-range-custom':JSON.stringify({from:1000,to:2000})});\n\
         const c=make('local',{'huddle-range':'custom','huddle-range-custom':JSON.stringify({from:3000,to:2000})});\n\
         const d=make('local',{'huddle-range':'bogus'});\n\
         console.log(JSON.stringify([a,b,c,d].map(x=>({range:x.HIST.range,from:x.HIST.from,to:x.HIST.to,picking:x.HIST.picking}))));",
    );
    assert_eq!(v[0]["range"], "7d");
    assert_eq!(v[1]["range"], "custom");
    assert_eq!(v[1]["from"], 1000);
    assert_eq!(v[1]["to"], 2000);
    assert_eq!(v[2]["range"], "15m", "a reversed saved range is dropped");
    assert_eq!(v[3]["range"], "15m");
    assert_eq!(v[3]["picking"], false);
}

#[test]
fn l37_the_panel_closes_on_escape_and_outside_click_and_returns_focus() {
    let v = run_picker_script(
        "const m=make('utc');\n\
         const toggle=m.rangeClick({target:{closest:s=>s==='[data-rtoggle]'?{}:null}});\n\
         const opened={toggle,picking:m.HIST.picking,focus:m.log.filter(x=>x.startsWith('focus')),dialog:m.els['#rangebar'].html.includes('role=\"dialog\"'),expanded:m.els['#rangebar'].html.includes('aria-expanded=\"true\"')};\n\
         m.log.length=0;\n\
         const esc={key:'Escape',target:{closest:s=>s==='#rpanel'?{}:null},preventDefault(){},stopPropagation(){}};\n\
         m.keys.forEach(f=>f(esc));\n\
         const afterEsc={picking:m.HIST.picking,focus:m.log.filter(x=>x.startsWith('focus'))};\n\
         m.rangeClick({target:{closest:s=>s==='[data-rtoggle]'?{}:null}});m.log.length=0;\n\
         const inside=m.rangeClick({target:{closest:s=>s==='#rpanel'?{}:null}});\n\
         const stillOpen=m.HIST.picking;\n\
         const outside=m.rangeClick({target:{closest:()=>null}});\n\
         const afterOut={inside,stillOpen,outside,picking:m.HIST.picking,focus:m.log.filter(x=>x.startsWith('focus'))};\n\
         const idle=m.rangeClick({target:{closest:()=>null}});\n\
         console.log(JSON.stringify({opened,afterEsc,afterOut,idle}));",
    );
    assert_eq!(v["opened"]["picking"], true);
    assert_eq!(v["opened"]["dialog"], true);
    assert_eq!(v["opened"]["expanded"], true);
    assert_eq!(v["opened"]["focus"], serde_json::json!(["focus rfrom"]));
    assert_eq!(v["afterEsc"]["picking"], false);
    assert_eq!(
        v["afterEsc"]["focus"],
        serde_json::json!(["focus mb-range"])
    );
    assert_eq!(
        v["afterOut"]["stillOpen"], true,
        "a click inside keeps it open"
    );
    assert_eq!(v["afterOut"]["picking"], false);
    assert_eq!(
        v["afterOut"]["focus"],
        serde_json::json!(["focus mb-range"])
    );
    assert_eq!(v["idle"], false, "a click with the panel shut is not ours");
}

#[test]
fn l37_the_panel_stacks_at_phone_width() {
    let css = include_str!("../src/dashboard/style.css");
    let at = css
        .find("@media (max-width:700px){.rangepop")
        .expect("a 700px rule for the panel");
    let rule = &css[at..at + css[at..].find('\n').expect("end")];
    for part in [
        "grid-template-columns:minmax(0,1fr)",
        "left:0",
        "right:0",
        "overflow-y:auto",
    ] {
        assert!(rule.contains(part), "the phone rule holds {part}");
    }
    assert!(css.contains("grid-template-columns:280px 190px"));
}

/// The 2 s clock tick: the script between the `tk:start` and `tk:end` markers.
fn tick_block() -> String {
    PAGE.split("/* tk:start */")
        .nth(1)
        .and_then(|s| s.split("/* tk:end */").next())
        .expect("the tick sits between tk:start and tk:end")
        .to_string()
}

/// Runs the tick in bun on a fake clock. The block receives the formatters, `paint` and the
/// document as arguments, so `build` and the regions are not in its scope at all: a tick that
/// rebuilt a region would throw. `body` sees `make()` returning `{tick, tickArm, setBuilt, els,
/// paints, clock, ago, dur, clockOf}` and a shared fake clock advanced with `clock.advance(ms)`.
fn run_tick_script(body: &str) -> Value {
    use std::io::Write;
    use std::process::Stdio;
    let block = serde_json::to_string(&tick_block()).expect("block");
    let script = format!(
        "const BLOCK={block};\n\
         const clk={{t:1794614400000,timers:[],advance(ms){{const end=this.t+ms;for(;;){{let next=null;for(const x of this.timers)if(x.on&&(next===null||x.at<next.at))next=x;\n\
          if(!next||next.at>end)break;this.t=next.at;next.at+=next.ms;next.fn()}}this.t=end}}}};\n\
         Date.now=()=>clk.t;\n\
         let arms=0;\n\
         const agoS=t=>{{const d=clk.t-t;if(d<4e3)return 'now';if(d<6e4)return Math.floor(d/1e3)+'s ago';if(d<36e5)return Math.floor(d/6e4)+'m ago';return Math.floor(d/864e5)+'d ago'}};\n\
         const durS=d=>{{d=Math.max(0,d);if(d<6e4)return Math.floor(d/1e3)+'s';if(d<36e5)return Math.floor(d/6e4)+'m '+String(Math.floor(d%6e4/1e3)).padStart(2,'0')+'s';return Math.floor(d/36e5)+'h '+Math.floor(d%36e5/6e4)+'m'}};\n\
         const clockS=t=>{{const d=new Date(t),p=n=>String(n).padStart(2,'0');return p(d.getUTCHours())+':'+p(d.getUTCMinutes())+':'+p(d.getUTCSeconds())}};\n\
         function make(){{\n\
          const els={{clock:{{textContent:''}},marked:[]}};const paints={{n:0}};const listeners={{}};\n\
          const doc={{visibilityState:'visible',getElementById:id=>id==='clock'?els.clock:null,\n\
           querySelectorAll:s=>s==='[data-t]'?els.marked:[],addEventListener:(t,f)=>{{listeners[t]=f}}}};\n\
          const made=new Function('ago','dur','clock','SEC','OFFSET','NOW','BUILT_AT','paint','document','setInterval','clearInterval',\n\
           BLOCK+';return {{tick,tickArm,setBuilt:v=>{{BUILT_AT=v}}}}')(\n\
           agoS,durS,clockS,1000,0,clk.t,0,()=>{{paints.n++}},doc,\n\
           (fn,ms)=>{{arms++;const x={{fn,ms,at:clk.t+ms,on:true}};clk.timers.push(x);return x}},\n\
           h=>{{if(h)h.on=false}});\n\
          return {{tick:made.tick,tickArm:made.tickArm,setBuilt:made.setBuilt,els,paints,doc,listeners,clock:clk,ago:agoS,dur:durS,clockOf:clockS}};\n\
         }}\n\
         (async()=>{{{body}}})();"
    );
    let mut child = bun()
        .args(["run", "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start bun");
    child
        .stdin
        .take()
        .expect("bun stdin")
        .write_all(script.as_bytes())
        .expect("write script");
    let out = child.wait_with_output().expect("bun");
    assert!(
        out.status.success(),
        "bun failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).expect("json out")
}

#[test]
fn l31_a_tick_does_not_rebuild_regions_with_no_new_data() {
    let block = tick_block();
    for call in ["build(", "update("] {
        assert!(
            !block.contains(call),
            "the tick never calls {call}: the name is not even in its scope"
        );
    }
    let v = run_tick_script(
        "const m=make();\n\
         const t0=m.clock.t;m.setBuilt(t0-60000);\n\
         m.els.clock.textContent='clock-initial';\n\
         m.els.marked=[{dataset:{t:String(t0-30000),f:'a'},textContent:'x'},\n\
          {dataset:{t:String(t0-90000),f:'d'},textContent:'x'},\n\
          {dataset:{t:String(t0+45000),f:'r'},textContent:'x'}];\n\
         const before=m.els.clock.textContent;\n\
         const v0=m.els.marked.map(x=>x.textContent);\n\
         m.clock.advance(2000);\n\
         const v1=m.els.marked.map(x=>x.textContent);\n\
         m.clock.advance(18000);\n\
         const v10=m.els.marked.map(x=>x.textContent);\n\
         console.log(JSON.stringify({before,clock:m.els.clock.textContent,v0,v1,v10,\n\
          expected:[m.ago(t0-30000),m.dur(m.clock.t-(t0-90000)),m.dur((t0+45000)-m.clock.t)],\n\
          paints:m.paints.n,clockExpected:m.clockOf(m.clock.t)}));",
    );
    assert_eq!(v["paints"], 0, "10 ticks repaint no region");
    assert_eq!(v["v0"].as_array().unwrap().len(), 3);
    assert_ne!(v["before"], v["clock"], "the clock text moves");
    assert_eq!(v["clock"], v["clockExpected"]);
    let v0: Vec<&str> = v["v0"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_str().unwrap())
        .collect();
    let v1: Vec<&str> = v["v1"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_str().unwrap())
        .collect();
    let v10: Vec<&str> = v["v10"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_str().unwrap())
        .collect();
    let expected: Vec<&str> = v["expected"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_str().unwrap())
        .collect();
    for i in 0..3 {
        assert_eq!(v0[i], "x", "tick {i} never ran on an unmarked span");
        assert_ne!(
            v1[i], v0[i],
            "the first tick moved the text: {}",
            expected[i]
        );
        assert_ne!(v1[i], v10[i], "the time text advanced: {}", expected[i]);
        assert_eq!(v10[i], expected[i], "the tick re-renders the text");
    }
}

#[test]
fn l31_a_snapshot_inside_the_last_2_seconds_skips_the_tick() {
    let v = run_tick_script(
        "const m=make();\n\
         const t0=m.clock.t;\n\
         m.els.clock.textContent='c0';\n\
         m.els.marked=[{dataset:{t:String(t0-30000),f:'a'},textContent:'a0'}];\n\
         m.setBuilt(t0-500);\n\
         m.tick();\n\
         const skipped=[m.els.clock.textContent,m.els.marked[0].textContent,m.paints.n];\n\
         m.setBuilt(t0-2500);\n\
         m.tick();\n\
         const ran=[m.els.clock.textContent,m.els.marked[0].textContent,m.paints.n];\n\
         console.log(JSON.stringify({skipped,ran}));",
    );
    assert_eq!(
        v["skipped"],
        serde_json::json!(["c0", "a0", 0]),
        "a snapshot 0.5 s ago skips the tick"
    );
    assert_eq!(
        v["ran"][0], "00:00:00",
        "outside the window the clock moves"
    );
    assert_eq!(v["ran"][1], "30s ago", "and the time texts move");
    assert_eq!(
        v["ran"][2], v["skipped"][2],
        "a tick that only moves texts never repaints"
    );
}

#[test]
fn l31_a_hidden_tab_runs_no_timer() {
    let v = run_tick_script(
        "const m=make();\n\
         const t0=m.clock.t;m.setBuilt(t0-60000);\n\
         m.clock.advance(2000);\n\
         const armed=m.els.clock.textContent;\n\
         m.doc.visibilityState='hidden';m.listeners.visibilitychange();\n\
         const hiddenAt=m.els.clock.textContent;m.clock.advance(60000);\n\
         const hidden=m.els.clock.textContent;\n\
         m.doc.visibilityState='visible';m.listeners.visibilitychange();\n\
         m.clock.advance(2000);\n\
         const back=m.els.clock.textContent;\n\
         m.doc.visibilityState='hidden';const directAt=m.els.clock.textContent;m.tick();\n\
         const direct=m.els.clock.textContent;\n\
         m.doc.visibilityState='visible';\n\
         const armCalls=arms;m.tickArm(true);m.tickArm(true);\n\
         console.log(JSON.stringify({armed,hiddenAt,hidden,back,directAt,direct,armCalls,armsAfter:arms}));",
    );
    assert_ne!(v["armed"], "", "the interval is armed");
    assert_eq!(v["hidden"], v["hiddenAt"], "a hidden tab holds no ticks");
    assert_ne!(v["back"], v["hidden"], "a tab that shows again ticks");
    assert_eq!(v["direct"], v["directAt"], "the tick skips a hidden tab");
    assert_eq!(
        v["armsAfter"], v["armCalls"],
        "tickArm(true) never arms a second interval"
    );
    assert!(
        PAGE.contains("tickArm(document.visibilityState==='visible')"),
        "the page arms and disarms the tick with the tab's visibility"
    );
    assert!(
        !PAGE.contains("update(SNAP),2000"),
        "the old full repaint interval is gone"
    );
}

#[test]
fn l38_applying_a_custom_range_remembers_it() {
    let v = run_picker_script(
        "const m=make('local');const now=Date.now();const h=3600e3;\n\
         const a={from:now-3*h,to:now-h},b={from:now-5*h,to:now-4*h};\n\
         m.initPick();m.DT.from=a.from;m.DT.to=a.to;m.applyCustom();\n\
         const one=JSON.parse(m.saved['huddle-range-recent']);\n\
         m.initPick();m.DT.from=b.from;m.DT.to=b.to;m.applyCustom();\n\
         const two=JSON.parse(m.saved['huddle-range-recent']);\n\
         m.initPick();m.DT.from=a.from;m.DT.to=a.to;m.applyCustom();\n\
         const three=JSON.parse(m.saved['huddle-range-recent']);\n\
         console.log(JSON.stringify({one,two,three,a,b}));",
    );
    assert_eq!(v["one"], serde_json::json!([v["a"]]));
    assert_eq!(
        v["two"],
        serde_json::json!([v["b"], v["a"]]),
        "the newest range comes first"
    );
    assert_eq!(
        v["three"],
        serde_json::json!([v["a"], v["b"]]),
        "a repeat moves to the front without a duplicate"
    );
}

#[test]
fn l38_only_four_recent_ranges_are_kept() {
    let v = run_picker_script(
        "const m=make('local');const now=Date.now();const h=3600e3;\n\
         const spans=[[2,1],[3,2],[4,3],[5,4],[6,5]].map(([f,t])=>({from:now-f*h,to:now-t*h}));\n\
         spans.forEach(s=>{m.initPick();m.DT.from=s.from;m.DT.to=s.to;m.applyCustom()});\n\
         const kept=JSON.parse(m.saved['huddle-range-recent']);\n\
         console.log(JSON.stringify({kept,expected:[spans[4],spans[3],spans[2],spans[1]]}));",
    );
    assert_eq!(
        v["kept"], v["expected"],
        "five applies leave the four newest, oldest gone"
    );
}

#[test]
fn l38_a_failed_apply_and_a_quick_range_add_nothing() {
    let v = run_picker_script(
        "const m=make('local');const now=Date.now();const h=3600e3;\n\
         m.initPick();m.DT.from=now-3*h;m.DT.to=now-h;m.applyCustom();\n\
         const before=JSON.parse(m.saved['huddle-range-recent']);\n\
         m.initPick();m.DT.from=now-1000;m.DT.to=now-5000;m.applyCustom();\n\
         m.DT.from=m.earliest()-864e5;m.DT.to=now;m.applyCustom();\n\
         m.setRange('24h');\n\
         console.log(JSON.stringify({before,after:JSON.parse(m.saved['huddle-range-recent']),range:m.HIST.range}));",
    );
    assert_eq!(
        v["after"], v["before"],
        "From after To, From before earliest and a quick range add nothing"
    );
    assert_eq!(v["range"], "24h");
}

#[test]
fn l38_a_recent_range_applies_on_click() {
    let v = run_picker_script(
        "const from=Date.UTC(2026,10,14,8,0),to=Date.UTC(2026,10,14,10,0),oldFrom=Date.now()-200*864e5,oldTo=oldFrom+3600e3;\n\
         const m=make('utc',{'huddle-range-recent':JSON.stringify([{from:from,to:to},{from:oldFrom,to:oldTo}])});\n\
         m.HIST.picking=true;m.renderRangeBar();const html=m.els['#rangebar'].html;\n\
         const listed=html.includes('Recently used absolute ranges')&&html.includes('14 Nov 08:00 to 14 Nov 10:00');\n\
         const click=i=>m.rangeClick({target:{closest:s=>s==='[data-rr]'?{dataset:{rr:String(i)}}:null}});\n\
         click(0);\n\
         const applied={range:m.HIST.range,from:m.HIST.from,to:m.HIST.to,query:m.rangeQuery(),stored:JSON.parse(m.saved['huddle-range-recent']),picking:m.HIST.picking};\n\
         m.HIST.picking=true;m.renderRangeBar();m.els['#rerr'].html='';click(1);\n\
         const rejected={err:m.els['#rerr'].html,range:m.HIST.range,from:m.HIST.from,to:m.HIST.to,stored:JSON.parse(m.saved['huddle-range-recent'])};\n\
         console.log(JSON.stringify({listed,applied,rejected,from,to,oldFrom,oldTo}));",
    );
    assert_eq!(v["listed"], true, "the panel lists the stored ranges");
    let applied = &v["applied"];
    assert_eq!(applied["range"], "custom");
    assert_eq!(applied["from"], v["from"]);
    assert_eq!(applied["to"], v["to"]);
    let query = format!("from={}&to={}&tz_offset_min=0", v["from"], v["to"]);
    assert_eq!(
        applied["query"],
        query.as_str(),
        "the same query shape as Apply"
    );
    assert_eq!(
        applied["stored"],
        serde_json::json!([
            {"from": v["from"], "to": v["to"]},
            {"from": v["oldFrom"], "to": v["oldTo"]}
        ])
    );
    let rejected = &v["rejected"];
    assert!(
        rejected["err"]
            .as_str()
            .expect("error line")
            .contains("keeps 90 days"),
        "a row older than the kept history shows the error"
    );
    assert_eq!(rejected["range"], "custom", "the old row applies nothing");
    assert_eq!(rejected["from"], v["from"]);
    assert_eq!(rejected["to"], v["to"]);
    assert_eq!(
        rejected["stored"], applied["stored"],
        "the store is untouched"
    );
}

#[test]
fn l38_a_blocked_or_bad_store_is_an_empty_list() {
    let v = run_picker_script(
        "const blocked=make('local',null,{getItem:()=>{throw new Error('blocked')},setItem:()=>{throw new Error('blocked')}});\n\
         const bad=make('local',{'huddle-range-recent':'not json'});\n\
         const empty=make('local',{'huddle-range-recent':null});\n\
         const clean=m=>{m.HIST.picking=true;m.renderRangeBar();return !m.els['#rangebar'].html.includes('Recently used absolute ranges')};\n\
         const rows={blocked:clean(blocked),bad:clean(bad),empty:clean(empty)};\n\
         blocked.initPick();blocked.DT.from=Date.now()-3600e3;blocked.DT.to=Date.now()-1800e3;\n\
         let threw='no';try{blocked.applyCustom()}catch(e){threw='yes'}\n\
         console.log(JSON.stringify({rows,lists:{blocked:blocked.recentRanges(),bad:bad.recentRanges(),empty:empty.recentRanges()},threw,range:blocked.HIST.range,query:blocked.rangeQuery()}));",
    );
    assert_eq!(
        v["rows"],
        serde_json::json!({"blocked":true,"bad":true,"empty":true}),
        "no rows render from any bad store"
    );
    assert_eq!(v["lists"]["blocked"], serde_json::json!([]));
    assert_eq!(v["lists"]["bad"], serde_json::json!([]));
    assert_eq!(v["lists"]["empty"], serde_json::json!([]));
    assert_eq!(v["threw"], "no", "a throwing store does not stop Apply");
    assert_eq!(v["range"], "custom");
    let query = v["query"].as_str().expect("query");
    assert!(
        query.starts_with("from=")
            && query.contains("&to=")
            && query.ends_with("&tz_offset_min=120"),
        "{query}"
    );
}

#[test]
fn l38_the_zone_lives_in_the_panel_footer() {
    assert!(!PAGE.contains("tzbar"), "the header switch is gone");
    assert!(
        PAGE.contains("id=\"tzfoot\""),
        "the footer lives in the panel"
    );
    let v = run_picker_script(
        "const out={};\n\
         for(const zone of ['local','utc']){const m=make(zone);\n\
          m.HIST.picking=true;m.renderRangeBar();const html=m.els['#rangebar'].html;\n\
          const name=Intl.DateTimeFormat().resolvedOptions().timeZone||'';\n\
          out[zone]={hasLocal:html.includes('Local (UTC+2)'),hasUTC:html.includes('>UTC</button>'),\n\
           localOn:html.includes('data-tz=\"local\" aria-pressed=\"true\"'),utcOn:html.includes('data-tz=\"utc\" aria-pressed=\"true\"'),\n\
           zone:name,zoneShown:name?html.includes(name):null};\n\
          m.rangeClick({target:{closest:s=>s==='[data-tz]'?{dataset:{tz:zone==='local'?'utc':'local'}}:null}});\n\
          out[zone].stored=m.saved['huddle-tz'];out[zone].query=m.rangeQuery();out[zone].picking=m.HIST.picking}\n\
         const shut=make('local');let bootThrew='no';try{shut.renderTzBar()}catch(e){bootThrew='yes'}\n\
         out.boot={bootThrew,html:shut.els['#rangebar'].html};\n\
         console.log(JSON.stringify(out));",
    );
    for zone in ["local", "utc"] {
        let side = &v[zone];
        assert_eq!(
            side["hasLocal"], true,
            "{zone}: the Local button shows the zone and offset"
        );
        assert_eq!(side["hasUTC"], true, "{zone}: the UTC button shows");
        assert_eq!(
            side["localOn"],
            zone == "local",
            "{zone}: the current zone is pressed"
        );
        assert_eq!(
            side["utcOn"],
            zone == "utc",
            "{zone}: the other zone is not pressed"
        );
        if side["zone"].as_str().is_some_and(|z| !z.is_empty()) {
            assert_eq!(
                side["zoneShown"], true,
                "{zone}: the browser zone name shows"
            );
        }
        assert_eq!(
            side["picking"], false,
            "{zone}: choosing re-renders and closes like Apply"
        );
    }
    assert_eq!(v["local"]["stored"], "utc");
    assert_eq!(
        v["local"]["query"], "range=15m&tz_offset_min=0",
        "choosing UTC refetches at offset 0"
    );
    assert_eq!(v["utc"]["stored"], "local");
    assert_eq!(v["utc"]["query"], "range=15m&tz_offset_min=120");
    assert_eq!(
        v["boot"]["bootThrew"], "no",
        "renderTzBar with the panel shut is a no-op"
    );
    assert_eq!(v["boot"]["html"], "");
}

#[test]
fn l38_the_panel_flips_to_the_right_edge_when_it_would_clip() {
    let css = include_str!("../src/dashboard/style.css");
    assert!(
        css.contains(".rangepop.right{left:auto;right:0}"),
        "the panel hangs from the button's right corner"
    );
    assert!(
        css.contains(".rangepop,.rangepop.right{position:fixed"),
        "the phone rule keeps the full-width panel"
    );
    let render = PAGE
        .split("function renderRangeBar(){")
        .nth(1)
        .and_then(|s| s.split('}').next())
        .expect("renderRangeBar");
    assert!(
        render.contains("getBoundingClientRect().right>innerWidth-8")
            && render.contains("classList.add('right')"),
        "renderRangeBar flips the panel when it would pass the right edge: {render}"
    );
}

/// T: 2026-11-14T12:00:00Z, the fixed `Date.now` the shift tests run at.
const L39_T: i64 = 1_794_657_600_000;
const L39_HOUR: i64 = 3_600_000;
const L39_DAY: i64 = 86_400_000;

#[test]
fn l39_shift_back_moves_a_quick_range_by_its_own_length() {
    let v = run_picker_script(
        "const m=make('local');const T=Date.UTC(2026,10,14,12,0);Date.now=()=>T;\n\
         m.HIST.range='24h';\n\
         const r=m.shiftedRange(-1);\n\
         m.rangeClick({target:{closest:s=>s.includes('#rshl')?{id:'rshl'}:null}});\n\
         console.log(JSON.stringify({r,range:m.HIST.range,from:m.HIST.from,to:m.HIST.to,\n\
          query:m.rangeQuery(),stored:m.saved['huddle-range'],\n\
          custom:JSON.parse(m.saved['huddle-range-custom']??'null'),recent:m.saved['huddle-range-recent']??null,\n\
          bar:m.els['#rangebar'].html.includes('id=\"rshl\"')}));",
    );
    let from = L39_T - 48 * L39_HOUR;
    let to = L39_T - 24 * L39_HOUR;
    assert_eq!(
        v["r"],
        serde_json::json!({"from": from, "to": to}),
        "« moves a 24h range back by its own length"
    );
    assert_eq!(v["range"], "custom", "the shift makes the range custom");
    assert_eq!(v["from"], from);
    assert_eq!(v["to"], to);
    assert_eq!(
        v["query"],
        format!("from={from}&to={to}&tz_offset_min=120"),
        "the shift applies as the same custom query"
    );
    assert_eq!(v["stored"], "custom");
    assert_eq!(v["custom"], serde_json::json!({"from": from, "to": to}));
    assert_eq!(v["bar"], true, "the re-rendered bar holds the buttons");
}

#[test]
fn l39_shift_forward_is_off_when_the_range_ends_at_now() {
    let v = run_picker_script(
        "const m=make('local');const T=Date.UTC(2026,10,14,12,0);Date.now=()=>T;\n\
         const tag=(h,id)=>{const x=h.match(new RegExp('<button[^>]*id=\"'+id+'\"[^>]*>'));return x?x[0]:''};\n\
         const states={};\n\
         for(const [k] of m.RANGES){m.HIST.range=k;m.renderRangeBar();const h=m.els['#rangebar'].html;\n\
          states[k]={shr:tag(h,'rshr').includes('disabled'),shl:tag(h,'rshl').includes('disabled')}}\n\
         m.HIST.range='custom';m.HIST.from=T-4*3600e3;m.HIST.to=T-2*3600e3;m.renderRangeBar();\n\
         const earlierOff=tag(m.els['#rangebar'].html,'rshr').includes('disabled');\n\
         m.els['#rshr']={disabled:true,focus(){m.log.push('focus rshr')}};\n\
         m.rangeClick({target:{closest:s=>s.includes('#rshr')?{id:'rshr'}:null}});\n\
         const stopped={from:m.HIST.from,to:m.HIST.to,focus:m.log[m.log.length-1]};\n\
         m.HIST.from=T-6*3600e3;m.HIST.to=T-4*3600e3;m.els['#rshr'].disabled=false;\n\
         m.rangeClick({target:{closest:s=>s.includes('#rshr')?{id:'rshr'}:null}});\n\
         const kept={from:m.HIST.from,to:m.HIST.to,focus:m.log[m.log.length-1]};\n\
         m.HIST.from=T-2*3600e3;m.HIST.to=T;\n\
         m.rangeClick({target:{closest:s=>s.includes('#rshr')?{id:'rshr'}:null}});\n\
         const refused=m.HIST.from===T-2*3600e3&&m.HIST.to===T;\n\
         console.log(JSON.stringify({states,earlierOff,stopped,kept,refused}));",
    );
    for key in ["15m", "1h", "24h", "7d", "30d", "90d"] {
        assert_eq!(
            v["states"][key]["shr"], true,
            "{key}: » is off while the range ends at now"
        );
        assert_eq!(
            v["states"][key]["shl"],
            key == "90d",
            "{key}: « is on until the range starts at the kept history"
        );
    }
    assert_eq!(
        v["earlierOff"], false,
        "an earlier custom range shifts forward"
    );
    assert_eq!(
        v["stopped"],
        serde_json::json!({"from": L39_T-2*L39_HOUR, "to": L39_T, "focus": "focus mb-range"}),
        "the shift stops at now and focus moves to the time button once » is off"
    );
    assert_eq!(
        v["kept"],
        serde_json::json!({"from": L39_T-4*L39_HOUR, "to": L39_T-2*L39_HOUR, "focus": "focus rshr"}),
        "focus stays on » while it stays usable"
    );
    assert_eq!(
        v["refused"], true,
        "a range already ending at now shifts nowhere"
    );
}

#[test]
fn l39_shift_back_stops_at_the_earliest_kept_history() {
    let v = run_picker_script(
        "const m=make('local');const T=Date.UTC(2026,10,14,12,0);Date.now=()=>T;\n\
         const tag=(h,id)=>{const x=h.match(new RegExp('<button[^>]*id=\"'+id+'\"[^>]*>'));return x?x[0]:''};\n\
         m.HIST.keep=7;m.HIST.range='7d';\n\
         m.renderRangeBar();\n\
         const quickBtn=tag(m.els['#rangebar'].html,'rshl').includes('disabled');\n\
         m.HIST.range='custom';m.HIST.from=T-5*864e5;m.HIST.to=T-864e5;\n\
         const partial=m.shiftedRange(-1);\n\
         console.log(JSON.stringify({quickBtn,partial}));",
    );
    assert_eq!(
        v["quickBtn"], true,
        "a 7d range with 7 kept days renders « disabled"
    );
    assert_eq!(
        v["partial"],
        serde_json::json!({"from": L39_T-7*L39_DAY, "to": L39_T-3*L39_DAY}),
        "the shift stops at earliest() and keeps its length"
    );
}

#[test]
fn l39_zoom_out_doubles_the_range_around_its_centre() {
    let v = run_picker_script(
        "const m=make('local');const T=Date.UTC(2026,10,14,12,0);Date.now=()=>T;\n\
         const tag=(h,id)=>{const x=h.match(new RegExp('<button[^>]*id=\"'+id+'\"[^>]*>'));return x?x[0]:''};\n\
         m.HIST.range='24h';\n\
         const quick=m.zoomedRange();\n\
         m.HIST.range='custom';m.HIST.from=T-3*3600e3;m.HIST.to=T-3600e3;\n\
         const past=m.zoomedRange();\n\
         m.HIST.from=T-1001;m.HIST.to=T;\n\
         const rounded=m.zoomedRange();\n\
         m.HIST.keep=7;m.HIST.from=T-7*864e5;m.HIST.to=T;m.renderRangeBar();\n\
         const allOff=tag(m.els['#rangebar'].html,'rzoom').includes('disabled');\n\
         console.log(JSON.stringify({quick,past,rounded,allOff}));",
    );
    assert_eq!(
        v["quick"],
        serde_json::json!({"from": L39_T-36*L39_HOUR, "to": L39_T}),
        "a range touching now doubles back and stays at now"
    );
    assert_eq!(
        v["past"],
        serde_json::json!({"from": L39_T-4*L39_HOUR, "to": L39_T})
    );
    assert_eq!(
        v["rounded"],
        serde_json::json!({"from": L39_T-1501, "to": L39_T}),
        "a half-millisecond centre rounds to whole milliseconds"
    );
    assert_eq!(
        v["allOff"], true,
        "a range covering all kept history disables zoom out"
    );
}

#[test]
fn l39_a_shifted_range_survives_a_reload() {
    let v = run_picker_script(
        "const m=make('local');const T=Date.UTC(2026,10,14,12,0);Date.now=()=>T;\n\
         m.HIST.range='24h';\n\
         m.rangeClick({target:{closest:s=>s.includes('#rshl')?{id:'rshl'}:null}});\n\
         const savedRange=m.saved['huddle-range'],savedCustom=m.saved['huddle-range-custom'];\n\
         const m2=make('local',{'huddle-range':savedRange,'huddle-range-custom':savedCustom});\n\
         console.log(JSON.stringify({savedRange,range:m2.HIST.range,from:m2.HIST.from,to:m2.HIST.to,\n\
          match:m2.HIST.from===m.HIST.from&&m2.HIST.to===m.HIST.to}));",
    );
    assert_eq!(
        v["savedRange"], "custom",
        "the store holds huddle-range=custom"
    );
    assert_eq!(v["range"], "custom");
    assert_eq!(v["from"], L39_T - 48 * L39_HOUR);
    assert_eq!(v["to"], L39_T - 24 * L39_HOUR);
    assert_eq!(v["match"], true, "loading the store gives the same HIST");
}

#[test]
fn l39_a_shift_adds_nothing_to_recent_ranges() {
    let v = run_picker_script(
        "const m=make('local');const T=Date.UTC(2026,10,14,12,0);Date.now=()=>T;\n\
         m.HIST.range='24h';\n\
         m.rangeClick({target:{closest:s=>s.includes('#rshl')?{id:'rshl'}:null}});\n\
         const afterShift=m.saved['huddle-range-recent']??null;\n\
         m.initPick();m.DT.from=T-3*3600e3;m.DT.to=T-2*3600e3;m.applyCustom();\n\
         const afterApply=JSON.parse(m.saved['huddle-range-recent']??'null');\n\
         console.log(JSON.stringify({afterShift,afterApply}));",
    );
    assert_eq!(
        v["afterShift"],
        serde_json::value::Value::Null,
        "a shift stores no recent range"
    );
    assert_eq!(
        v["afterApply"],
        serde_json::json!([{"from": L39_T-3*L39_HOUR, "to": L39_T-2*L39_HOUR}]),
        "a plain Apply still does"
    );
}

#[test]
fn l39_the_buttons_are_labelled_and_stay_in_the_bar() {
    for label in ["Shift range back", "Shift range forward", "Zoom out"] {
        assert!(PAGE.contains(&format!("'{label}'")), "{label} is labelled");
    }
    assert!(
        PAGE.contains("aria-label=\"${lab}\" title=\"${lab}\""),
        "the buttons carry the label as both aria-label and title"
    );
    let pos = |s: &str| PAGE.find(s).unwrap_or_else(|| panic!("missing {s}"));
    assert!(
        pos("rs('rshl'") < pos("id=\"mb-range\"")
            && pos("id=\"mb-range\"") < pos("rs('rshr'")
            && pos("rs('rshr'") < pos("rs('rzoom'"),
        "the buttons sit around the time button"
    );
    let css = include_str!("../src/dashboard/style.css");
    assert!(
        css.contains("#rshl{border-radius:8px 0 0 8px}"),
        "the first button rounds the group's left edge"
    );
    assert!(
        css.contains(".rfnow.off,.rfnow.off:hover{"),
        "a disabled shift button reads inert like the refresh button"
    );
    assert!(
        css.contains(".refbar,.rfnow,.rangebar,.mchev{flex:none}"),
        "the buttons keep their size on a phone"
    );
    assert!(
        css.contains(".ctlgroup .rangebar .mval{max-width:11ch}"),
        "the time label shrinks first at phone width"
    );
    let v = run_picker_script(
        "const m=make('local');m.HIST.picking=true;m.renderRangeBar();const h=m.els['#rangebar'].html;\n\
         const labels=['Shift range back','Shift range forward','Zoom out'].map(l=>h.includes('aria-label=\"'+l+'\"'));\n\
         const order=['id=\"rshl\"','id=\"mb-range\"','id=\"rshr\"','id=\"rzoom\"'].map(s=>h.indexOf(s));\n\
         console.log(JSON.stringify({labels,order}));",
    );
    assert_eq!(v["labels"], serde_json::json!([true, true, true]));
    let order: Vec<i64> = v["order"]
        .as_array()
        .expect("order")
        .iter()
        .map(|x| x.as_i64().expect("position"))
        .collect();
    assert!(
        order.iter().all(|p| *p >= 0) && order.windows(2).all(|w| w[0] < w[1]),
        "the bar renders «, the time button, » and zoom out in order: {order:?}"
    );
}

/// Runs the page's session mapping and its snapshot entry point in bun. `body` sees
/// `run(snapshot)`, which returns the sessions and worktrees the page ends up with.
fn run_machine_script(body: &str) -> Value {
    use std::io::Write;
    use std::process::Stdio;
    let build = serde_json::to_string(page_between(
        "function buildSessions(){",
        "function buildRequests(){",
    ))
    .expect("block");
    let entry = serde_json::to_string(page_between("function onSnapshot(s){", "\n")).expect("line");
    let script = format!(
        "const num=v=>typeof v==='number'&&isFinite(v)?v:null,arr=v=>Array.isArray(v)?v:[];\n\
         let SNAP={{}},SESS=[],AGENTS=[],MACH=null,HAVE_RECENT=false,OFFSET=0,LOST=false,seen=null;\n\
         const update=s=>{{SNAP=s;buildSessions();seen={{sessions:SESS.map(s=>[s.id,s.state,s.waitingSince]),worktrees:SNAP.worktrees}}}};\n\
         eval({build}+{entry}+';globalThis.onSnapshot=onSnapshot;globalThis.buildSessions=buildSessions');\n\
         const run=s=>{{onSnapshot(s);return seen}};\n\
         (async()=>{{{body}}})();"
    );
    let mut child = bun()
        .args(["run", "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start bun");
    child
        .stdin
        .take()
        .expect("bun stdin")
        .write_all(script.as_bytes())
        .expect("write script");
    let out = child.wait_with_output().expect("bun");
    assert!(
        out.status.success(),
        "bun failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).expect("json out")
}

#[test]
fn l30_the_page_renders_the_same_from_machine_and_snapshot_events() {
    let v = run_machine_script(
        "const sessions=[{id:'s1',state:'your_turn',state_since_ms:5},{id:'s2',state:'needs_you',state_since_ms:9}];\n\
         const worktrees=[{path:'/w'}];\n\
         const inFlight=[{id:1,session:'s1'}];\n\
         const full=run({now_ms:1,in_flight:inFlight,sessions,worktrees});\n\
         MACH={sessions,worktrees};\n\
         const stream=run({now_ms:1,in_flight:inFlight});\n\
         const idle=run({now_ms:2,in_flight:[]});\n\
         console.log(JSON.stringify({same:JSON.stringify(full)===JSON.stringify(stream),s1:stream.sessions[0],idle:idle.sessions[0]}));",
    );
    assert_eq!(
        v["same"], true,
        "a stream snapshot and a /state snapshot build the same page"
    );
    assert_eq!(v["s1"], serde_json::json!(["s1", "working", null]));
    assert_eq!(v["idle"], serde_json::json!(["s1", "your_turn", 5]));
}

#[test]
fn l32_an_evicted_page_polls_instead_of_reconnecting() {
    let v = run_refresh_script(
        "const a=make(store('rt'));a.rf.start();a.streams[0].cb.ok();\n\
         const state0=a.calls.state;\n\
         a.streams[0].cb.evicted();\n\
         a.clock.advance(30000);\n\
         console.log(JSON.stringify({opened:a.streams.length,closed:a.streams[0].closed,polls:a.calls.state-state0,label:a.rf.label()}));",
    );
    assert_eq!(v["opened"], 1, "no stream is opened after the eviction");
    assert_eq!(v["closed"], true, "the evicted stream is closed");
    assert_eq!(v["polls"], 6, "a poll runs every 5 s for 30 s");
    assert_eq!(
        v["label"], "Realtime",
        "the page does not claim to reconnect"
    );
}
