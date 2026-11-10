use std::io::Write;

use huddle_config::env::Env;

mod words;

fn main() {
    let args: Result<Vec<String>, _> = std::env::args_os()
        .skip(1)
        .map(|arg| arg.into_string())
        .collect();
    let code = match args {
        Ok(args) => words::dispatch(&args, &Env::from_process()),
        Err(bad) => {
            let _ = writeln!(
                std::io::stderr(),
                "huddle: argument is not UTF-8: {}",
                bad.to_string_lossy()
            );
            2
        }
    };
    std::process::exit(code);
}
