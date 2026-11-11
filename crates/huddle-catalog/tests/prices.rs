use huddle_catalog::prices::{TokenCounts, cost_usd, is_api_equivalent, price_of};

#[test]
fn a_provider_model_is_priced_and_an_unknown_id_is_not() {
    assert_eq!(price_of("glm-5.3").expect("price").input, 1.4);
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

#[test]
fn t7_claude_models_have_api_equivalent_prices() {
    let rates = [
        ("claude-fable-5-1", 10.0, 50.0, 0.25, 12.5),
        ("claude-opus-5-5", 4.0, 20.0, 0.2, 5.0),
        ("claude-sonnet-5-5", 2.0, 10.0, 0.1, 2.5),
        ("claude-haiku-5-5", 0.1, 0.5, 0.01, 0.125),
    ];
    for (id, input, output, cache_read, cache_write) in rates {
        let price = price_of(id).unwrap_or_else(|| panic!("{id} has no price"));
        assert_eq!(price.input, input, "{id} input");
        assert_eq!(price.output, output, "{id} output");
        assert_eq!(price.cache_read, cache_read, "{id} cache read");
        assert_eq!(price.cache_write, cache_write, "{id} cache write");
        assert!(price.verified, "{id}");
    }
}

#[test]
fn t7_suffixed_claude_ids_price_as_their_base_model() {
    let opus = price_of("claude-opus-5-5");
    assert!(opus.is_some());
    assert_eq!(price_of("claude-opus-5-5[1m]"), opus);
    assert_eq!(price_of("claude-opus-5-5-20261001[1m]"), opus);
    assert_eq!(
        price_of("claude-haiku-5-5-20261001"),
        price_of("claude-haiku-5-5")
    );
    assert!(price_of("claude-haiku-5-5").is_some());
    assert_eq!(price_of("claude-unknown-9"), None);
}

#[test]
fn t7_only_claude_ids_are_api_equivalent() {
    assert!(is_api_equivalent("claude-sonnet-5-5"));
    assert!(is_api_equivalent("claude-opus-5-5[1m]"));
    assert!(is_api_equivalent("claude-haiku-5-5-20261001"));
    assert!(!is_api_equivalent("glm-5.3"));
    assert!(!is_api_equivalent("madeup"));
}

#[test]
fn t7_cost_of_a_claude_request() {
    let tokens = TokenCounts {
        input: 1000,
        output: 2000,
        cache_read: 100_000,
        cache_write: 10_000,
    };
    // 0.004 + 0.04 + 0.02 + 0.05
    let cost = cost_usd("claude-opus-5-5[1m]", &tokens);
    assert!((cost - 0.114).abs() < 1e-9, "{cost}");
}
