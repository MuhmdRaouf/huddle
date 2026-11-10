use huddle_catalog::prices::{TokenCounts, cost_usd, price_of};

#[test]
fn a_provider_model_is_priced_and_a_claude_id_is_not() {
    assert_eq!(price_of("glm-5.3").expect("price").input, 1.4);
    assert_eq!(price_of("claude-opus-5-5"), None);
    assert_eq!(price_of("madeup"), None);
    assert!(!price_of("qwen3.8-max").expect("price").verified);
    assert!(price_of("kimi-k3").expect("price").verified);
}

#[test]
fn the_cost_sums_every_rate_per_million() {
    let tokens = TokenCounts {
        input: 1000,
        output: 100,
        cache_read: 9000,
        cache_write: 0,
    };
    let cost = cost_usd("glm-5.3", &tokens);
    assert!((cost - 0.00418).abs() < 1e-9, "{cost}");
    assert_eq!(cost_usd("madeup", &tokens), 0.0);
}
