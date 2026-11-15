//! huddle-router: MCP over HTTP.
//!
//! `/mcp` serves the same tools as `huddle mcp`, from the router's own data. The Host guard in
//! the entry handler has already refused every Host but the loopback and the dashboard hosts, and
//! a dashboard host reaches nothing but its read-only paths, so it never gets here. Here Host and
//! Origin are checked against the exact loopback authorities of this port, then the request must
//! carry the token from the state folder; the service checks Host and Origin once more.

use std::path::PathBuf;
use std::sync::Arc;

use http_body_util::combinators::BoxBody;
use http_body_util::{BodyExt, Full, Limited};
use huddle_config::mcp_token::McpToken;
use huddle_mcp::{
    HttpService, HuddleServer, MCP_BODY_LIMIT, Machine, RouterSources, allowed_hosts,
    allowed_origins, http_service,
};
use hyper::body::{Bytes, Incoming};
use hyper::header::{
    ALLOW, AUTHORIZATION, CONTENT_TYPE, HOST, HeaderValue, ORIGIN, WWW_AUTHENTICATE,
};
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
async fn read_post(req: Request<Incoming>) -> Result<Request<Full<Bytes>>, Response<RespBody>> {
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

/// Who may use `/mcp`: a request to this port's loopback names that carries the token in
/// `token_path`. The file is read for each request, so the router and `huddle mcp headers`
/// always agree; a file that cannot be read refuses every request.
pub struct McpGuard {
    pub port: u16,
    pub token_path: PathBuf,
}

/// The paths an MCP client probes for OAuth metadata after a 401 start here. They are answered
/// on the loopback names, so the probe never leaves the machine as a forwarded request.
const OAUTH_DISCOVERY: &str = "/.well-known/";

/// The one message for a missing or wrong token, whichever it was.
const NO_TOKEN: &str =
    "huddle-router: /mcp needs huddle's token. Run /huddle:update, then reconnect huddle in /mcp.";

/// `forward`, except that `/mcp` goes to `service` once Host, Origin and the token pass. A GET
/// is refused: the tools never push a message, so no standalone event stream is held open.
pub fn with_mcp(forward: Handler, service: HttpService, guard: McpGuard) -> Handler {
    let guard = Arc::new(guard);
    Arc::new(move |req| {
        if req.uri().path().starts_with(OAUTH_DISCOVERY) && from_loopback(&req, guard.port) {
            return Box::pin(async {
                anthropic_error(
                    StatusCode::NOT_FOUND,
                    "not_found_error",
                    "huddle-router: /mcp takes huddle's token, not OAuth",
                )
            });
        }
        if req.uri().path() != MCP_PATH {
            return forward(req);
        }
        let service = service.clone();
        let guard = Arc::clone(&guard);
        Box::pin(async move {
            if !from_loopback(&req, guard.port) {
                return rpc_error(
                    StatusCode::FORBIDDEN,
                    INVALID_REQUEST,
                    "huddle-router: /mcp answers 127.0.0.1 and localhost on this port only",
                );
            }
            let path = guard.token_path.clone();
            let token = tokio::task::spawn_blocking(move || McpToken::read(&path)).await;
            let carries_token = match (token, req.headers().get(AUTHORIZATION)) {
                (Ok(Ok(token)), Some(value)) => token.accepts(value.as_bytes()),
                _ => false,
            };
            if !carries_token {
                let mut refused = rpc_error(StatusCode::UNAUTHORIZED, INVALID_REQUEST, NO_TOKEN);
                refused
                    .headers_mut()
                    .insert(WWW_AUTHENTICATE, HeaderValue::from_static("Bearer"));
                return refused;
            }
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

/// True when Host is one of this port's loopback authorities and Origin is absent or the
/// `http` origin of one of them.
fn from_loopback(req: &Request<Incoming>, port: u16) -> bool {
    let host = req
        .headers()
        .get(HOST)
        .and_then(|value| value.to_str().ok())
        .or_else(|| req.uri().authority().map(|authority| authority.as_str()));
    let host_ok = host.is_some_and(|host| allowed_hosts(port).iter().any(|ok| ok == host));
    let origin_ok = match req.headers().get(ORIGIN) {
        None => true,
        Some(origin) => origin
            .to_str()
            .is_ok_and(|origin| allowed_origins(port).iter().any(|ok| ok == origin)),
    };
    host_ok && origin_ok
}
