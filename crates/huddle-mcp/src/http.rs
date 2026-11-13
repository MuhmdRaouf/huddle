use std::sync::Arc;

use rmcp::transport::streamable_http_server::session::local::LocalSessionManager;
use rmcp::transport::{StreamableHttpServerConfig, StreamableHttpService};

use crate::server::HuddleServer;

/// The MCP streamable HTTP service: one session per client, each with its own id.
pub type HttpService = StreamableHttpService<HuddleServer, LocalSessionManager>;

/// The largest POST body `/mcp` takes: 4 MiB, far above any tool call.
pub const MCP_BODY_LIMIT: usize = 4 * 1024 * 1024;

/// Serves `server` over streamable HTTP for a router on `port`. A request is answered only when
/// its Host is `127.0.0.1:<port>` or `localhost:<port>` and its Origin, when present, is the
/// `http` origin of one of them.
pub fn http_service(server: HuddleServer, port: u16) -> HttpService {
    let config = StreamableHttpServerConfig::default()
        .with_max_request_body_bytes(MCP_BODY_LIMIT)
        .with_allowed_hosts(allowed_hosts(port))
        .with_allowed_origins(allowed_origins(port));
    StreamableHttpService::new(
        move || Ok(server.clone()),
        Arc::new(LocalSessionManager::default()),
        config,
    )
}

/// The Host values the service answers on `port`.
pub fn allowed_hosts(port: u16) -> [String; 2] {
    [format!("127.0.0.1:{port}"), format!("localhost:{port}")]
}

/// The Origin values the service answers on `port`; a request may also carry none.
pub fn allowed_origins(port: u16) -> [String; 2] {
    [
        format!("http://127.0.0.1:{port}"),
        format!("http://localhost:{port}"),
    ]
}
