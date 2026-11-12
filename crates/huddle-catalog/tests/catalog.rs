use huddle_catalog::providers::{ModelRef, provider_by_id, provider_ids, providers};

/// One row of the models table: provider, tier, model id, label, behaves-as hint, default
/// effort.
type ModelRow = (
    &'static str,
    &'static str,
    &'static str,
    &'static str,
    Option<&'static str>,
    Option<&'static str>,
);

/// One row of the keys table: provider, key env names, billing and keys URLs, strip
/// list, model prefixes.
type KeyRow = (
    &'static str,
    &'static [&'static str],
    &'static str,
    &'static str,
    &'static [&'static str],
    &'static [&'static str],
);

fn model(p: &str, tier: &str) -> &'static ModelRef {
    let provider = provider_by_id(p).expect("known provider");
    match tier {
        "main" => &provider.main,
        _ => &provider.flash,
    }
}

#[test]
fn there_are_five_providers_in_the_compiled_order() {
    let ids: Vec<_> = providers().iter().map(|p| p.id).collect();
    assert_eq!(ids, ["zai", "kimi", "deepseek", "minimax", "qwen"]);
    assert_eq!(
        provider_ids(),
        ["zai", "kimi", "deepseek", "minimax", "qwen"]
    );
    assert!(provider_by_id("madeup").is_none());
}

#[test]
fn display_slash_prefix_and_endpoint_are_the_table_values() {
    let want = [
        (
            "zai",
            "Z.ai GLM",
            "/huddle:zai:",
            "ZAI",
            "https://api.z.ai/api/anthropic",
        ),
        (
            "kimi",
            "Moonshot Kimi",
            "/huddle:kimi:",
            "KIMI",
            "https://api.moonshot.ai/anthropic",
        ),
        (
            "deepseek",
            "DeepSeek",
            "/huddle:deepseek:",
            "DEEPSEEK",
            "https://api.deepseek.com/anthropic",
        ),
        (
            "minimax",
            "MiniMax",
            "/huddle:minimax:",
            "MINIMAX",
            "https://api.minimax.io/anthropic",
        ),
        (
            "qwen",
            "Alibaba Qwen",
            "/huddle:qwen:",
            "QWEN",
            "https://dashscope-intl.aliyuncs.com/apps/anthropic",
        ),
    ];
    for (id, display, slash, prefix, url) in want {
        let p = provider_by_id(id).expect("provider");
        assert_eq!(
            (p.display, p.slash, p.env_prefix, p.base_url),
            (display, slash, prefix, url),
            "{id}"
        );
    }
}

#[test]
fn models_carry_the_table_labels_hints_and_efforts() {
    let want: [ModelRow; 10] = [
        (
            "zai",
            "main",
            "glm-5.3",
            "GLM 5.3",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        (
            "zai",
            "flash",
            "glm-5.3-flash",
            "GLM 5.3 Flash",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        (
            "kimi",
            "main",
            "kimi-k3",
            "Kimi K3",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        ("kimi", "flash", "kimi-k2.6", "Kimi K2.6", None, None),
        (
            "deepseek",
            "main",
            "deepseek-v4-pro",
            "DeepSeek V4 Pro",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        (
            "deepseek",
            "flash",
            "deepseek-flash",
            "DeepSeek Flash",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        (
            "minimax",
            "main",
            "MiniMax-M3",
            "MiniMax M3",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        (
            "minimax",
            "flash",
            "MiniMax-M2.7-highspeed",
            "MiniMax M2.7 Highspeed",
            None,
            Some("xhigh"),
        ),
        (
            "qwen",
            "main",
            "qwen3.8-max",
            "Qwen 3.8 Max",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
        (
            "qwen",
            "flash",
            "qwen3.8-flash",
            "Qwen 3.8 Flash",
            Some("claude-opus-5-5"),
            Some("xhigh"),
        ),
    ];
    for (p, tier, id, label, behaves, effort) in want {
        let m = model(p, tier);
        assert_eq!(
            (m.id, m.label, m.behaves_as, m.default_effort),
            (id, label, behaves, effort),
            "{p} {tier}"
        );
    }
}

#[test]
fn keys_billing_strip_and_prefixes_are_the_table_values() {
    let want: [KeyRow; 5] = [
        (
            "zai",
            &["ZAI_API_KEY"],
            "https://z.ai/manage-apikey/billing",
            "https://z.ai/manage-apikey/apikey-list",
            &[],
            &["glm-"],
        ),
        (
            "kimi",
            &["KIMI_API_KEY"],
            "https://platform.kimi.ai/console",
            "https://platform.kimi.ai/console/api-keys",
            &[],
            &["kimi-"],
        ),
        (
            "deepseek",
            &["DEEPSEEK_API_KEY"],
            "https://platform.deepseek.com/top_up",
            "https://platform.deepseek.com/api_keys",
            &[],
            &["deepseek-"],
        ),
        (
            "minimax",
            &["MINIMAX_API_KEY"],
            "https://platform.minimax.io/user-center/payment/balance",
            "https://platform.minimax.io/console/access",
            &[],
            &["MiniMax-"],
        ),
        (
            "qwen",
            &["QWEN_API_KEY", "DASHSCOPE_API_KEY"],
            "https://usercenter2-intl.aliyun.com/billing",
            "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key",
            &["context_management"],
            &["qwen"],
        ),
    ];
    for (id, key_env, billing, keys, strip, prefixes) in want {
        let p = provider_by_id(id).expect("provider");
        assert_eq!(p.key_env, key_env, "{id}");
        assert_eq!(p.billing_url, billing, "{id}");
        assert_eq!(p.keys_url, keys, "{id}");
        assert_eq!(p.strip, strip, "{id}");
        assert_eq!(p.model_prefixes, prefixes, "{id}");
    }
}

#[test]
fn nothing_is_shared_between_providers() {
    let all = providers();
    for (i, a) in all.iter().enumerate() {
        for b in &all[i + 1..] {
            assert_ne!(a.id, b.id);
            assert_ne!(a.display, b.display);
            assert_ne!(a.env_prefix, b.env_prefix);
            assert!(
                a.key_env.iter().all(|k| !b.key_env.contains(k)),
                "{} {}",
                a.id,
                b.id
            );
            assert_ne!(a.main.id, b.main.id);
            assert_ne!(a.flash.id, b.flash.id);
        }
    }
}

#[test]
fn each_model_is_claimed_by_its_own_prefixes_and_no_prefix_claims_claude() {
    for p in providers() {
        for prefix in p.model_prefixes {
            assert!(
                !"claude-opus-5-5".starts_with(prefix),
                "{} prefix {prefix}",
                p.id
            );
        }
        for id in [p.main.id, p.flash.id] {
            assert!(
                p.model_prefixes.iter().any(|x| id.starts_with(x)),
                "{} model {id}",
                p.id
            );
            for other in providers().iter().filter(|o| o.id != p.id) {
                assert!(
                    !other.model_prefixes.iter().any(|x| id.starts_with(x)),
                    "{id} also claimed by {}",
                    other.id
                );
            }
        }
    }
}

#[test]
fn only_the_exempt_models_have_no_window_hint() {
    let no_hint = ["kimi-k2.6", "MiniMax-M2.7-highspeed"];
    for p in providers() {
        for m in [p.main, p.flash] {
            assert_eq!(m.behaves_as.is_none(), no_hint.contains(&m.id), "{}", m.id);
        }
    }
}
