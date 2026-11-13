use std::io::Write;
use std::sync::Arc;
use std::time::Duration;

use huddle_config::env::Env;
use huddle_config::paths::{Paths, router_port};
use huddle_mcp::{HuddleServer, Machine, pick, serve_stdio};

use super::version::VERSION;

const HEALTH_WITHIN: Duration = Duration::from_millis(300);

/// `huddle mcp`: the MCP tools over stdio. Logs go to stderr; stdout is JSON-RPC only.
pub fn run(_args: &[String], env: &Env) -> i32 {
    match serve(env) {
        Ok(()) => 0,
        Err(message) => {
            let _ = writeln!(std::io::stderr(), "huddle mcp: {message}");
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
    let server = HuddleServer::new(VERSION.trim().to_string(), sources).with_env(env.clone());
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|err| err.to_string())?;
    runtime.block_on(serve_stdio(server))
}
