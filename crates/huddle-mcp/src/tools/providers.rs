//! `huddle_providers` and `huddle_quota`: which providers are set up, and what is left of each
//! plan or balance. A key is only ever reported as present or absent.

use std::collections::BTreeMap;

use huddle_catalog::providers::{Provider, Quota, provider_by_id};
use rmcp::schemars::{self, JsonSchema};
use rmcp::{ErrorData, Json, tool, tool_router};
use serde::{Deserialize, Serialize};

use crate::machine::ProviderSetup;
use crate::server::HuddleServer;
use crate::sources::Sources;

const READY: &str = "ready";
const ROUTER_DOWN: &str = "router down, quota unknown";
const NOT_ASKED: &str =
    "the router has not asked this provider yet; it asks while a dashboard is open";
const NO_QUOTA_API: &str = "this provider has no quota API";
const NOT_REPORTED: &str = "not reported";

/// One provider's setup.
#[derive(Debug, Serialize, JsonSchema)]
pub struct ProviderInfo {
    pub id: String,
    pub name: String,
    /// Whether the router serves this provider's lane.
    pub lane: bool,
    /// Whether a key is stored. The key itself never leaves huddle.
    pub key_present: bool,
    pub models: Vec<String>,
    /// The agent files in place, by model id.
    pub agents: Vec<String>,
    /// `ready`, or the command that finishes setup.
    pub next_step: String,
}

/// What `huddle_providers` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct ProvidersAnswer {
    pub providers: Vec<ProviderInfo>,
}

fn next_step(setup: &ProviderSetup) -> String {
    match (setup.lane, setup.key_present) {
        (true, true) => READY.to_string(),
        (false, _) => format!("run {}setup", setup.slash),
        (true, false) => format!("add a key: run {}setup", setup.slash),
    }
}

pub fn providers(sources: &dyn Sources) -> ProvidersAnswer {
    let providers = sources
        .provider_setups()
        .into_iter()
        .map(|setup| ProviderInfo {
            next_step: next_step(&setup),
            id: setup.id.to_string(),
            name: setup.display.to_string(),
            lane: setup.lane,
            key_present: setup.key_present,
            models: setup.models.iter().map(|id| id.to_string()).collect(),
            agents: setup.agents.iter().map(|id| id.to_string()).collect(),
        })
        .collect();
    ProvidersAnswer { providers }
}

/// One usage window of a plan.
#[derive(Debug, Serialize, JsonSchema)]
pub struct QuotaWindow {
    pub name: String,
    pub used_pct: Option<u64>,
    /// When the window resets, epoch milliseconds.
    pub resets_ms: Option<u64>,
    pub note: Option<String>,
}

#[derive(Debug, Serialize, JsonSchema)]
pub struct QuotaBalance {
    pub amount: f64,
    pub currency: String,
}

/// One provider's plan or balance.
#[derive(Debug, Serialize, JsonSchema)]
pub struct ProviderQuota {
    pub id: String,
    pub name: String,
    /// `known` when the router holds an answer, `unknown` otherwise.
    pub status: String,
    /// `plan` or `balance`, when the router said.
    pub kind: Option<String>,
    pub windows: Vec<QuotaWindow>,
    pub balance: Option<QuotaBalance>,
    /// When the router last asked the provider, epoch milliseconds.
    pub fetched_ms: Option<u64>,
    /// Why the quota is unknown.
    pub note: Option<String>,
}

/// What `huddle_quota` answers.
#[derive(Debug, Serialize, JsonSchema)]
pub struct QuotaAnswer {
    pub router_up: bool,
    pub providers: Vec<ProviderQuota>,
}

#[derive(Debug, Default, Deserialize)]
struct StateShape {
    #[serde(default)]
    providers: BTreeMap<String, ProviderShape>,
}

#[derive(Debug, Default, Deserialize)]
struct ProviderShape {
    #[serde(default)]
    quota: ViewShape,
}

#[derive(Debug, Default, Deserialize)]
struct ViewShape {
    #[serde(default)]
    kind: String,
    #[serde(default)]
    windows: Vec<WindowShape>,
    balance: Option<BalanceShape>,
    fetched_ms: Option<u64>,
    error: Option<String>,
}

#[derive(Debug, Deserialize)]
struct WindowShape {
    name: String,
    used_pct: Option<u64>,
    resets_ms: Option<u64>,
    note: Option<String>,
}

#[derive(Debug, Deserialize)]
struct BalanceShape {
    amount: f64,
    currency: String,
}

fn unknown(id: &str, name: &str, note: &str) -> ProviderQuota {
    ProviderQuota {
        id: id.to_string(),
        name: name.to_string(),
        status: "unknown".to_string(),
        kind: None,
        windows: Vec::new(),
        balance: None,
        fetched_ms: None,
        note: Some(note.to_string()),
    }
}

fn from_view(id: &str, name: &str, view: ViewShape) -> ProviderQuota {
    let held = view.balance.is_some()
        || view
            .windows
            .iter()
            .any(|w| w.used_pct.is_some() || w.resets_ms.is_some());
    let note = match (&view.error, held) {
        (Some(error), _) => Some(error.clone()),
        (None, false) => Some(NOT_REPORTED.to_string()),
        (None, true) => None,
    };
    ProviderQuota {
        id: id.to_string(),
        name: name.to_string(),
        status: if view.error.is_none() && held {
            "known"
        } else {
            "unknown"
        }
        .to_string(),
        kind: (view.kind != "none" && !view.kind.is_empty()).then_some(view.kind),
        windows: view
            .windows
            .into_iter()
            .map(|w| QuotaWindow {
                name: w.name,
                used_pct: w.used_pct,
                resets_ms: w.resets_ms,
                note: w.note,
            })
            .collect(),
        balance: view.balance.map(|b| QuotaBalance {
            amount: b.amount,
            currency: b.currency,
        }),
        fetched_ms: view.fetched_ms,
        note,
    }
}

fn has_quota_api(provider: Option<&Provider>) -> bool {
    provider.is_some_and(|p| p.quota != Quota::None)
}

/// Anthropic and each set-up provider, read from the router's state while it answers.
pub fn quota(sources: &dyn Sources) -> QuotaAnswer {
    let state = sources.router_state();
    let router_up = state.is_some();
    let mut views = state
        .and_then(|value| serde_json::from_value::<StateShape>(value).ok())
        .unwrap_or_default()
        .providers;
    let mut rows = Vec::new();
    if router_up && let Some(view) = views.remove("anthropic") {
        rows.push(from_view("anthropic", "Anthropic", view.quota));
    }
    for setup in sources.provider_setups().iter().filter(|s| s.lane) {
        let row = match views.remove(setup.id) {
            _ if !router_up => unknown(setup.id, setup.display, ROUTER_DOWN),
            Some(view) => from_view(setup.id, setup.display, view.quota),
            None if !has_quota_api(provider_by_id(setup.id)) => {
                unknown(setup.id, setup.display, NO_QUOTA_API)
            }
            None => unknown(setup.id, setup.display, NOT_ASKED),
        };
        rows.push(row);
    }
    QuotaAnswer {
        router_up,
        providers: rows,
    }
}

#[tool_router(router = providers_router, vis = "pub(crate)")]
impl HuddleServer {
    /// Which providers are set up: lane, whether a key is stored (never the key), models, agent
    /// files and the next setup step.
    #[tool(name = "huddle_providers")]
    async fn huddle_providers(&self) -> Result<Json<ProvidersAnswer>, ErrorData> {
        Ok(Json(self.with_sources(|sources| providers(sources)).await?))
    }

    /// Each set-up provider's plan or balance with resets, from the router while it is up;
    /// "router down, quota unknown" otherwise.
    #[tool(name = "huddle_quota")]
    async fn huddle_quota(&self) -> Result<Json<QuotaAnswer>, ErrorData> {
        Ok(Json(self.with_sources(|sources| quota(sources)).await?))
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use huddle_history::History;
    use serde_json::{Value, json};

    use super::*;

    struct Fake {
        state: Option<Value>,
    }

    impl Sources for Fake {
        fn router_up(&self) -> bool {
            self.state.is_some()
        }
        fn binary_path(&self) -> PathBuf {
            PathBuf::new()
        }
        fn open_history(&self) -> Option<History> {
            None
        }
        fn router_state(&self) -> Option<Value> {
            self.state.clone()
        }
        fn state_dir(&self) -> PathBuf {
            PathBuf::new()
        }
        fn provider_setups(&self) -> Vec<ProviderSetup> {
            ["zai", "qwen", "kimi"]
                .iter()
                .map(|id| {
                    let provider = provider_by_id(id).unwrap();
                    ProviderSetup {
                        id: provider.id,
                        display: provider.display,
                        slash: provider.slash,
                        lane: *id != "kimi",
                        key_present: true,
                        models: vec![provider.main.id],
                        agents: Vec::new(),
                    }
                })
                .collect()
        }
    }

    fn row<'a>(answer: &'a QuotaAnswer, id: &str) -> &'a ProviderQuota {
        answer.providers.iter().find(|p| p.id == id).unwrap()
    }

    #[test]
    fn quota_reports_what_the_router_holds() {
        let state = json!({"providers": {
            "zai": {"quota": {
                "kind": "plan",
                "windows": [{"name": "5 hours", "used_pct": 40, "resets_ms": 99, "note": null}],
                "balance": null, "fetched_ms": 5, "error": null, "unverified": false
            }},
            "kimi": {"quota": {"kind": "balance", "windows": [], "balance": {"amount": 3.5, "currency": "USD"},
                "fetched_ms": 5, "error": null, "unverified": false}}
        }});
        let answer = quota(&Fake { state: Some(state) });
        assert!(answer.router_up);
        let zai = row(&answer, "zai");
        assert_eq!(zai.status, "known");
        assert_eq!(zai.windows[0].used_pct, Some(40));
        assert_eq!(row(&answer, "qwen").note.as_deref(), Some(NO_QUOTA_API));
        assert!(answer.providers.iter().all(|p| p.id != "kimi"));
    }

    #[test]
    fn a_provider_error_reads_unknown() {
        let state = json!({"providers": {"zai": {"quota": {
            "kind": "plan", "windows": [], "balance": null, "fetched_ms": 5,
            "error": "quota unknown", "unverified": true
        }}}});
        let answer = quota(&Fake { state: Some(state) });
        let zai = row(&answer, "zai");
        assert_eq!(zai.status, "unknown");
        assert_eq!(zai.note.as_deref(), Some("quota unknown"));
    }

    #[test]
    fn a_down_router_says_unknown_for_every_lane() {
        let answer = quota(&Fake { state: None });
        assert!(!answer.router_up);
        assert_eq!(answer.providers.len(), 2);
        assert!(
            answer
                .providers
                .iter()
                .all(|p| p.note.as_deref() == Some(ROUTER_DOWN))
        );
    }
}
