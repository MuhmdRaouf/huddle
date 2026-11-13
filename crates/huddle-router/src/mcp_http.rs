//! huddle-router: MCP over HTTP.
//!
//! `/mcp` serves the same tools as `huddle mcp`, from the router's own data. The Host guard in
//! the entry handler has already refused every Host but the loopback and the dashboard hosts, and
//! a dashboard host reaches nothing but its read-only paths, so it never gets here. The service
//! then checks Host and Origin again against the exact loopback authorities of this port.

use std::sync::Arc;

use http_body_util::BodyExt;
use http_body_util::combinators::BoxBody;
use huddle_mcp::{HttpService, HuddleServer, Machine, RouterSources, http_service};
use hyper::header::{ALLOW, HeaderValue};
use hyper::{Method, Response, StatusCode};

use crate::server::{Handler, RespBody, anthropic_error};

/// The path the MCP service answers on.
pub const MCP_PATH: &str = "/mcp";

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
            let answer = service.handle(req).await;
            let (parts, body) = answer.into_parts();
            let body: RespBody = BoxBody::new(body.map_err(|never| match never {}));
            Response::from_parts(parts, body)
        })
    })
}
