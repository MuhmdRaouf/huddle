//! huddle's own health checks, in the order the page lists them.

use serde_json::Value;

use super::plugins::installed_version;
use super::{Check, Facts, Level};

pub fn checks(facts: &Facts) -> Vec<Check> {
    vec![
        binary(facts),
        router(facts),
        hook(facts),
        proxy(facts),
        ca(facts),
        providers_file(facts),
        config_file(facts),
    ]
}

fn check(
    id: &'static str,
    label: &'static str,
    level: Level,
    value: String,
    detail: &str,
) -> Check {
    Check {
        id,
        level,
        label,
        value,
        detail: detail.to_string(),
    }
}

/// The running binary against the version the huddle plugin carries.
fn binary(facts: &Facts) -> Check {
    let value = format!("huddle {}", facts.version);
    let plugin = installed_version(facts.installed_plugins, "huddle").flatten();
    match plugin {
        Some(plugin) if plugin != facts.version => check(
            "binary",
            "Binary",
            Level::Warn,
            value,
            &format!("the plugin carries {plugin}; run /huddle:update"),
        ),
        _ => check("binary", "Binary", Level::Pass, value, ""),
    }
}

/// This code runs inside the router, so it is up.
fn router(facts: &Facts) -> Check {
    check(
        "router",
        "Router",
        Level::Pass,
        format!("127.0.0.1:{}", facts.port),
        "",
    )
}

/// The hook ships in the huddle plugin's hooks.json; a hand-written one in settings counts too.
fn hook(facts: &Facts) -> Check {
    let from_plugin = installed_version(facts.installed_plugins, "huddle").is_some();
    if from_plugin || settings_name_hook(facts.settings) {
        return check(
            "hook",
            "Session hook",
            Level::Pass,
            "session-start".into(),
            "",
        );
    }
    check(
        "hook",
        "Session hook",
        Level::Warn,
        "missing".into(),
        "install the huddle plugin: /plugin install huddle@muhmdraouf",
    )
}

fn settings_name_hook(settings: Option<&str>) -> bool {
    let Some(doc) = settings.and_then(|text| serde_json::from_str::<Value>(text).ok()) else {
        return false;
    };
    let Some(events) = doc.get("hooks").and_then(|hooks| hooks.get("SessionStart")) else {
        return false;
    };
    events.to_string().contains("hook session-start")
}

fn proxy(facts: &Facts) -> Check {
    if facts.wiring.proxy {
        return check(
            "proxy",
            "HTTPS_PROXY",
            Level::Pass,
            format!("127.0.0.1:{}", facts.port),
            "",
        );
    }
    check(
        "proxy",
        "HTTPS_PROXY",
        Level::Fail,
        "not set".into(),
        "Claude Code does not point at the router; run /huddle:setup",
    )
}

fn ca(facts: &Facts) -> Check {
    if facts.wiring.ca {
        return check("ca", "CA", Level::Pass, "NODE_EXTRA_CA_CERTS".into(), "");
    }
    check(
        "ca",
        "CA",
        Level::Fail,
        "missing".into(),
        "NODE_EXTRA_CA_CERTS names no file; run /huddle:setup",
    )
}

fn providers_file(facts: &Facts) -> Check {
    match &facts.enabled {
        Err(why) => check(
            "providers",
            "providers.json",
            Level::Fail,
            "unreadable".into(),
            why,
        ),
        Ok(enabled) if enabled.is_empty() => check(
            "providers",
            "providers.json",
            Level::Warn,
            "none enabled".into(),
            "run /<provider>:setup to add a lane",
        ),
        Ok(enabled) => check(
            "providers",
            "providers.json",
            Level::Pass,
            enabled.join(", "),
            "",
        ),
    }
}

fn config_file(facts: &Facts) -> Check {
    match &facts.config_hosts {
        Err(why) => check("config", "config.toml", Level::Fail, "invalid".into(), why),
        Ok(0) => check("config", "config.toml", Level::Pass, "defaults".into(), ""),
        Ok(hosts) => check(
            "config",
            "config.toml",
            Level::Pass,
            format!("{hosts} dashboard hosts"),
            "",
        ),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::checks::Level;
    use huddle_router::live::Wiring;
    use std::collections::HashMap;

    const HUDDLE_CURRENT: &str = r#"{"plugins":{"huddle@muhmdraouf":[{"version":"0.0.3"}]}}"#;
    const HUDDLE_OLD: &str = r#"{"plugins":{"huddle@muhmdraouf":[{"version":"0.0.2"}]}}"#;

    fn facts<'a>(keys: &'a HashMap<&'static str, bool>) -> Facts<'a> {
        Facts {
            version: "0.0.3",
            port: 4835,
            installed_plugins: Some(HUDDLE_CURRENT),
            settings: None,
            wiring: Wiring {
                proxy: true,
                ca: true,
            },
            enabled: Ok(vec!["zai".into(), "kimi".into()]),
            config_hosts: Ok(0),
            keys,
        }
    }

    fn check<'a>(list: &'a [Check], id: &str) -> &'a Check {
        list.iter().find(|c| c.id == id).expect("check")
    }

    #[test]
    fn a_healthy_machine_passes_all_seven_checks_in_order() {
        let keys = HashMap::new();
        let list = checks(&facts(&keys));
        let ids: Vec<_> = list.iter().map(|c| c.id).collect();
        assert_eq!(
            ids,
            [
                "binary",
                "router",
                "hook",
                "proxy",
                "ca",
                "providers",
                "config"
            ]
        );
        assert!(list.iter().all(|c| c.level == Level::Pass), "{list:?}");
        assert_eq!(check(&list, "binary").value, "huddle 0.0.3");
        assert_eq!(check(&list, "router").value, "127.0.0.1:4835");
        assert_eq!(check(&list, "providers").value, "zai, kimi");
        assert_eq!(check(&list, "config").value, "defaults");
    }

    #[test]
    fn a_plugin_of_another_version_warns_and_names_huddle_update() {
        let keys = HashMap::new();
        let mut f = facts(&keys);
        f.installed_plugins = Some(HUDDLE_OLD);
        let list = checks(&f);
        let binary = check(&list, "binary");
        assert_eq!(binary.level, Level::Warn);
        assert!(
            binary.detail.contains("/huddle:update"),
            "{}",
            binary.detail
        );
        assert!(binary.detail.contains("0.0.2"), "{}", binary.detail);
    }

    #[test]
    fn a_proxy_or_ca_that_is_not_wired_fails() {
        let keys = HashMap::new();
        let mut f = facts(&keys);
        f.wiring = Wiring::default();
        let list = checks(&f);
        assert_eq!(check(&list, "proxy").level, Level::Fail);
        assert_eq!(check(&list, "ca").level, Level::Fail);
        assert!(check(&list, "proxy").detail.contains("/huddle:setup"));
    }

    #[test]
    fn the_session_hook_comes_from_the_plugin_or_from_settings() {
        let keys = HashMap::new();
        let mut f = facts(&keys);
        f.installed_plugins = None;
        assert_eq!(check(&checks(&f), "hook").level, Level::Warn);
        f.settings = Some(
            r#"{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/x/huddle hook session-start"}]}]}}"#,
        );
        assert_eq!(check(&checks(&f), "hook").level, Level::Pass);
        f.settings = None;
        f.installed_plugins = Some(HUDDLE_CURRENT);
        assert_eq!(check(&checks(&f), "hook").level, Level::Pass);
    }

    #[test]
    fn providers_json_with_no_lane_warns_and_an_unreadable_one_fails() {
        let keys = HashMap::new();
        let mut f = facts(&keys);
        f.enabled = Ok(Vec::new());
        assert_eq!(check(&checks(&f), "providers").level, Level::Warn);
        f.enabled = Err("bad json".into());
        let list = checks(&f);
        assert_eq!(check(&list, "providers").level, Level::Fail);
        assert_eq!(check(&list, "providers").detail, "bad json");
    }

    #[test]
    fn config_toml_counts_hosts_and_fails_when_invalid() {
        let keys = HashMap::new();
        let mut f = facts(&keys);
        f.config_hosts = Ok(2);
        assert_eq!(check(&checks(&f), "config").value, "2 dashboard hosts");
        f.config_hosts = Err("expected a table".into());
        let list = checks(&f);
        assert_eq!(check(&list, "config").level, Level::Fail);
        assert_eq!(check(&list, "config").detail, "expected a table");
    }
}
