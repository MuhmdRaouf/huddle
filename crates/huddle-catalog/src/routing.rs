//! Model routing: which lane serves a model id.

use crate::providers::{Provider, provider_by_id};

/// The lane a model routes to: Anthropic or one provider's lane.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Route {
    Anthropic,
    Lane(&'static Provider),
}

/// The ids a lane answers to once `<P>_MODEL_MAIN` and `<P>_MODEL_FLASH` are applied.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolvedIds {
    pub provider: &'static str,
    pub main: String,
    pub flash: String,
}

/// `decide_resolved(model, enabled, &[])`.
pub fn decide(model: &str, enabled: &[&str]) -> Route {
    decide_resolved(model, enabled, &[])
}

/// Rule, in this order:
/// 1. `model` empty, or starting `claude-`: `Route::Anthropic`.
/// 2. For each id in `enabled` (in the given order), its resolved main or flash id equal to `model` wins.
/// 3. Else, over every `enabled` provider's `model_prefixes`, the longest prefix `model` starts with wins;
///    a tie goes to the provider earlier in `enabled`.
/// 4. Else `Route::Anthropic`.
///
/// Prefixes are case-sensitive. A name in `enabled` no catalog provider owns is ignored. An entry of
/// `resolved` replaces that provider's catalog ids in step 2; providers without an entry use the catalog ids.
pub fn decide_resolved(model: &str, enabled: &[&str], resolved: &[ResolvedIds]) -> Route {
    if model.is_empty() || model.starts_with("claude-") {
        return Route::Anthropic;
    }
    for name in enabled {
        let Some(provider) = provider_by_id(name) else {
            continue;
        };
        let Some(entry) = resolved.iter().find(|r| r.provider == provider.id) else {
            if model == provider.main.id || model == provider.flash.id {
                return Route::Lane(provider);
            }
            continue;
        };
        if model == entry.main || model == entry.flash {
            return Route::Lane(provider);
        }
    }
    let mut best: Option<(&'static Provider, usize)> = None;
    for name in enabled {
        let Some(provider) = provider_by_id(name) else {
            continue;
        };
        for prefix in provider.model_prefixes {
            let longer = match best {
                None => true,
                Some((_, len)) => prefix.len() > len,
            };
            if model.starts_with(prefix) && longer {
                best = Some((provider, prefix.len()));
            }
        }
    }
    best.map_or(Route::Anthropic, |(provider, _)| Route::Lane(provider))
}
