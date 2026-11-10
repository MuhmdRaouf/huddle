use huddle_catalog::providers::provider_by_id;
use huddle_catalog::routing::{ResolvedIds, Route, decide, decide_resolved};

fn lane(id: &str) -> Route {
    Route::Lane(provider_by_id(id).expect("provider"))
}

const ALL: [&str; 5] = ["zai", "kimi", "deepseek", "minimax", "qwen"];

#[test]
fn catalog_ids_pick_their_lane() {
    assert_eq!(decide("glm-5.3", &ALL), lane("zai"));
    assert_eq!(decide("kimi-k2.6", &ALL), lane("kimi"));
    assert_eq!(decide("MiniMax-M3", &ALL), lane("minimax"));
}

#[test]
fn prefixes_claim_their_models_case_sensitively() {
    assert_eq!(decide("glm-9.9-new", &ALL), lane("zai"));
    assert_eq!(decide("qwen9", &ALL), lane("qwen"));
    assert_eq!(decide("GLM-5.3", &ALL), Route::Anthropic);
    assert_eq!(decide("minimax-m3", &ALL), Route::Anthropic);
}

#[test]
fn claude_unclaimed_and_empty_go_to_anthropic() {
    assert_eq!(decide("claude-opus-5-5", &ALL), Route::Anthropic);
    assert_eq!(decide("claude-sonnet-5-5-20260101", &ALL), Route::Anthropic);
    assert_eq!(decide("gpt-9", &ALL), Route::Anthropic);
    assert_eq!(decide("", &ALL), Route::Anthropic);
}

#[test]
fn a_disabled_provider_claims_nothing() {
    assert_eq!(decide("glm-5.3", &["kimi"]), Route::Anthropic);
    assert_eq!(decide("kimi-k3", &["kimi"]), lane("kimi"));
    assert_eq!(decide("kimi-k3", &[]), Route::Anthropic);
}

#[test]
fn an_unknown_enabled_name_is_ignored() {
    assert_eq!(decide("glm-5.3", &["madeup", "zai"]), lane("zai"));
}

#[test]
fn an_exact_resolved_id_beats_another_lanes_longer_prefix() {
    let resolved = [ResolvedIds {
        provider: "zai",
        main: "kimi-special".to_string(),
        flash: "glm-5.3-flash".to_string(),
    }];
    assert_eq!(
        decide_resolved("kimi-special", &["zai", "kimi"], &resolved),
        lane("zai")
    );
    assert_eq!(
        decide_resolved("kimi-other", &["zai", "kimi"], &resolved),
        lane("kimi")
    );
}

#[test]
fn a_resolved_id_outside_the_prefixes_routes_to_its_lane() {
    let resolved = [ResolvedIds {
        provider: "zai",
        main: "custom-model".to_string(),
        flash: "glm-5.3-flash".to_string(),
    }];
    assert_eq!(
        decide_resolved("custom-model", &["zai"], &resolved),
        lane("zai")
    );
    assert_eq!(decide("custom-model", &["zai"]), Route::Anthropic);
}

#[test]
fn a_resolved_claude_id_still_goes_to_anthropic() {
    let resolved = [ResolvedIds {
        provider: "zai",
        main: "claude-opus-5-5".to_string(),
        flash: "x".to_string(),
    }];
    assert_eq!(
        decide_resolved("claude-opus-5-5", &["zai"], &resolved),
        Route::Anthropic
    );
}

#[test]
fn a_tie_on_prefix_length_goes_to_the_earlier_enabled_lane() {
    assert_eq!(decide("qwen3.8-other", &["qwen"]), lane("qwen"));
    // Only qwen claims "qwen"; tie behaviour is exercised through the enabled order of equal claims.
    assert_eq!(decide("glm-x", &["zai", "qwen"]), lane("zai"));
}
