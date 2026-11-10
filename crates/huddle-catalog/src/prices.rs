//! The flat price table every cost estimate reads from: each catalog model's list rates in USD
//! per million tokens. A claude id stays unpriced here; cache write bills at the base rates, and
//! no request conditions (tier, peak, fast mode) are read off this table.

/// The day the price pages were read, as the estimate line names it.
pub const PRICES_RETRIEVED: &str = "2026-10-08";

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
static PRICES: [(&str, Price); 12] = [
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
];

/// One model's list price; a model the table does not know (a claude id, a made-up id) has none.
pub fn price_of(model: &str) -> Option<&'static Price> {
    PRICES
        .iter()
        .find(|(id, _)| *id == model)
        .map(|(_, price)| price)
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
