use huddle_router::keycheck::{Verdict, check_key};
use huddle_router::upstream::{UpstreamClient, UpstreamConfig};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use std::time::Duration;

fn client() -> UpstreamClient {
    UpstreamClient::new(UpstreamConfig {
        env: huddle_config::env::Env::with(&[]),
        roots: rustls::RootCertStore::empty(),
        idle: Duration::from_secs(1),
    })
}

async fn verdict_for(status: u16) -> Verdict {
    let fake = FakeUpstream::start(move |_, _| Step::answer(status, "{}")).await;
    check_key(&fake.url(), "the-key", "glm-5.3-flash", &client()).await
}

#[tokio::test]
async fn statuses_map_to_verdicts() {
    assert_eq!(verdict_for(200).await, Verdict::Accepted);
    assert_eq!(verdict_for(401).await, Verdict::Refused);
    assert_eq!(verdict_for(403).await, Verdict::Refused);
    assert_eq!(verdict_for(429).await, Verdict::Limited);
    assert_eq!(verdict_for(402).await, Verdict::Limited);
    assert_eq!(verdict_for(500).await, Verdict::Unknown);
}

#[tokio::test]
async fn one_post_with_the_key_only_in_the_authorization_header() {
    let fake = FakeUpstream::start(|_, _| Step::answer(200, "{}")).await;
    let base = format!("{}///", fake.url());
    check_key(&base, "the-key", "glm-5.3-flash", &client()).await;
    let requests = fake.requests();
    assert_eq!(requests.len(), 1);
    let request = &requests[0];
    assert_eq!(request.method, "POST");
    assert_eq!(request.path, "/v1/messages");
    assert_eq!(request.body, br#"{"model":"glm-5.3-flash","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}"#);
    let auth: Vec<_> = request
        .headers
        .iter()
        .filter(|(k, _)| k.eq_ignore_ascii_case("authorization"))
        .collect();
    assert_eq!(auth.len(), 1);
    assert_eq!(auth[0].1, "Bearer the-key");
    assert!(!String::from_utf8_lossy(&request.body).contains("the-key"));
}

#[tokio::test]
async fn a_bad_url_is_unknown_not_a_panic() {
    assert_eq!(
        check_key("not a url", "k", "m", &client()).await,
        Verdict::Unknown
    );
}
