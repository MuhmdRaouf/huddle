use std::sync::Arc;

use rmcp::handler::server::router::tool::ToolRouter;
use rmcp::model::{Implementation, ServerCapabilities, ServerConfig};
use rmcp::transport::stdio;
use rmcp::{ErrorData, Json, ServerHandler, ServiceExt, tool, tool_handler, tool_router};

use crate::sources::Sources;
use crate::tools::version::{self, VersionInfo};

/// Builds the sources for one tool call, so each call sees the router as it is then.
pub type SourcesFactory = Arc<dyn Fn() -> Box<dyn Sources> + Send + Sync>;

/// The tool registry. Transports hand it a connection and add no tool logic.
#[derive(Clone)]
pub struct HuddleServer {
    version: String,
    sources: SourcesFactory,
    tool_router: ToolRouter<Self>,
}

impl HuddleServer {
    pub fn new(version: String, sources: SourcesFactory) -> HuddleServer {
        HuddleServer {
            version,
            sources,
            tool_router: Self::tool_router(),
        }
    }
}

#[tool_router]
impl HuddleServer {
    /// The huddle version, whether the router is up, and the binary's path.
    #[tool(name = "huddle_version")]
    async fn huddle_version(&self) -> Result<Json<VersionInfo>, ErrorData> {
        let factory = Arc::clone(&self.sources);
        let sources = tokio::task::spawn_blocking(move || factory())
            .await
            .map_err(|err| ErrorData::internal_error(err.to_string(), None))?;
        Ok(Json(version::version(&self.version, sources.as_ref())))
    }
}

#[tool_handler(router = self.tool_router)]
impl ServerHandler for HuddleServer {
    fn get_info(&self) -> ServerConfig {
        ServerConfig::new(ServerCapabilities::builder().enable_tools().build())
            .with_server_info(Implementation::new("huddle", self.version.clone()))
    }
}

/// Serves `server` as JSON-RPC over stdin and stdout until the client closes stdin.
pub async fn serve_stdio(server: HuddleServer) -> Result<(), String> {
    let running = server.serve(stdio()).await.map_err(|err| err.to_string())?;
    running.waiting().await.map_err(|err| err.to_string())?;
    Ok(())
}
