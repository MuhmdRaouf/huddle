use huddle_config::env::Env;
use huddle_harness::env::{harness_env, names_router};

fn value_of(vars: &[(String, String)], name: &str) -> Option<String> {
    vars.iter().find(|(k, _)| k == name).map(|(_, v)| v.clone())
}

#[test]
fn the_users_own_env_passes_and_is_sorted() {
    let env = Env::with(&[
        ("ZULU", "z"),
        ("HOME", "/h"),
        ("PATH", "/bin"),
        ("OPENROUTER_API_KEY", "own"),
        ("ANTHROPIC_API_KEY", "keeps"),
        ("CLAUDE", "keeps"),
    ]);
    let got = harness_env(&env, &[]);
    let names: Vec<&str> = got.iter().map(|(k, _)| k.as_str()).collect();
    assert_eq!(
        names,
        vec![
            "ANTHROPIC_API_KEY",
            "CLAUDE",
            "HOME",
            "OPENROUTER_API_KEY",
            "PATH",
            "ZULU"
        ]
    );
}

#[test]
fn the_five_providers_never_reach_a_harness() {
    let env = Env::with(&[
        ("ZAI_API_KEY", "k"),
        ("ZAI_MODEL_MAIN", "m"),
        ("KIMI_API_KEY", "k"),
        ("DEEPSEEK_API_KEY", "k"),
        ("MINIMAX_API_KEY", "k"),
        ("QWEN_API_KEY", "k"),
        ("DASHSCOPE_API_KEY", "k"),
        ("HOME", "/h"),
    ]);
    let got = harness_env(&env, &[]);
    assert_eq!(got, vec![("HOME".to_string(), "/h".to_string())]);
}

#[test]
fn claude_session_vars_are_withheld() {
    let env = Env::with(&[
        ("ANTHROPIC_BASE_URL", "http://x"),
        ("ANTHROPIC_AUTH_TOKEN", "t"),
        ("ANTHROPIC_MODEL", "m"),
        ("ANTHROPIC_SMALL_FAST_MODEL", "m"),
        ("ANTHROPIC_CUSTOM_HEADERS", "h"),
        ("ANTHROPIC_DEFAULT_OPUS_MODEL", "m"),
        ("CLAUDE_CODE_ENTRYPOINT", "cli"),
        ("CLAUDE_PROJECT_DIR", "/p"),
        ("CLAUDECODE", "1"),
        ("KEEP", "1"),
    ]);
    assert_eq!(
        harness_env(&env, &[]),
        vec![("KEEP".to_string(), "1".to_string())]
    );
}

#[test]
fn a_proxy_that_names_the_router_goes_and_takes_the_ca_with_it() {
    for name in [
        "HTTPS_PROXY",
        "https_proxy",
        "HTTP_PROXY",
        "http_proxy",
        "ALL_PROXY",
        "all_proxy",
    ] {
        let env = Env::with(&[
            (name, "http://127.0.0.1:4835"),
            ("NODE_EXTRA_CA_CERTS", "/ca.pem"),
            ("NO_PROXY", "localhost,127.0.0.1"),
        ]);
        let got = harness_env(&env, &[]);
        assert_eq!(value_of(&got, name), None, "{name}");
        assert_eq!(value_of(&got, "NODE_EXTRA_CA_CERTS"), None, "{name}");
        assert_eq!(
            value_of(&got, "NO_PROXY"),
            Some("localhost,127.0.0.1".to_string()),
            "{name}"
        );
    }
}

#[test]
fn a_corporate_proxy_and_its_ca_stay() {
    let env = Env::with(&[
        ("HTTPS_PROXY", "http://proxy.corp:8080"),
        ("NODE_EXTRA_CA_CERTS", "/corp.pem"),
    ]);
    let got = harness_env(&env, &[]);
    assert_eq!(
        value_of(&got, "HTTPS_PROXY"),
        Some("http://proxy.corp:8080".to_string())
    );
    assert_eq!(
        value_of(&got, "NODE_EXTRA_CA_CERTS"),
        Some("/corp.pem".to_string())
    );
}

#[test]
fn the_router_is_recognised_on_its_moved_port_too() {
    let env = Env::with(&[
        ("HTTPS_PROXY", "127.0.0.1:4242"),
        ("HUDDLE_ROUTER_PORT", "4242"),
        ("NODE_EXTRA_CA_CERTS", "/ca.pem"),
    ]);
    let got = harness_env(&env, &[]);
    assert_eq!(value_of(&got, "HTTPS_PROXY"), None);
    assert_eq!(value_of(&got, "NODE_EXTRA_CA_CERTS"), None);
}

#[test]
fn extra_is_added_and_wins() {
    let env = Env::with(&[("A", "1"), ("B", "2")]);
    let extra = vec![
        ("B".to_string(), "9".to_string()),
        ("C".to_string(), "3".to_string()),
    ];
    assert_eq!(
        harness_env(&env, &extra),
        vec![
            ("A".to_string(), "1".to_string()),
            ("B".to_string(), "9".to_string()),
            ("C".to_string(), "3".to_string())
        ]
    );
}

#[test]
fn names_router_reads_host_and_port() {
    assert!(names_router("http://127.0.0.1:4835", None));
    assert!(names_router("127.0.0.1:4835", None));
    assert!(names_router("https://localhost:4835/", None));
    assert!(names_router("http://u:p@127.9.9.9:4835", None));
    assert!(names_router("http://[::1]:4835", None));
    assert!(names_router("http://LOCALHOST:4835", None));
    assert!(!names_router("http://127.0.0.1:4836", None));
    assert!(names_router("http://127.0.0.1:4836", Some(4836)));
    assert!(!names_router("http://example.com:4835", None));
    assert!(!names_router("http://127.0.0.1", None));
}
