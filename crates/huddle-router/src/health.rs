//! huddle-router: health.

use hyper::{Response, StatusCode};
use serde::Serialize;

use crate::server::{ROUTER_NAME, RespBody, json_answer};

pub const HEALTH_PATH: &str = "/huddle-router/health";

#[derive(Serialize)]
struct HealthBody<'a> {
    ok: bool,
    name: &'a str,
    providers: &'a [String],
    components: Vec<serde_json::Value>,
}

pub fn health_answer(providers: &[String]) -> Response<RespBody> {
    let body = HealthBody {
        ok: true,
        name: ROUTER_NAME,
        providers,
        components: vec![],
    };
    // Serializing this fixed shape cannot fail; an empty body keeps the answer well-formed.
    let json = serde_json::to_string(&body).unwrap_or_default();
    json_answer(StatusCode::OK, json)
}
