use std::io::Write;

use huddle_config::env::Env;

pub mod cancel;
pub mod dashboard_host;
pub mod hook;
pub mod provider;
pub mod result;
pub mod router;
pub mod run;
pub mod setup;
pub mod status;
pub mod uninstall;
pub mod version;

pub fn dispatch(args: &[String], env: &Env) -> i32 {
    let Some((word, rest)) = args.split_first() else {
        let _ = writeln!(
            std::io::stderr(),
            "usage: huddle <setup|run|status|result|cancel|add-dashboard-host|remove-dashboard-host|uninstall|router|provider|hook|version> ..."
        );
        return 2;
    };
    match word.as_str() {
        "--version" | "version" => version::run(rest, env),
        "setup" => setup::run(rest, env),
        "run" => run::run(rest, env),
        "status" => status::run(rest, env),
        "result" => result::run(rest, env),
        "cancel" => cancel::run(rest, env),
        "add-dashboard-host" => dashboard_host::add(rest, env),
        "remove-dashboard-host" => dashboard_host::remove(rest, env),
        "hook" => hook::run(rest, env),
        "uninstall" => uninstall::run(rest, env),
        "router" => router::run(rest, env),
        "provider" => provider::run(rest, env),
        other => {
            let _ = writeln!(std::io::stderr(), "huddle: unknown command {other}");
            2
        }
    }
}
