//! The plan for the `env` object of Claude Code's settings.json. Pure: no file work.

use huddle_config::paths::DEFAULT_ROUTER_PORT;

/// Where Claude Code's traffic goes: through the router proxy, or the base URL.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RouteMode {
    Proxy,
    BaseUrl,
}

/// What settings.json's `env` holds now. `https_proxy` is the lowercase value when set, else
/// the uppercase one: Claude Code reads the lowercase first.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct EnvView {
    pub https_proxy: Option<String>,
    pub node_ca: Option<String>,
    pub no_proxy: Option<String>,
    pub base_url: Option<String>,
    pub tool_search: Option<String>,
}

/// Where the router serves and what routing needs beside it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RouterTarget {
    pub port: u16,
    pub router_url: String,
    pub ca_pem: String,
    /// `https_proxy` then `HTTPS_PROXY` from huddle's own process env, empty counted as unset.
    pub process_https_proxy: Option<String>,
    /// Set when the local CA could not be created: forces base-URL mode.
    pub ca_error: Option<String>,
}

/// The env changes one patch makes: `set` writes keys in order, `remove` takes keys out,
/// `lines` is the report for the user.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EnvPlan {
    pub mode: RouteMode,
    pub set: Vec<(String, String)>,
    pub remove: Vec<String>,
    pub lines: Vec<String>,
}

/// Whether `value` names the router: a loopback host on `port` or on the default router port.
pub fn is_router_url(value: &str, port: u16) -> bool {
    let without_scheme = value
        .strip_prefix("https://")
        .or_else(|| value.strip_prefix("http://"))
        .unwrap_or(value);
    let host_port = without_scheme.strip_suffix('/').unwrap_or(without_scheme);
    let Some((host, port_text)) = host_port.rsplit_once(':') else {
        return false;
    };
    let loopback = host == "localhost" || host == "[::1]" || host.starts_with("127.");
    loopback && (port_text == port.to_string() || port_text == DEFAULT_ROUTER_PORT.to_string())
}

/// Decides the env changes for `view`: proxy mode when nothing of the user's own is in the
/// way and the local CA works, base-URL mode otherwise.
pub fn plan_env(view: &EnvView, target: &RouterTarget) -> EnvPlan {
    let foreign = |value: Option<&str>| value.is_some_and(|v| !is_router_url(v, target.port));
    let foreign_proxy = foreign(view.https_proxy.as_deref());
    let foreign_process = foreign(target.process_https_proxy.as_deref());
    let foreign_ca = view.node_ca.as_deref().is_some_and(|v| v != target.ca_pem);
    let foreign_base = foreign(view.base_url.as_deref());

    let mut plan = EnvPlan {
        mode: RouteMode::Proxy,
        set: Vec::new(),
        remove: Vec::new(),
        lines: Vec::new(),
    };
    if foreign_proxy || foreign_process || foreign_ca || foreign_base || target.ca_error.is_some() {
        plan.mode = RouteMode::BaseUrl;
        match target.ca_error.as_deref() {
            Some(err) => {
                plan.lines.push(format!(
                    "the local CA is unusable ({err}): routing keeps ANTHROPIC_BASE_URL"
                ));
            }
            None => {
                plan.lines
                    .push("kept your own proxy/CA; Remote Control needs ANTHROPIC_BASE_URL unset, so routing uses the base URL".to_string());
            }
        }
        set_base_url(&mut plan, view, target);
        return plan;
    }
    proxy_mode(&mut plan, view, target);
    plan
}

/// The base-URL fallback: point ANTHROPIC_BASE_URL at the router when it is absent or names a
/// router of ours, keep the user's own, and turn tool search on only when the base URL ends up
/// at the router.
fn set_base_url(plan: &mut EnvPlan, view: &EnvView, target: &RouterTarget) {
    let url = target.router_url.as_str();
    let base_at_router = match view.base_url.as_deref() {
        None => set_base(plan, url),
        Some(value) if is_router_url(value, target.port) => value == url || set_base(plan, url),
        Some(value) => {
            plan.lines
                .push(format!("kept ANTHROPIC_BASE_URL ({value}): your own proxy"));
            false
        }
    };
    if view.tool_search.is_none() && base_at_router {
        plan.set
            .push(("ENABLE_TOOL_SEARCH".to_string(), "true".to_string()));
        plan.lines.push("turned on MCP tool search".to_string());
    }
}

fn set_base(plan: &mut EnvPlan, url: &str) -> bool {
    plan.set
        .push(("ANTHROPIC_BASE_URL".to_string(), url.to_string()));
    plan.lines
        .push(format!("pointed ANTHROPIC_BASE_URL at {url}"));
    true
}

/// Proxy mode: the three proxy keys at the router, a stale router base URL out, tool search on.
fn proxy_mode(plan: &mut EnvPlan, view: &EnvView, target: &RouterTarget) {
    let url = target.router_url.as_str();
    match view.https_proxy.as_deref() {
        None => {
            plan.set.push(("HTTPS_PROXY".to_string(), url.to_string()));
            plan.lines.push(format!("pointed HTTPS_PROXY at {url}"));
        }
        // Proxy mode has no foreign proxy, so any other value is one of ours on another port.
        Some(value) if value != url => {
            plan.set.push(("HTTPS_PROXY".to_string(), url.to_string()));
            plan.lines
                .push(format!("moved HTTPS_PROXY from {value} to {url}"));
        }
        Some(_) => {}
    }
    if view.node_ca.as_deref() != Some(target.ca_pem.as_str()) {
        plan.set
            .push(("NODE_EXTRA_CA_CERTS".to_string(), target.ca_pem.clone()));
    }
    // A NO_PROXY the user never set is not ours to start on a keep; their own list gets the
    // loopback entries it lacks.
    let keep = view.https_proxy.is_some();
    if !(keep && view.no_proxy.is_none()) {
        let (merged, appended) = merge_no_proxy(view.no_proxy.as_deref());
        if view.no_proxy.as_deref() != Some(merged.as_str()) {
            plan.set.push(("NO_PROXY".to_string(), merged));
            if !appended.is_empty() {
                plan.lines
                    .push(format!("appended {} to NO_PROXY", appended.join(" and ")));
            }
        }
    }
    if let Some(value) = view.base_url.as_deref()
        && is_router_url(value, target.port)
    {
        plan.remove.push("ANTHROPIC_BASE_URL".to_string());
        plan.lines
            .push(format!("removed ANTHROPIC_BASE_URL ({value})"));
    }
    if view.tool_search.is_none() {
        plan.set
            .push(("ENABLE_TOOL_SEARCH".to_string(), "true".to_string()));
        plan.lines.push("turned on MCP tool search".to_string());
    }
}

/// Keeps the user's NO_PROXY entries in their order and appends the loopback entries they lack;
/// no current value starts a fresh list.
fn merge_no_proxy(current: Option<&str>) -> (String, Vec<&str>) {
    let mut entries: Vec<&str> = Vec::new();
    if let Some(current) = current {
        for entry in current.split(',') {
            let entry = entry.trim();
            if !entry.is_empty() {
                entries.push(entry);
            }
        }
    }
    let mut appended = Vec::new();
    for loopback in ["localhost", "127.0.0.1"] {
        if !entries.contains(&loopback) {
            appended.push(loopback);
            entries.push(loopback);
        }
    }
    (entries.join(","), appended)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target() -> RouterTarget {
        RouterTarget {
            port: 18999,
            router_url: "http://127.0.0.1:18999".into(),
            ca_pem: "/h/.agents/ca/ca.pem".into(),
            process_https_proxy: None,
            ca_error: None,
        }
    }

    #[test]
    fn a_fresh_env_gets_the_proxy_keys() {
        let plan = plan_env(&EnvView::default(), &target());
        assert_eq!(plan.mode, RouteMode::Proxy);
        assert_eq!(
            plan.set,
            vec![
                (
                    "HTTPS_PROXY".to_string(),
                    "http://127.0.0.1:18999".to_string()
                ),
                (
                    "NODE_EXTRA_CA_CERTS".to_string(),
                    "/h/.agents/ca/ca.pem".to_string()
                ),
                ("NO_PROXY".to_string(), "localhost,127.0.0.1".to_string()),
                ("ENABLE_TOOL_SEARCH".to_string(), "true".to_string()),
            ]
        );
        assert_eq!(plan.remove, Vec::<String>::new());
        assert!(
            plan.lines
                .contains(&"pointed HTTPS_PROXY at http://127.0.0.1:18999".to_string())
        );
    }

    #[test]
    fn a_keep_with_no_user_no_proxy_starts_none() {
        let view = EnvView {
            https_proxy: Some("http://127.0.0.1:18999".into()),
            node_ca: Some("/h/.agents/ca/ca.pem".into()),
            tool_search: Some("true".into()),
            ..EnvView::default()
        };
        let plan = plan_env(&view, &target());
        assert!(plan.set.is_empty(), "{:?}", plan.set);
    }

    #[test]
    fn a_users_no_proxy_entries_stay_first() {
        let view = EnvView {
            no_proxy: Some("corp.example, localhost".into()),
            ..EnvView::default()
        };
        let plan = plan_env(&view, &target());
        let no_proxy = plan
            .set
            .iter()
            .find(|(k, _)| k == "NO_PROXY")
            .expect("NO_PROXY set");
        assert_eq!(no_proxy.1, "corp.example,localhost,127.0.0.1");
    }

    #[test]
    fn a_foreign_proxy_falls_back_to_the_base_url() {
        let view = EnvView {
            https_proxy: Some("http://corp:3128".into()),
            ..EnvView::default()
        };
        let plan = plan_env(&view, &target());
        assert_eq!(plan.mode, RouteMode::BaseUrl);
        assert_eq!(
            plan.set[0],
            (
                "ANTHROPIC_BASE_URL".to_string(),
                "http://127.0.0.1:18999".to_string()
            )
        );
        assert!(
            plan.lines
                .iter()
                .any(|l| l.starts_with("kept your own proxy/CA;"))
        );
    }

    #[test]
    fn a_foreign_process_proxy_and_a_foreign_ca_both_fall_back() {
        let mut t = target();
        t.process_https_proxy = Some("http://corp:3128".into());
        assert_eq!(plan_env(&EnvView::default(), &t).mode, RouteMode::BaseUrl);

        let view = EnvView {
            node_ca: Some("/company/ca.pem".into()),
            ..EnvView::default()
        };
        assert_eq!(plan_env(&view, &target()).mode, RouteMode::BaseUrl);
    }

    #[test]
    fn an_unusable_ca_says_why_and_keeps_the_base_url() {
        let mut t = target();
        t.ca_error = Some("cannot write".into());
        let plan = plan_env(&EnvView::default(), &t);
        assert_eq!(plan.mode, RouteMode::BaseUrl);
        assert!(
            plan.lines.contains(
                &"the local CA is unusable (cannot write): routing keeps ANTHROPIC_BASE_URL"
                    .to_string()
            )
        );
    }

    #[test]
    fn a_stale_router_base_url_goes_out_in_proxy_mode() {
        let view = EnvView {
            base_url: Some("http://127.0.0.1:4835".into()),
            ..EnvView::default()
        };
        let plan = plan_env(&view, &target());
        assert_eq!(plan.mode, RouteMode::Proxy);
        assert_eq!(plan.remove, vec!["ANTHROPIC_BASE_URL".to_string()]);
    }

    #[test]
    fn is_router_url_knows_loopback_on_the_port() {
        assert!(is_router_url("http://127.0.0.1:18999", 18999));
        assert!(is_router_url("127.0.0.1:4835", 18999));
        assert!(is_router_url("http://localhost:18999/", 18999));
        assert!(!is_router_url("http://corp:18999", 18999));
        assert!(!is_router_url("http://127.0.0.1:9", 18999));
        assert!(!is_router_url("", 18999));
    }
}
