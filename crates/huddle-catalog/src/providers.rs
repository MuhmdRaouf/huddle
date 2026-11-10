//! The five providers' fixed data: what the router and setup read, as one static table.
//!
//! Covers id, display, slash command, env prefix, endpoint, the two models, key env names, key
//! file, billing and keys URLs, the strip list and the lane's model prefixes. The job engine's
//! fields (branch prefix, harness, tier names, agents, auth) are not catalog data.

/// One catalog model: its id, its display label, the Claude model it behaves as and its default
/// reasoning effort. `None` where the provider publishes no figure.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ModelRef {
    pub id: &'static str,
    pub label: &'static str,
    pub behaves_as: Option<&'static str>,
    pub default_effort: Option<&'static str>,
}

/// One provider as the router and setup read it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Provider {
    pub id: &'static str,
    pub display: &'static str,
    pub slash: &'static str,
    pub env_prefix: &'static str,
    pub base_url: &'static str,
    pub main: ModelRef,
    pub flash: ModelRef,
    /// API key environment variables in priority order (qwen also accepts DASHSCOPE_API_KEY).
    pub key_env: &'static [&'static str],
    /// The key file setup writes and the router reads instead (mode 600).
    pub key_file: &'static str,
    /// The billing or top-up page, named when the provider refuses for lack of balance.
    pub billing_url: &'static str,
    /// The page where the user creates an API key.
    pub keys_url: &'static str,
    /// Request fields this provider rejects; the router strips them from provider-bound requests.
    pub strip: &'static [&'static str],
    /// The model id prefixes the provider's lane claims in huddle-router.
    pub model_prefixes: &'static [&'static str],
}

/// The five providers in compiled order; lanes and lists use this order.
static PROVIDERS: [Provider; 5] = [
    Provider {
        id: "zai",
        display: "Z.ai GLM",
        slash: "/zai:",
        env_prefix: "ZAI",
        base_url: "https://api.z.ai/api/anthropic",
        main: ModelRef {
            id: "glm-5.3",
            label: "GLM 5.3",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        flash: ModelRef {
            id: "glm-5.3-flash",
            label: "GLM 5.3 Flash",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        key_env: &["ZAI_API_KEY"],
        key_file: "~/.config/zai-plugin-cc/env",
        billing_url: "https://z.ai/manage-apikey/billing",
        keys_url: "https://z.ai/manage-apikey/apikey-list",
        strip: &[],
        model_prefixes: &["glm-"],
    },
    Provider {
        id: "kimi",
        display: "Moonshot Kimi",
        slash: "/kimi:",
        env_prefix: "KIMI",
        base_url: "https://api.moonshot.ai/anthropic",
        main: ModelRef {
            id: "kimi-k3",
            label: "Kimi K3",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        // Moonshot publishes no effort control for kimi-k2.6, and its 262,144-token window is far below the 1M a behaves-as hint would name, so those stay unset.
        flash: ModelRef {
            id: "kimi-k2.6",
            label: "Kimi K2.6",
            behaves_as: None,
            default_effort: None,
        },
        key_env: &["KIMI_API_KEY"],
        key_file: "~/.config/kimi-plugin-cc/env",
        billing_url: "https://platform.kimi.ai/console",
        keys_url: "https://platform.kimi.ai/console/api-keys",
        strip: &[],
        model_prefixes: &["kimi-"],
    },
    Provider {
        id: "deepseek",
        display: "DeepSeek",
        slash: "/deepseek:",
        env_prefix: "DEEPSEEK",
        base_url: "https://api.deepseek.com/anthropic",
        main: ModelRef {
            id: "deepseek-v4-pro",
            label: "DeepSeek V4 Pro",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        flash: ModelRef {
            id: "deepseek-flash",
            label: "DeepSeek Flash",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        key_env: &["DEEPSEEK_API_KEY"],
        key_file: "~/.config/deepseek-plugin-cc/env",
        billing_url: "https://platform.deepseek.com/top_up",
        keys_url: "https://platform.deepseek.com/api_keys",
        strip: &[],
        model_prefixes: &["deepseek-"],
    },
    Provider {
        id: "minimax",
        display: "MiniMax",
        slash: "/minimax:",
        env_prefix: "MINIMAX",
        base_url: "https://api.minimax.io/anthropic",
        main: ModelRef {
            id: "MiniMax-M3",
            label: "MiniMax M3",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        // MiniMax-M2.7-highspeed runs a 204,800-token window, far below the 1M a behaves-as hint
        // would name.
        flash: ModelRef {
            id: "MiniMax-M2.7-highspeed",
            label: "MiniMax M2.7 Highspeed",
            behaves_as: None,
            default_effort: Some("xhigh"),
        },
        key_env: &["MINIMAX_API_KEY"],
        key_file: "~/.config/minimax-plugin-cc/env",
        billing_url: "https://platform.minimax.io/user-center/payment/balance",
        keys_url: "https://platform.minimax.io/console/access",
        strip: &[],
        model_prefixes: &["MiniMax-"],
    },
    Provider {
        id: "qwen",
        display: "Alibaba Qwen",
        slash: "/qwen:",
        env_prefix: "QWEN",
        base_url: "https://dashscope-intl.aliyuncs.com/apps/anthropic",
        main: ModelRef {
            id: "qwen3.8-max",
            label: "Qwen 3.8 Max",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        flash: ModelRef {
            id: "qwen3.8-flash",
            label: "Qwen 3.8 Flash",
            behaves_as: Some("claude-opus-5-5"),
            default_effort: Some("xhigh"),
        },
        key_env: &["QWEN_API_KEY", "DASHSCOPE_API_KEY"],
        key_file: "~/.config/qwen-plugin-cc/env",
        billing_url: "https://usercenter2-intl.aliyun.com/billing",
        keys_url: "https://modelstudio.console.alibabacloud.com/?tab=playground#/api-key",
        // Qwen rejects context_management.
        strip: &["context_management"],
        // "qwen" without a dash: it also claims qwen3.8-max and qwen3.8-flash.
        model_prefixes: &["qwen"],
    },
];

/// The five providers, zai, kimi, deepseek, minimax, qwen, in that order.
pub fn providers() -> &'static [Provider] {
    &PROVIDERS
}

/// The provider with this id, or None.
pub fn provider_by_id(id: &str) -> Option<&'static Provider> {
    providers().iter().find(|p| p.id == id)
}

/// The five provider ids, in compiled order.
pub fn provider_ids() -> [&'static str; 5] {
    ["zai", "kimi", "deepseek", "minimax", "qwen"]
}
