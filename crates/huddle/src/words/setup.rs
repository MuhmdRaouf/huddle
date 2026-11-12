//! The setup word: every harness as a table, or one harness's full readiness report.

use std::io::Write;

use super::run::parse_args;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::check::{
    SmokeRunner, TurnSmoke, check_all, check_entry, render_all_json, render_all_markdown,
    render_json, render_markdown,
};
use huddle_harness::user_entries::all_entries;
use huddle_setup::{install, old_plugins};

const USAGE: &str = "usage: huddle setup [<harness>] [--smoke] [--json]";

pub fn run(args: &[String], env: &Env) -> i32 {
    if args.first().map(String::as_str) == Some("--install") {
        return install_word(env);
    }
    let parsed = match parse_args(args, &["smoke", "json", "help"], &[]) {
        Ok(parsed) => parsed,
        Err(message) => {
            let _ = writeln!(std::io::stderr(), "huddle: {message}\n{USAGE}");
            return 2;
        }
    };
    if parsed.has("help") {
        let _ = writeln!(std::io::stdout(), "{USAGE}");
        return 0;
    }
    if parsed.positionals.len() > 1 {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: setup takes at most one harness id\n{USAGE}"
        );
        return 2;
    }
    if parsed.has("smoke") && parsed.positionals.is_empty() {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: --smoke needs a harness id\n{USAGE}"
        );
        return 2;
    }
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let entries = match all_entries(&paths.config_dir.join("harnesses.toml")) {
        Ok(entries) => entries,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let Some(id) = parsed.positionals.first().map(String::as_str) else {
        let reports = check_all(&entries, env);
        let text = if parsed.has("json") {
            render_all_json(&reports)
        } else {
            render_all_markdown(&reports)
        };
        let _ = writeln!(std::io::stdout(), "{text}");
        if !parsed.has("json") {
            print_old_plugin_hint(&paths);
        }
        return 0;
    };
    let Some(entry) = entries.iter().find(|entry| entry.id == id) else {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: no harness named \"{id}\": /huddle:setup lists them"
        );
        return 2;
    };
    let runner;
    let smoke: Option<&dyn SmokeRunner> = if parsed.has("smoke") {
        runner = TurnSmoke {
            env,
            dir: paths.state_dir.join("smoke"),
        };
        Some(&runner)
    } else {
        None
    };
    let report = check_entry(entry, env, smoke);
    let text = if parsed.has("json") {
        render_json(&report)
    } else {
        render_markdown(&report)
    };
    let _ = writeln!(std::io::stdout(), "{text}");
    if !parsed.has("json") {
        print_old_plugin_hint(&paths);
    }
    if report.ready { 0 } else { 6 }
}

fn print_old_plugin_hint(paths: &Paths) {
    for line in old_plugins::hint_lines(&paths.claude_dir) {
        let _ = writeln!(std::io::stdout(), "{line}");
    }
}

/// The `--install` word: CA, router, settings patch, one restart line.
fn install_word(env: &Env) -> i32 {
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
                "huddle setup: cannot find the running binary"
            );
            return 1;
        }
    };
    match install::run(env, &paths, &exe) {
        Ok(report) => {
            for line in &report.lines {
                let _ = writeln!(std::io::stdout(), "{line}");
            }
            if report.restart_needed {
                let _ = writeln!(
                    std::io::stdout(),
                    "Restart Claude Code once so it picks up the proxy settings."
                );
            }
            print_old_plugin_hint(&paths);
            0
        }
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            1
        }
    }
}
