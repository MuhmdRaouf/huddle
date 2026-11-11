//! The `huddle add-dashboard-host` and `huddle remove-dashboard-host` words.

use std::io::Write;

use huddle_config::dashboard_hosts::{self, Change, DashboardHostsError};
use huddle_config::env::Env;
use huddle_config::paths::Paths;

/// One word's fixed text: its usage line and what it says when the host was already in the
/// wanted state.
struct Word {
    usage: &'static str,
    unchanged: &'static str,
    edit: Edit,
}

const ADD: Word = Word {
    usage: "usage: huddle add-dashboard-host <host>",
    unchanged: "is already a dashboard host",
    edit: dashboard_hosts::add,
};
const REMOVE: Word = Word {
    usage: "usage: huddle remove-dashboard-host <host>",
    unchanged: "is not a dashboard host",
    edit: dashboard_hosts::remove,
};

pub fn add(args: &[String], env: &Env) -> i32 {
    change(args, env, &ADD)
}

pub fn remove(args: &[String], env: &Env) -> i32 {
    change(args, env, &REMOVE)
}

type Edit = fn(&std::path::Path, &str) -> Result<Change, DashboardHostsError>;

fn change(args: &[String], env: &Env, word: &Word) -> i32 {
    let [host] = args else {
        let _ = writeln!(std::io::stderr(), "{}", word.usage);
        return 2;
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    match (word.edit)(&dashboard_hosts::config_path(&paths), host) {
        Ok(change) => {
            let _ = writeln!(std::io::stdout(), "{}", describe(&change, word));
            0
        }
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            match error {
                DashboardHostsError::BadHost { .. } => 2,
                _ => 1,
            }
        }
    }
}

fn describe(change: &Change, word: &Word) -> String {
    match change {
        Change::Added(host) => format!("huddle: {host} added to the dashboard hosts"),
        Change::Removed(host) => format!("huddle: {host} removed from the dashboard hosts"),
        Change::Unchanged(host) => format!("huddle: {host} {}", word.unchanged),
    }
}
