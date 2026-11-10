//! `huddle-test-guard [cargo test args]`: runs `cargo test` under the suite guard.

use huddle_testkit::guard::run_guarded;

fn main() {
    let mut command = vec!["cargo".to_string(), "test".to_string()];
    command.extend(
        std::env::args_os()
            .skip(1)
            .map(|arg| arg.to_string_lossy().into_owned()),
    );
    let code = run_guarded(&std::env::temp_dir(), &command, &mut std::io::stderr());
    std::process::exit(code);
}
