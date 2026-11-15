//! No test resolves a fixed-path harness to a file outside its temp home, so no gate starts
//! the real app installed on the test machine.

use huddle_config::env::Env;
use huddle_harness::entries::builtin;
use huddle_harness::resolve::resolve;
use huddle_testkit::temp_home::TempHome;

fn home_env(home: &TempHome) -> Env {
    let vars = home.env();
    let pairs: Vec<(&str, &str)> = vars.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    Env::with(&pairs)
}

#[test]
fn l43_no_fixed_path_is_reached_from_a_temp_home() {
    let home = TempHome::new();
    let root = home.root().to_path_buf();
    let env = home_env(&home);
    for entry in builtin() {
        if entry.binary.fixed.is_empty() {
            continue;
        }
        if let Ok(found) = resolve(&entry, &env) {
            assert!(
                found.path.starts_with(&root),
                "{} resolved to {} outside the temp home",
                entry.id,
                found.path.display()
            );
        }
    }
}

#[test]
fn l43_a_fixed_path_entry_names_its_own_variable() {
    let home = TempHome::new();
    let vars = home.env();
    for entry in builtin() {
        if entry.binary.fixed.is_empty() {
            continue;
        }
        let Some(var) = entry.binary.env_var.as_deref() else {
            panic!("{} has fixed paths and no override variable", entry.id);
        };
        assert!(
            vars.iter().any(|(name, _)| name == var),
            "{var} is missing from TempHome::env(), so a test reaches the fixed path"
        );
    }
}
