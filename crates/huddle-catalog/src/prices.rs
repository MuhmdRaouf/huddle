//! The flat price table every cost estimate reads from: each catalog model's list rates in USD
//! per million tokens. Claude models carry the API list rates: a Claude plan is flat-rate, so those
//! prices say what the traffic would cost on the API (`is_api_equivalent`). A provider model with
//! no cache write rate bills cache writes at zero, and no request conditions (tier, peak, fast
//! mode, Haiku's over-100k-token rates) are read off this table.

/// The day the price pages were read, as the estimate line names it.
pub const PRICES_RETRIEVED: &str = "2026-10-08";

/// The day the Claude rates were read from platform.claude.com/docs/en/about-claude/pricing.
pub const CLAUDE_PRICES_RETRIEVED: &str = "2026-11-11";

/// One model's list rates in USD per million tokens. `verified` is false where the cache rates
/// are inferred rather than read on the provider's page.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Price {
    pub input: f64,
    pub output: f64,
    pub cache_read: f64,
    pub cache_write: f64,
    pub verified: bool,
}

/// Token counts, as the route events record them.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct TokenCounts {
    pub input: u64,
    pub output: u64,
    pub cache_read: u64,
    pub cache_write: u64,
}

/// Every priced model, by its catalog id.
static PRICES: [(&str, Price); 16] = [
    (
        "glm-5.3",
        Price {
            input: 1.4,
            output: 4.4,
            cache_read: 0.26,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "glm-5.3-flash",
        Price {
            input: 0.15,
            output: 0.5,
            cache_read: 0.03,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "glm-5.3-flashx",
        Price {
            input: 0.37,
            output: 1.25,
            cache_read: 0.075,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "kimi-k3",
        Price {
            input: 3.0,
            output: 15.0,
            cache_read: 0.3,
            cache_write: 3.0,
            verified: true,
        },
    ),
    (
        "kimi-k2.6",
        Price {
            input: 0.95,
            output: 4.0,
            cache_read: 0.16,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "deepseek-flash",
        Price {
            input: 0.3,
            output: 1.2,
            cache_read: 0.006,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "deepseek-v4-pro",
        Price {
            input: 1.32,
            output: 3.96,
            cache_read: 0.044,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "MiniMax-M3",
        Price {
            input: 0.3,
            output: 1.2,
            cache_read: 0.06,
            cache_write: 0.0,
            verified: true,
        },
    ),
    (
        "MiniMax-M2.7",
        Price {
            input: 0.3,
            output: 1.2,
            cache_read: 0.06,
            cache_write: 0.375,
            verified: true,
        },
    ),
    (
        "MiniMax-M2.7-highspeed",
        Price {
            input: 0.6,
            output: 2.4,
            cache_read: 0.06,
            cache_write: 0.375,
            verified: true,
        },
    ),
    (
        "qwen3.8-max",
        Price {
            input: 2.0,
            output: 6.0,
            cache_read: 0.2,
            cache_write: 2.5,
            verified: false,
        },
    ),
    (
        "qwen3.8-flash",
        Price {
            input: 0.15,
            output: 0.47,
            cache_read: 0.015,
            cache_write: 0.19,
            verified: false,
        },
    ),
    (
        "claude-fable-5-1",
        Price {
            input: 10.0,
            output: 50.0,
            cache_read: 0.25,
            cache_write: 12.5,
            verified: true,
        },
    ),
    (
        "claude-opus-5-5",
        Price {
            input: 4.0,
            output: 20.0,
            cache_read: 0.2,
            cache_write: 5.0,
            verified: true,
        },
    ),
    (
        "claude-sonnet-5-5",
        Price {
            input: 2.0,
            output: 10.0,
            cache_read: 0.1,
            cache_write: 2.5,
            verified: true,
        },
    ),
    (
        "claude-haiku-5-5",
        Price {
            input: 0.1,
            output: 0.5,
            cache_read: 0.01,
            cache_write: 0.125,
            verified: true,
        },
    ),
];

/// One model's list price; a model the table does not know (a made-up id) has none. A Claude id
/// prices as its base model once a `[1m]` suffix and a trailing `-YYYYMMDD` date are dropped.
pub fn price_of(model: &str) -> Option<&'static Price> {
    let id = base_model(model);
    PRICES
        .iter()
        .find(|(known, _)| *known == id)
        .map(|(_, price)| price)
}

/// Whether the model is a Claude id, whose price is what the API would charge rather than what
/// the plan charges.
pub fn is_api_equivalent(model: &str) -> bool {
    base_model(model).starts_with("claude-") && price_of(model).is_some()
}

/// A Claude id without its context suffix and date; any other id unchanged.
fn base_model(model: &str) -> &str {
    if !model.starts_with("claude-") {
        return model;
    }
    let id = model.strip_suffix("[1m]").unwrap_or(model);
    match id.rsplit_once('-') {
        Some((base, date)) if date.len() == 8 && date.bytes().all(|b| b.is_ascii_digit()) => base,
        _ => id,
    }
}

/// What a model's tokens cost at its list price; a model with no price costs nothing rather than
/// the wrong sum.
pub fn cost_usd(model: &str, tokens: &TokenCounts) -> f64 {
    let Some(price) = price_of(model) else {
        return 0.0;
    };
    per_million(tokens.input, price.input)
        + per_million(tokens.output, price.output)
        + per_million(tokens.cache_read, price.cache_read)
        + per_million(tokens.cache_write, price.cache_write)
}

/// One token count at its per-million rate.
fn per_million(count: u64, rate: f64) -> f64 {
    (count as f64 / 1_000_000.0) * rate
}
