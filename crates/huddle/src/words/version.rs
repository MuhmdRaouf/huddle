use std::io::Write;

use huddle_config::env::Env;

pub const VERSION: &str = include_str!("../../../../VERSION");

pub fn run(_args: &[String], _env: &Env) -> i32 {
    let _ = writeln!(std::io::stdout(), "huddle {}", VERSION.trim());
    0
}
