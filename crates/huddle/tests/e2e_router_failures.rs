mod e2e_support;

use e2e_support::{Daemon, raw_exchange, request};
use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::ports::free_port;

const GLM: &[u8] = br#"{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}"#;
const JSON: (&str, &str) = ("content-type", "application/json");

/// A plain upstream answering every request 200 JSON.
fn plain() -> FakeUpstream {
    e2e_support::upstream("{\"from\":\"fake\"}")
}

/// An upstream answering every request the same fixed error answer.
fn scripted(status: u16, body: &str) -> FakeUpstream {
    let body = body.to_string();
    let headers = vec![("content-type".to_string(), "application/json".to_string())];
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |_, _| {
            Step::answer_with_headers(status, headers.clone(), body.as_bytes())
        }))
}

fn daemon_with(anthropic: &FakeUpstream, zai_base: String, key: Option<&str>) -> Daemon {
    let mut extra = vec![
        ("HUDDLE_ROUTER_ANTHROPIC_URL", anthropic.url()),
        ("ZAI_BASE_URL", zai_base),
    ];
    if let Some(k) = key {
        extra.push(("ZAI_API_KEY", k.to_string()));
    }
    Daemon::start(&["zai"], &extra)
}

#[test]
fn a_bad_path_is_refused_and_goes_nowhere() {
    let anthropic = plain();
    let zai = plain();
    let daemon = daemon_with(&anthropic, zai.url(), Some("abcdefghijklmnop"));

    let reply = raw_exchange(
        daemon.port,
        &format!(
            "GET //evil HTTP/1.1\r\nHost: 127.0.0.1:{}\r\nConnection: close\r\n\r\n",
            daemon.port
        ),
    );

    assert!(reply.starts_with("HTTP/1.1 400"), "{reply}");
    assert!(
        reply.contains("huddle-router router: bad request path"),
        "{reply}"
    );
    assert!(anthropic.requests().is_empty());
    assert!(zai.requests().is_empty());
}

#[test]
fn a_wrong_host_is_refused_and_goes_nowhere() {
    let anthropic = plain();
    let zai = plain();
    let daemon = daemon_with(&anthropic, zai.url(), Some("abcdefghijklmnop"));

    let reply = raw_exchange(
        daemon.port,
        "GET /v1/models HTTP/1.1\r\nHost: evil.example\r\nConnection: close\r\n\r\n",
    );

    assert!(reply.starts_with("HTTP/1.1 421"), "{reply}");
    assert!(
        reply.contains("huddle-router router: wrong Host"),
        "{reply}"
    );
    assert!(anthropic.requests().is_empty());
}

#[test]
fn a_missing_key_is_a_non_retryable_400_naming_setup() {
    let anthropic = plain();
    let zai = plain();
    let daemon = daemon_with(&anthropic, zai.url(), None);

    let reply = request(daemon.port, "POST", "/v1/messages", &[JSON], GLM);

    assert_eq!(reply.status, 400, "{}", reply.body);
    assert_eq!(reply.header("x-should-retry"), Some("false"));
    assert!(reply.body.contains("/huddle:zai:setup"), "{}", reply.body);
    assert!(
        reply.body.contains("Claude models are not affected."),
        "{}",
        reply.body
    );
    assert!(
        zai.requests().is_empty(),
        "nothing goes upstream without a key"
    );
}

#[test]
fn a_provider_key_refusal_comes_back_as_the_refusal_answer() {
    let anthropic = plain();
    let zai = scripted(
        401,
        r#"{"error":{"type":"authentication_error","message":"bad key"}}"#,
    );
    let daemon = daemon_with(&anthropic, zai.url(), Some("abcdefghijklmnop"));

    let reply = request(daemon.port, "POST", "/v1/messages", &[JSON], GLM);

    assert_eq!(reply.status, 400, "{}", reply.body);
    assert_eq!(reply.header("x-should-retry"), Some("false"));
    assert!(
        reply.body.contains("Z.ai GLM key refused or missing"),
        "{}",
        reply.body
    );
    assert!(
        !reply.body.contains("bad key"),
        "no upstream text is copied: {}",
        reply.body
    );
}

#[test]
fn an_unreachable_provider_is_a_502() {
    let anthropic = plain();
    let closed = format!("http://127.0.0.1:{}", free_port());
    let daemon = daemon_with(&anthropic, closed, Some("abcdefghijklmnop"));

    let reply = request(daemon.port, "POST", "/v1/messages", &[JSON], GLM);

    assert_eq!(reply.status, 502, "{}", reply.body);
    assert!(reply.body.contains("unreachable"), "{}", reply.body);
}

#[test]
fn a_provider_500_passes_through_whole() {
    let anthropic = plain();
    let zai = scripted(
        500,
        r#"{"error":{"type":"api_error","message":"overloaded"}}"#,
    );
    let daemon = daemon_with(&anthropic, zai.url(), Some("abcdefghijklmnop"));

    let reply = request(daemon.port, "POST", "/v1/messages", &[JSON], GLM);

    assert_eq!(reply.status, 500, "{}", reply.body);
    assert!(reply.body.contains("overloaded"));
    assert_eq!(
        zai.requests().len(),
        1,
        "a 500 is not retried by the router here"
    );
}
