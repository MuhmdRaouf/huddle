use std::sync::Arc;

use rmcp::transport::streamable_http_server::session::local::LocalSessionManager;
use rmcp::transport::{StreamableHttpServerConfig, StreamableHttpService};

use crate::server::HuddleServer;

/// The MCP streamable HTTP service: one session per client, each with its own id.
pub type HttpService = StreamableHttpService<HuddleServer, LocalSessionManager>;

/// Serves `server` over streamable HTTP for a router on `port`. A request is answered only when
/// its Host is `127.0.0.1:<port>` or `localhost:<port>` and its Origin, when present, is the
/// `http` origin of one of them.
pub fn http_service(server: HuddleServer, port: u16) -> HttpService {
    let config = StreamableHttpServerConfig::default()
        .with_allowed_hosts([format!("127.0.0.1:{port}"), format!("localhost:{port}")])
        .with_allowed_origins([
            format!("http://127.0.0.1:{port}"),
            format!("http://localhost:{port}"),
        ]);
    StreamableHttpService::new(
        move || Ok(server.clone()),
        Arc::new(LocalSessionManager::default()),
        config,
    )
}
