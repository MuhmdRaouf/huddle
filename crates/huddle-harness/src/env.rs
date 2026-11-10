//! The environment a harness starts with.

use huddle_config::env::Env;
use huddle_config::paths::DEFAULT_ROUTER_PORT;

/// Sorted by name. `extra` entries are added last and override (the access env, the launch env).
pub fn harness_env(env: &Env, extra: &[(String, String)]) -> Vec<(String, String)> {
    let own_port = env
        .get("HUDDLE_ROUTER_PORT")
        .and_then(|port| port.parse::<u16>().ok());
    let mut drop_ca = false;
    let mut vars: Vec<(String, String)> = Vec::new();
    for (name, value) in env.iter() {
        if is_provider_var(name) || is_claude_session_var(name) {
            continue;
        }
        if is_proxy_var(name) && names_router(value, own_port) {
            drop_ca = true;
            continue;
        }
        vars.push((name.to_string(), value.to_string()));
    }
    for (name, value) in extra {
        match vars.iter_mut().find(|(existing, _)| existing == name) {
            Some(slot) => slot.1 = value.clone(),
            None => vars.push((name.clone(), value.clone())),
        }
    }
    // The CA was set for the router proxy's certificate; it never passes, whatever its source.
    if drop_ca {
        vars.retain(|(name, _)| name != "NODE_EXTRA_CA_CERTS");
    }
    vars.sort_by(|a, b| a.0.cmp(&b.0));
    vars
}

/// True when a proxy value names huddle-router: loopback host, port `DEFAULT_ROUTER_PORT` or the
/// router's own port. Scheme optional; userinfo, path and query tolerated. The `127.` prefix test
/// is deliberately loose: any 127.x address counts as loopback.
pub fn names_router(value: &str, own_port: Option<u16>) -> bool {
    let with_scheme;
    let raw = if value.contains("://") {
        value
    } else {
        with_scheme = format!("http://{value}");
        with_scheme.as_str()
    };
    let Some((_, rest)) = raw.split_once("://") else {
        return false;
    };
    let authority_end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
    let authority = &rest[..authority_end];
    let host_port = match authority.rsplit_once('@') {
        Some((_, host_port)) => host_port,
        None => authority,
    };
    // The port split runs while the brackets are still on, so the last colon of `[::1]:4835` is
    // the port separator, not one of the address's own.
    let Some((bracketed_host, port)) = host_port.rsplit_once(':') else {
        return false;
    };
    let host = bracketed_host
        .trim_matches(|c| c == '[' || c == ']')
        .to_ascii_lowercase();
    let loopback = host == "localhost" || host == "::1" || host.starts_with("127.");
    if !loopback {
        return false;
    }
    match port.parse::<u16>() {
        Ok(port) => port == DEFAULT_ROUTER_PORT || own_port == Some(port),
        Err(_) => false,
    }
}

/// The five providers' variables and their shared key name (`DASHSCOPE_API_KEY`).
fn is_provider_var(name: &str) -> bool {
    const PREFIXES: [&str; 5] = ["ZAI_", "KIMI_", "DEEPSEEK_", "MINIMAX_", "QWEN_"];
    PREFIXES.iter().any(|prefix| name.starts_with(prefix)) || name == "DASHSCOPE_API_KEY"
}

/// The Claude Code session's variables: routing, model defaults and session markers. A bare
/// `CLAUDE` and `ANTHROPIC_API_KEY` are the user's own and pass.
fn is_claude_session_var(name: &str) -> bool {
    matches!(
        name,
        "ANTHROPIC_BASE_URL"
            | "ANTHROPIC_AUTH_TOKEN"
            | "ANTHROPIC_MODEL"
            | "ANTHROPIC_SMALL_FAST_MODEL"
            | "ANTHROPIC_CUSTOM_HEADERS"
    ) || name.starts_with("ANTHROPIC_DEFAULT_")
        || name.starts_with("CLAUDE_")
        || name == "CLAUDECODE"
}

/// An outbound proxy variable, both spellings. `NO_PROXY` is an exemption list, not a proxy.
fn is_proxy_var(name: &str) -> bool {
    name.eq_ignore_ascii_case("http_proxy")
        || name.eq_ignore_ascii_case("https_proxy")
        || name.eq_ignore_ascii_case("all_proxy")
}
