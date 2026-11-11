//! `huddle provider <p> remove`: the lane out, the settings back the way the ledger found
//! them, the stored key gone. The router keeps serving the other providers.

use std::io::Write;

use huddle_catalog::providers::Provider;
use huddle_config::{env::Env, paths::Paths};
use huddle_keystore::port::platform_default;
use huddle_setup::model_entries::{self, RemoveOutcome};

/// Disables the lane, restores the settings and drops the stored key. Every failure is one
/// line and the removal carries on. Exit 0 when every step worked, 1 when one did not.
pub fn run(provider: &'static Provider, env: &Env) -> i32 {
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let mut lines = Vec::new();
    let mut failed = false;
    if let Err(error) =
        huddle_config::providers_file::set_enabled(&paths.config_dir, provider.id, false)
    {
        failed = true;
        lines.push(format!("could not update the router: {error}"));
    }
    match model_entries::remove(&paths, provider) {
        Ok(RemoveOutcome::Removed {
            lines: mut took, ..
        }) => lines.append(&mut took),
        Ok(RemoveOutcome::Nothing) => {}
        Err(error) => {
            failed = true;
            lines.push(format!("could not restore settings.json: {error}"));
        }
    }
    if let Err(error) = platform_default(&paths).delete(provider.id) {
        failed = true;
        lines.push(format!("could not remove the key: {error}"));
    }
    let _ = writeln!(std::io::stdout(), "{} removed.", provider.id);
    for line in &lines {
        let _ = writeln!(std::io::stdout(), "{line}");
    }
    if failed { 1 } else { 0 }
}
