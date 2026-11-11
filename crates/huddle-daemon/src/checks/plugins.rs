//! One row per provider plugin, and the plugin report that carries huddle's checks beside them.

use std::collections::HashMap;
use std::fs;

use huddle_catalog::providers::{Provider, providers};
use huddle_config::dashboard_hosts;
use huddle_config::paths::Paths;
use huddle_config::providers_file;
use huddle_router::live::Wiring;
use serde_json::Value;

use super::health;
use super::{Check, Facts, Phase};

/// The marketplace the plugins install from.
pub const MARKETPLACE: &str = "muhmdraouf";

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct PluginRow {
    pub name: String,
    pub version: Option<String>,
    pub installed: bool,
    pub setup: bool,
    pub key: bool,
    pub lane: Option<String>,
    pub models: usize,
    pub next: Option<String>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize)]
pub struct PluginsReport {
    pub state: Phase,
    pub rows: Vec<PluginRow>,
    pub checks: Vec<Check>,
}

/// The report for one reading of the machine.
pub fn report(facts: &Facts) -> PluginsReport {
    PluginsReport {
        state: Phase::Ready,
        rows: rows(facts),
        checks: health::checks(facts),
    }
}

/// Reads the machine's files and builds the report. A missing or broken file is a fact the
/// builders show, never an error.
pub fn read(
    paths: &Paths,
    version: &str,
    port: u16,
    wiring: Wiring,
    keys: &HashMap<&'static str, bool>,
) -> PluginsReport {
    let known: Vec<&str> = providers().iter().map(|provider| provider.id).collect();
    let installed_plugins =
        fs::read_to_string(paths.claude_dir.join("plugins/installed_plugins.json")).ok();
    let carried = carried_versions(installed_plugins.as_deref());
    let settings = fs::read_to_string(paths.claude_dir.join("settings.json")).ok();
    report(&Facts {
        version,
        port,
        installed_plugins: installed_plugins.as_deref(),
        carried: &carried,
        settings: settings.as_deref(),
        wiring,
        enabled: providers_file::read_enabled(&paths.config_dir, &known)
            .map_err(|error| error.to_string()),
        config_hosts: dashboard_hosts::read(&dashboard_hosts::config_path(paths))
            .map(|hosts| hosts.len())
            .map_err(|error| error.to_string()),
        keys,
    })
}

/// The key Claude Code records a plugin under.
pub fn plugin_key(plugin: &str) -> String {
    format!("{plugin}@{MARKETPLACE}")
}

/// The installed release of a plugin (`zai-plugin-cc`, `huddle`): `None` when it is not
/// installed, `Some(None)` when it is installed without a readable release. A plugin installed
/// from a git checkout is recorded under the commit, so its release is the one its `dist/run`
/// carries.
pub fn installed_version(
    installed_plugins: Option<&str>,
    carried: &HashMap<String, String>,
    plugin: &str,
) -> Option<Option<String>> {
    let doc: Value = serde_json::from_str(installed_plugins?).ok()?;
    let entries = doc.get("plugins")?.get(plugin_key(plugin))?.as_array()?;
    let recorded = entries.first()?.get("version").and_then(Value::as_str);
    Some(match recorded {
        Some(version) if is_release(version) => Some(version.to_string()),
        _ => carried.get(plugin).cloned(),
    })
}

/// `<major>.<minor>.<patch>`, digits only.
fn is_release(version: &str) -> bool {
    let parts: Vec<&str> = version.split('.').collect();
    parts.len() == 3
        && parts
            .iter()
            .all(|part| !part.is_empty() && part.bytes().all(|b| b.is_ascii_digit()))
}

/// The `version=` line of each marketplace plugin's installed `dist/run`, by plugin name.
fn carried_versions(installed_plugins: Option<&str>) -> HashMap<String, String> {
    let Some(doc) = installed_plugins.and_then(|text| serde_json::from_str::<Value>(text).ok())
    else {
        return HashMap::new();
    };
    let Some(plugins) = doc.get("plugins").and_then(Value::as_object) else {
        return HashMap::new();
    };
    let suffix = format!("@{MARKETPLACE}");
    plugins
        .iter()
        .filter_map(|(key, entries)| {
            let name = key.strip_suffix(&suffix)?;
            let install = entries.as_array()?.first()?.get("installPath")?.as_str()?;
            let run = fs::read_to_string(std::path::Path::new(install).join("dist/run")).ok()?;
            let version = run
                .lines()
                .find_map(|line| line.strip_prefix("version="))?
                .trim();
            is_release(version).then(|| (name.to_string(), version.to_string()))
        })
        .collect()
}

fn rows(facts: &Facts) -> Vec<PluginRow> {
    let picker = picker_models(facts.settings);
    providers()
        .iter()
        .map(|provider| row(provider, facts, &picker))
        .collect()
}

fn row(provider: &Provider, facts: &Facts, picker: &[String]) -> PluginRow {
    let plugin = format!("{}-plugin-cc", provider.id);
    let installed = installed_version(facts.installed_plugins, facts.carried, &plugin);
    let setup = facts
        .enabled
        .as_ref()
        .is_ok_and(|enabled| enabled.iter().any(|name| name == provider.id));
    let key = facts.keys.get(provider.id).copied().unwrap_or(false);
    let models = [provider.main.id, provider.flash.id]
        .iter()
        .filter(|id| picker.iter().any(|held| held == *id))
        .count();
    let version = installed.clone().flatten();
    let next = if installed.is_none() {
        Some(format!("/plugin install {}", plugin_key(&plugin)))
    } else if version.as_deref().is_some_and(|v| v != facts.version) {
        Some("/huddle:update".to_string())
    } else if !setup || !key {
        Some(format!("{}setup", provider.slash))
    } else {
        None
    };
    PluginRow {
        name: provider.id.to_string(),
        version,
        installed: installed.is_some(),
        setup,
        key,
        lane: setup.then(|| lane_of(provider)),
        models,
        next,
    }
}

/// The model prefixes the lane claims, as globs: `glm-*`.
fn lane_of(provider: &Provider) -> String {
    let globs: Vec<String> = provider
        .model_prefixes
        .iter()
        .map(|prefix| format!("{prefix}*"))
        .collect();
    globs.join(", ")
}

/// The model ids in `modelPicker.options` of Claude Code's settings.
fn picker_models(settings: Option<&str>) -> Vec<String> {
    let doc: Option<Value> = settings.and_then(|text| serde_json::from_str(text).ok());
    doc.as_ref()
        .and_then(|doc| doc.get("modelPicker")?.get("options")?.as_array())
        .map(|options| {
            options
                .iter()
                .filter_map(|row| row.get("model")?.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_router::live::Wiring;
    use std::collections::HashMap;

    const INSTALLED: &str = r#"{"version":2,"plugins":{
        "huddle@muhmdraouf":[{"scope":"user","version":"0.0.3"}],
        "zai-plugin-cc@muhmdraouf":[{"scope":"user","version":"0.0.3"}],
        "kimi-plugin-cc@muhmdraouf":[{"scope":"user","version":"0.0.2"}],
        "other@elsewhere":[{"version":"9"}]}}"#;

    static NO_RUN_VERSIONS: std::sync::LazyLock<HashMap<String, String>> =
        std::sync::LazyLock::new(HashMap::new);

    const SETTINGS: &str = r#"{"modelPicker":{"options":[
        {"model":"glm-5.3","label":"GLM 5.3"},{"model":"glm-5.3-flash","label":"GLM 5.3 Flash"},
        {"model":"kimi-k3","label":"Kimi K3"}]}}"#;

    fn facts<'a>(keys: &'a HashMap<&'static str, bool>) -> Facts<'a> {
        Facts {
            version: "0.0.3",
            port: 4835,
            installed_plugins: Some(INSTALLED),
            carried: &NO_RUN_VERSIONS,
            settings: Some(SETTINGS),
            wiring: Wiring {
                proxy: true,
                ca: true,
            },
            enabled: Ok(vec!["zai".to_string()]),
            config_hosts: Ok(0),
            keys,
        }
    }

    fn row<'a>(rows: &'a [PluginRow], name: &str) -> &'a PluginRow {
        rows.iter().find(|row| row.name == name).expect("row")
    }

    #[test]
    fn a_set_up_plugin_shows_its_lane_models_and_key_with_no_next_step() {
        let keys = HashMap::from([("zai", true)]);
        let rows = rows(&facts(&keys));
        let zai = row(&rows, "zai");
        assert_eq!(
            *zai,
            PluginRow {
                name: "zai".into(),
                version: Some("0.0.3".into()),
                installed: true,
                setup: true,
                key: true,
                lane: Some("glm-*".into()),
                models: 2,
                next: None,
            }
        );
    }

    #[test]
    fn there_is_one_row_per_catalog_provider() {
        let keys = HashMap::new();
        let names: Vec<_> = rows(&facts(&keys)).into_iter().map(|r| r.name).collect();
        let catalog: Vec<_> = huddle_catalog::providers::providers()
            .iter()
            .map(|p| p.id.to_string())
            .collect();
        assert_eq!(names, catalog);
    }

    #[test]
    fn a_missing_plugin_names_the_install_command() {
        let keys = HashMap::new();
        let rows = rows(&facts(&keys));
        let qwen = row(&rows, "qwen");
        assert!(!qwen.installed);
        assert_eq!(qwen.version, None);
        assert_eq!(
            qwen.next.as_deref(),
            Some("/plugin install qwen-plugin-cc@muhmdraouf")
        );
    }

    #[test]
    fn a_version_other_than_the_binary_names_huddle_update() {
        let keys = HashMap::from([("kimi", true)]);
        let rows = rows(&facts(&keys));
        assert_eq!(row(&rows, "kimi").next.as_deref(), Some("/huddle:update"));
    }

    #[test]
    fn an_installed_plugin_without_a_lane_names_its_setup() {
        let keys = HashMap::from([("kimi", false)]);
        let mut f = facts(&keys);
        let all_current = INSTALLED.replace("0.0.2", "0.0.3");
        f.installed_plugins = Some(&all_current);
        let rows = rows(&f);
        let kimi = row(&rows, "kimi");
        assert!(!kimi.setup);
        assert_eq!(kimi.lane, None);
        assert_eq!(kimi.next.as_deref(), Some("/kimi:setup"));
    }

    #[test]
    fn a_lane_without_a_key_names_setup_too() {
        let keys = HashMap::from([("zai", false)]);
        let rows = rows(&facts(&keys));
        let zai = row(&rows, "zai");
        assert!(zai.setup && !zai.key);
        assert_eq!(zai.next.as_deref(), Some("/zai:setup"));
    }

    #[test]
    fn unreadable_files_leave_every_plugin_not_installed() {
        let keys = HashMap::new();
        let mut f = facts(&keys);
        f.installed_plugins = None;
        f.settings = Some("{");
        f.enabled = Err("unreadable".into());
        assert!(
            rows(&f)
                .iter()
                .all(|r| !r.installed && !r.setup && r.models == 0)
        );
    }

    #[test]
    fn installed_version_tells_missing_from_unversioned() {
        let doc = r#"{"plugins":{"zai-plugin-cc@muhmdraouf":[{"scope":"user"}]}}"#;
        let none = HashMap::new();
        assert_eq!(
            installed_version(Some(doc), &none, "zai-plugin-cc"),
            Some(None)
        );
        assert_eq!(installed_version(Some(doc), &none, "kimi-plugin-cc"), None);
        assert_eq!(installed_version(None, &none, "zai-plugin-cc"), None);
    }

    #[test]
    fn q3_a_git_sha_record_reads_the_release_its_dist_run_carries() {
        let doc = r#"{"plugins":{
            "huddle@muhmdraouf":[{"version":"1d1ac60d1993"}],
            "zai-plugin-cc@muhmdraouf":[{"version":"1d1ac60d1993"}],
            "kimi-plugin-cc@muhmdraouf":[{"version":"0.0.2"}]}}"#;
        let carried = HashMap::from([("huddle".to_string(), "0.0.3".to_string())]);
        assert_eq!(
            installed_version(Some(doc), &carried, "huddle"),
            Some(Some("0.0.3".into()))
        );
        assert_eq!(
            installed_version(Some(doc), &carried, "zai-plugin-cc"),
            Some(None)
        );
        assert_eq!(
            installed_version(Some(doc), &carried, "kimi-plugin-cc"),
            Some(Some("0.0.2".into()))
        );
    }

    #[test]
    fn q3_read_finds_the_version_in_the_installed_dist_run() {
        use huddle_config::env::Env;
        use huddle_testkit::temp_home::TempHome;

        let home = TempHome::new();
        let root = home.root().display().to_string();
        let paths = Paths::from_env(&Env::with(&[("HOME", root.as_str())])).expect("paths");
        let install = paths
            .claude_dir
            .join("plugins/cache/muhmdraouf/huddle/1d1ac60d1993");
        fs::create_dir_all(install.join("dist")).expect("install dir");
        fs::write(
            install.join("dist/run"),
            "#!/bin/sh\nset -eu\nversion=0.0.3\n",
        )
        .expect("dist/run");
        let doc = serde_json::json!({"plugins": {"huddle@muhmdraouf": [
            {"version": "1d1ac60d1993", "installPath": install.display().to_string()}
        ]}});
        fs::write(
            paths.claude_dir.join("plugins/installed_plugins.json"),
            doc.to_string(),
        )
        .expect("installed");
        let keys = HashMap::new();
        let wiring = Wiring {
            proxy: true,
            ca: true,
        };

        let report = read(&paths, "0.0.3", 4835, wiring, &keys);

        let binary = report
            .checks
            .iter()
            .find(|c| c.id == "binary")
            .expect("binary");
        assert_eq!(binary.detail, "", "{}", binary.detail);
    }

    #[test]
    fn read_builds_the_report_from_files_in_a_temp_home() {
        use huddle_config::env::Env;
        use huddle_testkit::temp_home::TempHome;

        let home = TempHome::new();
        let root = home.root().display().to_string();
        let paths = Paths::from_env(&Env::with(&[("HOME", root.as_str())])).expect("paths");
        fs::create_dir_all(paths.claude_dir.join("plugins")).expect("plugins dir");
        fs::write(
            paths.claude_dir.join("plugins/installed_plugins.json"),
            INSTALLED,
        )
        .expect("installed");
        fs::write(paths.claude_dir.join("settings.json"), SETTINGS).expect("settings");
        providers_file::set_enabled(&paths.config_dir, "zai", true).expect("providers.toml");
        fs::create_dir_all(&paths.config_dir).expect("config dir");
        fs::write(
            paths.config_dir.join("config.toml"),
            "[dashboard]\nhosts = [\"hub.example.com\"]\n",
        )
        .expect("config");
        let keys = HashMap::from([("zai", true)]);
        let wiring = Wiring {
            proxy: true,
            ca: true,
        };

        let report = read(&paths, "0.0.3", 4835, wiring, &keys);

        assert_eq!(report.state, Phase::Ready);
        let zai = row(&report.rows, "zai");
        assert!(zai.installed && zai.setup && zai.key);
        assert_eq!(zai.models, 2);
        let config = report
            .checks
            .iter()
            .find(|c| c.id == "config")
            .expect("config");
        assert_eq!(config.value, "1 dashboard hosts");
        let providers = report
            .checks
            .iter()
            .find(|c| c.id == "providers")
            .expect("providers");
        assert_eq!(providers.value, "zai");
    }

    #[test]
    fn read_of_an_empty_home_shows_nothing_installed_without_failing() {
        use huddle_config::env::Env;
        use huddle_testkit::temp_home::TempHome;

        let home = TempHome::new();
        let root = home.root().display().to_string();
        let paths = Paths::from_env(&Env::with(&[("HOME", root.as_str())])).expect("paths");
        let report = read(&paths, "0.0.3", 4835, Wiring::default(), &HashMap::new());
        assert_eq!(report.state, Phase::Ready);
        assert!(report.rows.iter().all(|r| !r.installed && !r.setup));
        assert_eq!(report.checks.len(), 7);
    }
}
