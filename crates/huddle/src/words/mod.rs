use huddle_config::env::Env;

pub mod cancel;
pub mod dashboard_host;
pub mod home;
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
        return home::run_home(env);
    };
    match word.as_str() {
        "help" | "-h" | "--help" => home::run_help(),
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
        _ => home::run_unknown_word(),
    }
}
