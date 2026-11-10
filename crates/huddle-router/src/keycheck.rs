//! huddle-router: keycheck.
//!
//! Proves a stored provider key with one small request: one `POST /v1/messages`, one output
//! token, no redirects, a 20 second ceiling, the key only in the `authorization` header. The
//! status alone decides the verdict; the answer body is read and dropped, and no error text is
//! ever kept.

use std::time::Duration;

use http_body_util::BodyExt;
use huddle_catalog::providers::Provider;
use hyper::body::Bytes;
use hyper::header::{HeaderMap, HeaderValue};
use hyper::{Method, StatusCode, Uri};

use crate::upstream::{
    FailureCode, RequestBody, UpstreamAnswer, UpstreamClient, UpstreamRequest, upstream_url,
};

/// The one request's ceiling.
const CHECK_TIMEOUT: Duration = Duration::from_secs(20);

/// What the provider's answer said about the key.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Verdict {
    /// The provider answered 2xx: the key works.
    Accepted,
    /// 402 or 429: the key is good but the provider takes no requests yet (quota or balance).
    Limited,
    /// 401 or 403: the provider rejected the key.
    Refused,
    /// A network failure, a timeout, an unusable base URL, or any other status.
    Unknown,
}

/// One request, 20 s, no redirect. A network failure, timeout or unusable base URL is Unknown.
pub async fn check_key(base_url: &str, key: &str, model: &str, client: &UpstreamClient) -> Verdict {
    let request = match build_request(base_url, key, model) {
        Ok(request) => request,
        Err(_) => return Verdict::Unknown,
    };
    let answer = match tokio::time::timeout(CHECK_TIMEOUT, client.send(request)).await {
        Ok(Ok(answer)) => answer,
        // A timeout and a network failure look the same from here, and neither keeps text.
        _ => return Verdict::Unknown,
    };
    let verdict = verdict_of(answer.status);
    drain(client.idle(), answer).await;
    verdict
}

/// The model the check asks for: the provider's flash model, except minimax which uses main.
pub fn ping_model(provider: &Provider) -> &'static str {
    if provider.id == "minimax" {
        provider.main.id
    } else {
        provider.flash.id
    }
}

/// The one request: trailing `/` collapsed off the base, `/v1/messages`, the key in
/// `authorization` alone. Any part that cannot ride the wire is `EUpstream`, read as Unknown.
fn build_request(base_url: &str, key: &str, model: &str) -> Result<UpstreamRequest, FailureCode> {
    let target =
        Uri::try_from(base_url.trim_end_matches('/')).map_err(|_| FailureCode::EUpstream)?;
    let headers = request_headers(key)?;
    Ok(UpstreamRequest {
        url: upstream_url(&target, "/v1/messages")?,
        method: Method::POST,
        headers,
        body: RequestBody::Whole(Bytes::from(format!(
            r#"{{"model":"{model}","max_tokens":1,"messages":[{{"role":"user","content":"ping"}}]}}"#
        ))),
    })
}

/// The three headers the check sends. A value that is not a legal header byte string is
/// `EUpstream`, never a panic and never a leaked key.
fn request_headers(key: &str) -> Result<HeaderMap, FailureCode> {
    let mut headers = HeaderMap::new();
    for (name, value) in [
        ("authorization", format!("Bearer {key}")),
        ("anthropic-version", "2023-06-01".to_string()),
        ("content-type", "application/json".to_string()),
    ] {
        let value = HeaderValue::from_str(&value).map_err(|_| FailureCode::EUpstream)?;
        headers.insert(name, value);
    }
    Ok(headers)
}

/// The status alone decides; the body is never read for content.
fn verdict_of(status: StatusCode) -> Verdict {
    match status.as_u16() {
        200..=299 => Verdict::Accepted,
        401 | 403 => Verdict::Refused,
        402 | 429 => Verdict::Limited,
        _ => Verdict::Unknown,
    }
}

/// Reads the answer body to its end so the connection returns to the pool, dropping every byte.
/// A body that stalls past the client's idle budget or breaks only loses the connection; the
/// status already decided the verdict.
async fn drain(idle: Duration, answer: UpstreamAnswer) {
    let UpstreamAnswer { mut body, conn, .. } = answer;
    while let Ok(Some(Ok(_))) = tokio::time::timeout(idle, body.frame()).await {}
    conn.finish();
}
