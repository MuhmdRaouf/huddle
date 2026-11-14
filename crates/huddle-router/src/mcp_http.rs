//! huddle-router: MCP over HTTP.
//!
//! `/mcp` serves the same tools as `huddle mcp`, from the router's own data. The Host guard in
//! the entry handler has already refused every Host but the loopback and the dashboard hosts, and
//! a dashboard host reaches nothing but its read-only paths, so it never gets here. The service
//! then checks Host and Origin again against the exact loopback authorities of this port.

use std::sync::Arc;

use hyper::body::Bytes;

use http_body_util::combinators::BoxBody;
use http_body_util::{BodyExt, Full, Limited};
use huddle_mcp::{HttpService, HuddleServer, MCP_BODY_LIMIT, Machine, RouterSources, http_service};
use hyper::header::{ALLOW, CONTENT_TYPE, HeaderValue};
use hyper::{Method, Request, Response, StatusCode};

use crate::server::{Handler, RespBody, anthropic_error, json_answer};

/// The path the MCP service answers on.
pub const MCP_PATH: &str = "/mcp";

/// JSON-RPC: the text is not JSON.
const PARSE_ERROR: i64 = -32700;
/// JSON-RPC: the JSON is not a request this server takes.
const INVALID_REQUEST: i64 = -32600;
/// The session id names no open session.
const SESSION_NOT_FOUND: i64 = -32001;

/// `Content-Type: application/json`, a JSON-RPC error with no request id.
fn rpc_error(status: StatusCode, code: i64, message: &str) -> Response<RespBody> {
    let body = serde_json::json!({
        "jsonrpc": "2.0",
        "id": null,
        "error": {"code": code, "message": message},
    });
    json_answer(status, body.to_string())
}

/// What a POST body may be, decided before the MCP service sees it: one JSON object, within
/// the limit. A batch is refused because MCP no longer has batching.
// The err side is the whole answer; the 128-byte move only happens on a failure path.
#[allow(clippy::result_large_err)]
async fn read_post(
    req: Request<hyper::body::Incoming>,
) -> Result<Request<Full<Bytes>>, Response<RespBody>> {
    let (parts, body) = req.into_parts();
    if parts.method != Method::POST {
        return Ok(Request::from_parts(parts, Full::new(Bytes::new())));
    }
    let bytes = match Limited::new(body, MCP_BODY_LIMIT).collect().await {
        Ok(collected) => collected.to_bytes(),
        Err(error) if error.is::<http_body_util::LengthLimitError>() => {
            return Err(rpc_error(
                StatusCode::PAYLOAD_TOO_LARGE,
                INVALID_REQUEST,
                &format!("request body exceeds {MCP_BODY_LIMIT} bytes"),
            ));
        }
        Err(_) => {
            return Err(rpc_error(
                StatusCode::BAD_REQUEST,
                INVALID_REQUEST,
                "the request body could not be read",
            ));
        }
    };
    match serde_json::from_slice::<serde_json::Value>(&bytes) {
        Err(_) => Err(rpc_error(
            StatusCode::BAD_REQUEST,
            PARSE_ERROR,
            "the request body is not valid JSON",
        )),
        Ok(serde_json::Value::Array(_)) => Err(rpc_error(
            StatusCode::BAD_REQUEST,
            INVALID_REQUEST,
            "batch requests are not supported: send one message per request",
        )),
        Ok(_) => Ok(Request::from_parts(parts, Full::new(bytes))),
    }
}

/// An error the MCP service answered as plain text becomes a JSON-RPC error with the same
/// status and text. Every other answer passes through.
async fn json_errors(answer: Response<RespBody>) -> Response<RespBody> {
    let is_json = answer
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.starts_with("application/json"));
    if !answer.status().is_client_error() || is_json {
        return answer;
    }
    let status = answer.status();
    let text = match answer.into_body().collect().await {
        Ok(collected) => String::from_utf8_lossy(&collected.to_bytes()).into_owned(),
        Err(_) => String::new(),
    };
    let message = if text.trim().is_empty() {
        status.canonical_reason().unwrap_or("request refused")
    } else {
        text.trim()
    };
    let code = if status == StatusCode::NOT_FOUND {
        SESSION_NOT_FOUND
    } else {
        INVALID_REQUEST
    };
    rpc_error(status, code, message)
}

/// The MCP service for the router that runs `version` on `machine.port`: the same tools as
/// `huddle mcp`, read through this router.
pub fn service(version: &str, machine: Machine) -> HttpService {
    let port = machine.port;
    let env = machine.env.clone();
    let server = HuddleServer::new(
        version.trim().to_string(),
        Arc::new(move || {
            Box::new(RouterSources {
                machine: machine.clone(),
            })
        }),
    )
    .with_env(env);
    http_service(server, port)
}

/// `forward`, except that `/mcp` goes to `service`. A GET is refused: the tools never push a
/// message, so no standalone event stream is held open.
pub fn with_mcp(forward: Handler, service: HttpService) -> Handler {
    Arc::new(move |req| {
        if req.uri().path() != MCP_PATH {
            return forward(req);
        }
        let service = service.clone();
        Box::pin(async move {
            if req.method() == Method::GET {
                let mut refused = anthropic_error(
                    StatusCode::METHOD_NOT_ALLOWED,
                    "invalid_request_error",
                    "huddle-router: /mcp takes POST and DELETE",
                );
                refused
                    .headers_mut()
                    .insert(ALLOW, HeaderValue::from_static("POST, DELETE"));
                return refused;
            }
            let req = match read_post(req).await {
                Ok(req) => req,
                Err(refused) => return refused,
            };
            let answer = service.handle(req).await;
            let (parts, body) = answer.into_parts();
            let body: RespBody = BoxBody::new(body.map_err(|never| match never {}));
            json_errors(Response::from_parts(parts, body)).await
        })
    })
}
