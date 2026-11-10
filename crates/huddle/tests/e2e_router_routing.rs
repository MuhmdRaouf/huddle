mod e2e_support;

use e2e_support::{Daemon, request};
use huddle_testkit::fake_upstream::{FakeUpstream, Recorded};

const CLAUDE_BODY: &[u8] =
    br#"{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}"#;
const GLM_BODY: &[u8] = br#"{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}"#;
const JSON: (&str, &str) = ("content-type", "application/json");

fn fixture() -> (FakeUpstream, FakeUpstream, Daemon) {
    let anthropic = e2e_support::upstream("{\"from\":\"anthropic\"}");
    let zai = e2e_support::upstream("{\"from\":\"zai\"}");
    let daemon = Daemon::start(
        &["zai"],
        &[
            ("HUDDLE_ROUTER_ANTHROPIC_URL", anthropic.url()),
            ("ZAI_BASE_URL", zai.url()),
            ("ZAI_API_KEY", "abcdefghijklmnop".to_string()),
        ],
    );
    (anthropic, zai, daemon)
}

fn header<'a>(req: &'a Recorded, name: &str) -> Option<&'a str> {
    req.headers
        .iter()
        .find(|(k, _)| k.eq_ignore_ascii_case(name))
        .map(|(_, v)| v.as_str())
}

#[test]
fn health_names_the_router_and_its_providers() {
    let (_a, _z, daemon) = fixture();
    let reply = request(daemon.port, "GET", "/huddle-router/health", &[], b"");
    assert_eq!(reply.status, 200);
    assert_eq!(
        reply.body,
        "{\"ok\":true,\"name\":\"huddle-router\",\"providers\":[\"zai\"],\"components\":[]}"
    );
    assert_eq!(reply.header("content-type"), Some("application/json"));
}

#[test]
fn a_claude_model_reaches_anthropic_untouched() {
    let (anthropic, zai, daemon) = fixture();
    let reply = request(
        daemon.port,
        "POST",
        "/v1/messages",
        &[JSON, ("x-api-key", "client-key")],
        CLAUDE_BODY,
    );
    assert_eq!(reply.status, 200, "{}", reply.body);
    assert!(reply.body.contains("anthropic"));
    let got = anthropic.requests();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].path, "/v1/messages");
    assert_eq!(
        got[0].body,
        CLAUDE_BODY.to_vec(),
        "the body arrives byte for byte"
    );
    assert_eq!(header(&got[0], "x-api-key"), Some("client-key"));
    assert!(zai.requests().is_empty());
}

#[test]
fn a_glm_model_reaches_the_zai_lane_with_its_key() {
    let (anthropic, zai, daemon) = fixture();
    let reply = request(
        daemon.port,
        "POST",
        "/v1/messages?beta=true",
        &[JSON, ("x-api-key", "client-key")],
        GLM_BODY,
    );
    assert_eq!(reply.status, 200, "{}", reply.body);
    assert!(reply.body.contains("zai"));
    let got = zai.requests();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].path, "/v1/messages?beta=true");
    assert_eq!(
        header(&got[0], "authorization"),
        Some("Bearer abcdefghijklmnop")
    );
    assert_eq!(
        header(&got[0], "x-api-key"),
        None,
        "the client's Anthropic key does not go to the provider"
    );
    assert!(anthropic.requests().is_empty());
}

#[test]
fn an_unknown_model_goes_to_anthropic() {
    let (anthropic, zai, daemon) = fixture();
    let body = br#"{"model":"totally-unknown-1","messages":[]}"#;
    let reply = request(daemon.port, "POST", "/v1/messages", &[JSON], body);
    assert_eq!(reply.status, 200, "{}", reply.body);
    assert_eq!(anthropic.requests().len(), 1);
    assert!(zai.requests().is_empty());
}

#[test]
fn any_other_path_goes_to_anthropic() {
    let (anthropic, zai, daemon) = fixture();
    let reply = request(daemon.port, "GET", "/v1/models", &[], b"");
    assert_eq!(reply.status, 200, "{}", reply.body);
    let got = anthropic.requests();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].path, "/v1/models");
    assert!(zai.requests().is_empty());
}
