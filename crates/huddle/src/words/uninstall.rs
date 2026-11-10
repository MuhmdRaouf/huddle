//! The `huddle uninstall` word: undoes every patch huddle made, one line per step.

use std::io::Write;

use huddle_config::env::Env;
use huddle_config::paths::Paths;

pub fn run(_args: &[String], env: &Env) -> i32 {
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let exe = match std::env::current_exe() {
        Ok(exe) => exe,
        Err(_) => {
            let _ = writeln!(
                std::io::stderr(),
                "huddle uninstall: cannot find the running binary"
            );
            return 1;
        }
    };
    let report = huddle_setup::uninstall::run(&paths, &exe);
    for line in &report.lines {
        let _ = writeln!(std::io::stdout(), "{line}");
    }
    0
}
