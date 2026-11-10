use std::io::Write;

use huddle_config::env::Env;

const VERSION_FILE: &str = include_str!("../../../../VERSION");

pub fn run(_args: &[String], _env: &Env) -> i32 {
    let _ = writeln!(std::io::stdout(), "huddle {}", VERSION_FILE.trim());
    0
}
