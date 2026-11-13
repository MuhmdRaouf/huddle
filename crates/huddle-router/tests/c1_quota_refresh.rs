//! C1: a quota tool call asks the router to refresh. The state answer with `?quota=refresh`
//! counts as a viewer for that one ask; the five minute limit per provider still holds.

use std::sync::Arc;
use std::time::Duration;

use huddle_keystore::port::SecretString;
use huddle_router::dashboard::{Dashboard, DashboardInputs};
use huddle_router::keycache::{KeyRead, KeySources};
use huddle_router::live::LiveState;
use huddle_router::quota::{QuotaFetcher, UrlFor};
use huddle_router::server::{
    EntryCtx, Handler, ServerLimits, anthropic_error, bind, entry_handler, serve,
};
use huddle_router::upstream::{UpstreamClient, UpstreamConfig};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use hyper::body::Incoming;
use hyper::{Request, StatusCode};
use serde_json::Value;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::watch;

const ZAI_BODY: &str = r#"{"code":200,"success":true,"msg":"ok","data":{"level":"pro","limits":[
 {"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":63,"nextResetTime":1794018000000}]}}"#;

async fn router(quota_url: String) -> u16 {
    let client = Arc::new(UpstreamClient::new(UpstreamConfig {
        env: huddle_config::env::Env::with(&[]),
        roots: rustls::RootCertStore::empty(),
        idle: Duration::from_secs(1),
    }));
    let url_for: UrlFor = Arc::new(move |_| Some(quota_url.clone()));
    let fetcher = QuotaFetcher::new(
        client,
        Arc::new(|_| {
            let file: KeyRead = Arc::new(|| Ok(None));
            KeySources {
                env_key: Some(SecretString::new("secret-zai".to_owned())),
                os: None,
                file,
            }
        }),
        url_for,
    );
    let listener = bind(0).await.expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let providers: Arc<dyn Fn() -> Vec<String> + Send + Sync> =
        Arc::new(|| vec!["zai".to_string()]);
    let dashboard = Dashboard::new(
        Arc::new(LiveState::new(0)),
        DashboardInputs::default(),
        providers.clone(),
    )
    .with_quota(fetcher);
    let refuse: Handler = Arc::new(|_req: Request<Incoming>| {
        Box::pin(async move { anthropic_error(StatusCode::IM_A_TEAPOT, "x", "forwarded") })
    });
    let handler = entry_handler(Arc::new(EntryCtx {
        port: Some(port),
        connect: refuse.clone(),
        forward: refuse,
        providers,
        dashboard: Some(Arc::new(dashboard)),
        dashboard_hosts: Arc::new(Vec::new),
    }));
    let (_tx, rx) = watch::channel(false);
    tokio::spawn(async move {
        let _keep = _tx;
        serve(listener, handler, ServerLimits::default(), rx)
            .await
            .expect("serve")
    });
    port
}

async fn state(port: u16, query: &str) -> Value {
    let mut s = TcpStream::connect(("127.0.0.1", port))
        .await
        .expect("connect");
    let req = format!(
        "GET /huddle-router/state{query} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n"
    );
    s.write_all(req.as_bytes()).await.expect("write");
    let mut out = Vec::new();
    let _ = tokio::time::timeout(Duration::from_secs(5), s.read_to_end(&mut out)).await;
    let text = String::from_utf8_lossy(&out).into_owned();
    let body = text.split("\r\n\r\n").nth(1).unwrap_or("");
    serde_json::from_str(body).unwrap_or_else(|e| panic!("{e}: {text}"))
}

#[tokio::test]
async fn c1_a_state_ask_with_quota_refresh_fetches_without_a_dashboard_open() {
    let fake = FakeUpstream::start(move |_, _| Step::answer(200, ZAI_BODY)).await;
    let port = router(format!("{}/quota", fake.url())).await;

    let plain = state(port, "").await;
    assert_eq!(fake.requests().len(), 0, "no viewer, no ask");
    assert!(plain["providers"]["zai"]["quota"]["windows"].is_null());

    let fresh = state(port, "?quota=refresh").await;
    let windows = fresh["providers"]["zai"]["quota"]["windows"]
        .as_array()
        .expect("windows");
    assert_eq!(windows.len(), 1, "{fresh}");
    assert_eq!(windows[0]["used_pct"], 63);
    assert_eq!(fake.requests().len(), 1);

    let again = state(port, "?quota=refresh").await;
    assert_eq!(fake.requests().len(), 1, "five minute limit per provider");
    assert_eq!(
        again["providers"]["zai"]["quota"]["windows"][0]["used_pct"],
        63
    );
}
