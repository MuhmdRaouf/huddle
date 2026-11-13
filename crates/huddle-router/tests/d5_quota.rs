//! D5: the router asks each set-up provider for its quota or balance, only while a dashboard is
//! open, at most every five minutes, and never lets a bad answer break the snapshot.

use std::sync::Arc;
use std::time::Duration;

use huddle_catalog::providers::{Provider, provider_by_id};
use huddle_keystore::port::SecretString;
use huddle_router::keycache::{KeyRead, KeySources};
use huddle_router::quota::{QuotaFetcher, QuotaKind, QuotaView, UrlFor};
use huddle_router::upstream::{UpstreamClient, UpstreamConfig};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};

const T0: u64 = 1_794_000_000_000;

fn client() -> Arc<UpstreamClient> {
    Arc::new(UpstreamClient::new(UpstreamConfig {
        env: huddle_config::env::Env::with(&[]),
        roots: rustls::RootCertStore::empty(),
        idle: Duration::from_secs(1),
    }))
}

fn key_of(provider: &'static Provider) -> SecretString {
    SecretString::new(format!("secret-{}", provider.id))
}

fn fetcher(urls: Vec<(&'static str, String)>) -> Arc<QuotaFetcher> {
    let url_for: UrlFor = Arc::new(move |provider| {
        urls.iter()
            .find(|(id, _)| *id == provider.id)
            .map(|(_, url)| url.clone())
    });
    QuotaFetcher::new(
        client(),
        Arc::new(|provider| {
            let key = key_of(provider);
            let file: KeyRead = Arc::new(|| Ok(None));
            KeySources {
                env_key: Some(key),
                os: None,
                file,
            }
        }),
        url_for,
    )
}

fn enabled(ids: &[&str]) -> Vec<String> {
    ids.iter().map(|id| id.to_string()).collect()
}

async fn answering(status: u16, body: &str) -> FakeUpstream {
    let body = body.to_owned();
    FakeUpstream::start(move |_, _| Step::answer(status, &body)).await
}

fn header(fake: &FakeUpstream, name: &str) -> Option<String> {
    fake.requests()[0]
        .headers
        .iter()
        .find(|(key, _)| key.eq_ignore_ascii_case(name))
        .map(|(_, value)| value.clone())
}

const ZAI_BODY: &str = r#"{"code":200,"success":true,"msg":"ok","data":{"level":"pro","limits":[
 {"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":63,"nextResetTime":1794018000000},
 {"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":12,"nextResetTime":1794500000000},
 {"type":"TIME_LIMIT","unit":5,"number":1,"percentage":4,"currentValue":4,"usage":100,"usageDetails":[],"nextResetTime":null}]}}"#;

#[tokio::test]
async fn d5_zai_quota_parses_windows_and_resets() {
    let fake = answering(200, ZAI_BODY).await;
    let quota = fetcher(vec![(
        "zai",
        format!("{}/api/monitor/usage/quota/limit", fake.url()),
    )]);
    quota.refresh(T0, 1, &enabled(&["zai"])).await;
    let view = quota.view().remove("zai").expect("zai row");
    assert_eq!(view.kind, QuotaKind::Plan);
    assert_eq!(view.error, None);
    assert_eq!(view.fetched_ms, Some(T0));
    let windows: Vec<_> = view
        .windows
        .iter()
        .map(|w| (w.name.as_str(), w.used_pct, w.resets_ms))
        .collect();
    assert_eq!(
        windows,
        vec![
            ("5 hours", Some(63), Some(1_794_018_000_000)),
            ("7 days", Some(12), Some(1_794_500_000_000)),
            ("Month", Some(4), None),
        ]
    );
    let request = &fake.requests()[0];
    assert_eq!(request.method, "GET");
    assert_eq!(request.path, "/api/monitor/usage/quota/limit");
    assert_eq!(
        header(&fake, "authorization").as_deref(),
        Some("secret-zai")
    );
    assert!(header(&fake, "host").is_some_and(|host| host.starts_with("127.0.0.1")));
}

#[tokio::test]
async fn d5_a_changed_or_failed_answer_is_quota_unknown() {
    let cases = [
        (200, r#"{"data":{"something":"else"}}"#),
        (200, "not json"),
        (
            200,
            r#"{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":"high"}]}}"#,
        ),
        (500, "{}"),
        (401, "{}"),
    ];
    for (status, body) in cases {
        let fake = answering(status, body).await;
        let quota = fetcher(vec![("zai", format!("{}/q", fake.url()))]);
        quota.refresh(T0, 1, &enabled(&["zai"])).await;
        let view = quota
            .view()
            .remove("zai")
            .expect("a row, never a failed snapshot");
        assert_eq!(
            view.error.as_deref(),
            Some("quota unknown"),
            "{status} {body}"
        );
        assert!(view.windows.is_empty() && view.balance.is_none());
    }
    // A host that does not answer is the same.
    let quota = fetcher(vec![("zai", "http://127.0.0.1:9/q".into())]);
    quota.refresh(T0, 1, &enabled(&["zai"])).await;
    assert_eq!(quota.view()["zai"].error.as_deref(), Some("quota unknown"));
}

#[tokio::test]
async fn d5_balance_providers_parse() {
    let deepseek = answering(
        200,
        r#"{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.50","granted_balance":"10.00","topped_up_balance":"100.50"}]}"#,
    )
    .await;
    let kimi = answering(
        200,
        r#"{"code":0,"data":{"available_balance":49.58,"voucher_balance":46.58,"cash_balance":3.0},"status":true}"#,
    )
    .await;
    let quota = fetcher(vec![
        ("deepseek", format!("{}/user/balance", deepseek.url())),
        ("kimi", format!("{}/v1/users/me/balance", kimi.url())),
    ]);
    quota.refresh(T0, 1, &enabled(&["deepseek", "kimi"])).await;
    let rows = quota.view();
    let deep = &rows["deepseek"];
    assert_eq!(deep.kind, QuotaKind::Balance);
    let balance = deep.balance.as_ref().expect("deepseek balance");
    assert_eq!((balance.amount, balance.currency.as_str()), (110.5, "CNY"));
    assert!(deep.windows.is_empty());
    let balance = rows["kimi"].balance.as_ref().expect("kimi balance");
    assert_eq!((balance.amount, balance.currency.as_str()), (49.58, "USD"));
    assert_eq!(
        header(&deepseek, "authorization").as_deref(),
        Some("Bearer secret-deepseek")
    );
    assert_eq!(
        header(&kimi, "authorization").as_deref(),
        Some("Bearer secret-kimi")
    );
}

#[tokio::test]
async fn d5_no_fetch_without_a_viewer_and_at_most_every_five_minutes() {
    let fake = answering(200, ZAI_BODY).await;
    let quota = fetcher(vec![("zai", format!("{}/q", fake.url()))]);
    let on = enabled(&["zai"]);

    quota.refresh(T0, 0, &on).await;
    assert_eq!(fake.requests().len(), 0, "no viewer, no request");
    assert!(quota.view().is_empty());

    quota.refresh(T0 + 1, 1, &enabled(&[])).await;
    assert_eq!(
        fake.requests().len(),
        0,
        "a provider that is not set up is not asked"
    );

    quota.refresh(T0 + 2, 1, &on).await;
    assert_eq!(fake.requests().len(), 1);
    quota.refresh(T0 + 2 + 299_999, 1, &on).await;
    assert_eq!(
        fake.requests().len(),
        1,
        "inside five minutes the cache answers"
    );
    quota.refresh(T0 + 2 + 299_999, 0, &on).await;
    assert_eq!(fake.requests().len(), 1);
    quota.refresh(T0 + 2 + 300_000, 1, &on).await;
    assert_eq!(fake.requests().len(), 2, "five minutes on, one more");
    assert_eq!(quota.view()["zai"].fetched_ms, Some(T0 + 2 + 300_000));
}

#[tokio::test]
async fn d5_a_failure_also_waits_five_minutes() {
    let fake = answering(500, "{}").await;
    let quota = fetcher(vec![("zai", format!("{}/q", fake.url()))]);
    let on = enabled(&["zai"]);
    quota.refresh(T0, 1, &on).await;
    quota.refresh(T0 + 10_000, 1, &on).await;
    assert_eq!(fake.requests().len(), 1);
}

#[tokio::test]
async fn d5_qwen_has_no_quota_row_and_asks_nobody() {
    let quota = fetcher(vec![]);
    quota.refresh(T0, 1, &enabled(&["qwen"])).await;
    let view: QuotaView = quota.view().remove("qwen").expect("qwen row");
    assert_eq!(view.kind, QuotaKind::None);
    assert_eq!(view.error, None);
}

#[tokio::test]
async fn d5_the_key_goes_only_to_its_provider_and_never_into_the_snapshot() {
    let zai = answering(200, ZAI_BODY).await;
    // The answer repeats the key, as a hostile or broken upstream might.
    let deepseek = answering(500, "secret-zai secret-deepseek").await;
    let quota = fetcher(vec![
        ("zai", format!("{}/q", zai.url())),
        ("deepseek", format!("{}/b", deepseek.url())),
    ]);
    quota.refresh(T0, 1, &enabled(&["zai", "deepseek"])).await;
    for request in zai.requests() {
        assert!(!String::from_utf8_lossy(&request.raw).contains("secret-deepseek"));
    }
    for request in deepseek.requests() {
        assert!(!String::from_utf8_lossy(&request.raw).contains("secret-zai"));
    }
    assert_eq!(zai.requests().len(), 1);
    assert_eq!(deepseek.requests().len(), 1);
    let json = serde_json::to_string(&quota.view()).expect("serializes");
    assert!(!json.contains("secret-"), "{json}");
}

#[tokio::test]
async fn d5_minimax_plan_is_read_and_marked_unverified() {
    let fake = answering(
        200,
        r#"{"model_remains":[{"model_name":"MiniMax-M*","current_interval_total_count":1500,"current_interval_usage_count":300,"remains_time":3600000}],"base_resp":{"status_code":0}}"#,
    )
    .await;
    let quota = fetcher(vec![("minimax", format!("{}/r", fake.url()))]);
    quota.refresh(T0, 1, &enabled(&["minimax"])).await;
    let view = quota.view().remove("minimax").expect("minimax row");
    assert!(view.unverified);
    assert_eq!(view.kind, QuotaKind::Plan);
    assert_eq!(view.windows.len(), 1);
    assert_eq!(view.windows[0].used_pct, Some(20));
    assert_eq!(view.windows[0].resets_ms, Some(T0 + 3_600_000));
}

async fn state_of(dashboard: &huddle_router::dashboard::Dashboard) -> serde_json::Value {
    use http_body_util::BodyExt;
    let body = dashboard
        .state_answer()
        .into_body()
        .collect()
        .await
        .expect("body")
        .to_bytes();
    serde_json::from_slice(&body).expect("snapshot json")
}

#[tokio::test]
async fn d5_the_dashboard_asks_only_while_a_page_is_open() {
    use huddle_router::dashboard::{Dashboard, DashboardInputs};
    use huddle_router::live::LiveState;
    let fake = answering(200, ZAI_BODY).await;
    let quota = fetcher(vec![("zai", format!("{}/q", fake.url()))]);
    let live = Arc::new(LiveState::new(0));
    let enabled: Arc<dyn Fn() -> Vec<String> + Send + Sync> = Arc::new(|| vec!["zai".to_owned()]);
    let dashboard =
        Dashboard::new(Arc::clone(&live), DashboardInputs::default(), enabled).with_quota(quota);

    let closed = state_of(&dashboard).await;
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(fake.requests().len(), 0, "no page open, no ask");
    assert_eq!(
        closed["providers"]["anthropic"]["quota"]["windows"][0]["note"],
        "not reported"
    );

    let _page = live.viewer();
    state_of(&dashboard).await;
    tokio::time::sleep(Duration::from_millis(300)).await;
    let open = state_of(&dashboard).await;
    assert_eq!(fake.requests().len(), 1);
    assert_eq!(
        open["providers"]["zai"]["quota"]["windows"][0]["used_pct"],
        63
    );
}

#[tokio::test]
async fn d5_a_base_url_override_moves_the_quota_ask() {
    use huddle_config::env::Env;
    use huddle_router::quota::catalog_urls;
    let zai = provider_by_id("zai").expect("zai");
    let kimi = provider_by_id("kimi").expect("kimi");

    // No override: the catalog URL.
    let plain = catalog_urls(&Env::with(&[]));
    assert_eq!(
        plain(zai).as_deref(),
        Some("https://api.z.ai/api/monitor/usage/quota/limit")
    );

    // An empty override is no override.
    let blank = catalog_urls(&Env::with(&[("ZAI_BASE_URL", "  ")]));
    assert_eq!(blank(zai), plain(zai));

    // An override moves the host and keeps the catalog path; other providers stay put.
    let fake = answering(200, ZAI_BODY).await;
    let base = format!("{}/anthropic", fake.url());
    let env = Env::with(&[("ZAI_BASE_URL", base.as_str())]);
    let moved = catalog_urls(&env);
    assert_eq!(
        moved(zai).as_deref(),
        Some(format!("{}/api/monitor/usage/quota/limit", fake.url()).as_str())
    );
    assert_eq!(moved(kimi), plain(kimi));

    let quota = QuotaFetcher::new(
        client(),
        Arc::new(|provider| KeySources {
            env_key: Some(key_of(provider)),
            os: None,
            file: Arc::new(|| Ok(None)),
        }),
        moved,
    );
    quota.refresh(T0, 1, &enabled(&["zai"])).await;
    assert_eq!(fake.requests().len(), 1);
    assert_eq!(fake.requests()[0].path, "/api/monitor/usage/quota/limit");
}
