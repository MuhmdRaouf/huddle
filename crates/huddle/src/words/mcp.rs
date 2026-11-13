use std::io::Write;
use std::sync::Arc;
use std::time::Duration;

use huddle_config::env::Env;
use huddle_config::mcp_token::{McpToken, token_path};
use huddle_config::paths::{Paths, router_port};
use huddle_mcp::{HuddleServer, Machine, pick, serve_stdio};

use super::version::VERSION;

const HEALTH_WITHIN: Duration = Duration::from_millis(300);

/// `huddle mcp`: the MCP tools over stdio. Logs go to stderr; stdout is JSON-RPC only.
/// `huddle mcp headers`: the plugin's `headersHelper` for `/mcp` over HTTP.
pub fn run(args: &[String], env: &Env) -> i32 {
    if args.first().map(String::as_str) == Some("headers") {
        return headers(env);
    }
    match serve(env) {
        Ok(()) => 0,
        Err(message) => {
            let _ = writeln!(std::io::stderr(), "huddle mcp: {message}");
            1
        }
    }
}

/// Prints `{"Authorization":"Bearer <token>"}` for Claude Code to send on every request to the
/// router's `/mcp`. Reads the token only: the router makes it, so the two never disagree.
fn headers(env: &Env) -> i32 {
    let token = Paths::from_env(env)
        .map_err(|err| err.to_string())
        .and_then(|paths| McpToken::read(&token_path(&paths)).map_err(|err| err.to_string()));
    match token {
        Ok(token) => {
            let line = serde_json::json!({ "Authorization": token.authorization() });
            let _ = writeln!(std::io::stdout(), "{line}");
            0
        }
        Err(message) => {
            let _ = writeln!(
                std::io::stderr(),
                "huddle mcp headers: MCP token {message}. Run /huddle:setup."
            );
            1
        }
    }
}

fn serve(env: &Env) -> Result<(), String> {
    let port = router_port(env).map_err(|err| err.to_string())?;
    let binary = std::env::current_exe().map_err(|err| err.to_string())?;
    let machine = Machine {
        binary,
        paths: Paths::from_env(env).map_err(|err| err.to_string())?,
        env: env.clone(),
        port,
    };
    let sources = Arc::new(move || {
        let up = huddle_daemon::start::health(port, HEALTH_WITHIN).is_some();
        pick(up, machine.clone())
    });
    let server = HuddleServer::new(VERSION.trim().to_string(), sources)
        .with_env(env.clone())
        .with_folder(std::env::current_dir().ok());
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|err| err.to_string())?;
    runtime.block_on(serve_stdio(server))
}
