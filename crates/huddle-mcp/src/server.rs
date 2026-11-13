use std::sync::Arc;

use huddle_config::env::Env;
use rmcp::handler::server::router::tool::ToolRouter;
use rmcp::handler::server::wrapper::Parameters;
use rmcp::model::{Implementation, ServerCapabilities, ServerConfig};
use rmcp::transport::stdio;
use rmcp::{ErrorData, Json, ServerHandler, ServiceExt, tool, tool_handler, tool_router};

use crate::sources::Sources;
use crate::tools::delegation::{
    self, CancelInput, CancelOutput, Context, ResultInput, ResultOutput, RunInput, RunOutput,
    StatusInput, StatusOutput,
};
use crate::tools::version::{self, VersionInfo};

/// Builds the sources for one tool call, so each call sees the router as it is then.
pub type SourcesFactory = Arc<dyn Fn() -> Box<dyn Sources> + Send + Sync>;

/// The tool registry. Transports hand it a connection and add no tool logic.
#[derive(Clone)]
pub struct HuddleServer {
    version: String,
    sources: SourcesFactory,
    env: Env,
    tool_router: ToolRouter<Self>,
}

impl HuddleServer {
    pub fn new(version: String, sources: SourcesFactory) -> HuddleServer {
        HuddleServer {
            version,
            sources,
            env: Env::with(&[]),
            tool_router: Self::tool_router(),
        }
    }

    /// The environment the delegation tools read their folders and harness settings from.
    pub fn with_env(mut self, env: Env) -> HuddleServer {
        self.env = env;
        self
    }

    /// Runs one blocking delegation tool off the async task, with this server's environment and
    /// binary.
    async fn blocking<T, F>(&self, work: F) -> Result<Json<T>, String>
    where
        T: Send + 'static,
        F: FnOnce(&Context) -> Result<T, String> + Send + 'static,
    {
        let factory = Arc::clone(&self.sources);
        let env = self.env.clone();
        tokio::task::spawn_blocking(move || {
            let ctx = Context {
                exe: factory().binary_path(),
                env,
            };
            work(&ctx)
        })
        .await
        .map_err(|err| err.to_string())?
        .map(Json)
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

    /// Starts a task on a coding harness in a folder and, unless background is set, waits for
    /// its report. The harness edits files in that folder, so this changes things.
    #[tool(name = "huddle_run")]
    async fn huddle_run(
        &self,
        Parameters(input): Parameters<RunInput>,
    ) -> Result<Json<RunOutput>, String> {
        self.blocking(move |ctx| delegation::run(ctx, input)).await
    }

    /// Lists the delegated jobs of this repository, or the live jobs of every repository, with
    /// their state and why they are stuck or failed. Reads only.
    #[tool(name = "huddle_status")]
    async fn huddle_status(
        &self,
        Parameters(input): Parameters<StatusInput>,
    ) -> Result<Json<StatusOutput>, String> {
        self.blocking(move |ctx| delegation::status(ctx, input))
            .await
    }

    /// Reads a job's report, with its diff on request, or decides it: accept commits the change
    /// on the current branch, accept_no_commit leaves it uncommitted, return sends feedback to
    /// the harness. Accepting and returning change things; reading does not.
    #[tool(name = "huddle_result")]
    async fn huddle_result(
        &self,
        Parameters(input): Parameters<ResultInput>,
    ) -> Result<Json<ResultOutput>, String> {
        self.blocking(move |ctx| delegation::result(ctx, input))
            .await
    }

    /// Stops a job and marks it cancelled. Its folder stays as it is. This stops a running
    /// harness, so it changes things.
    #[tool(name = "huddle_cancel")]
    async fn huddle_cancel(
        &self,
        Parameters(input): Parameters<CancelInput>,
    ) -> Result<Json<CancelOutput>, String> {
        self.blocking(move |ctx| delegation::cancel(ctx, input))
            .await
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
