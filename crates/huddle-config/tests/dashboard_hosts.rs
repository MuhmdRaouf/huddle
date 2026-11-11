use std::fs;

use huddle_config::dashboard_hosts::{
    Change, add, config_path, normalize, read, remove, write_default_if_missing,
};
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_testkit::temp_home::TempHome;

const MANAGED: &str =
    "# Managed by huddle: huddle add-dashboard-host / remove-dashboard-host rewrite this file.\n";

#[test]
fn normalize_lowercases() {
    assert_eq!(normalize("HUB.raouf.me").as_deref(), Ok("hub.raouf.me"));
}

#[test]
fn normalize_refuses_what_is_not_a_bare_host() {
    for raw in ["https://x", "x/y", "x:443", "*.x", "", "a b", "a_b", "x\n"] {
        assert!(normalize(raw).is_err(), "{raw:?} must be refused");
    }
}

#[test]
fn a_missing_file_reads_empty() {
    let home = TempHome::new();
    assert_eq!(read(&home.root().join("none.toml")), Ok(vec![]));
}

#[test]
fn add_writes_the_exact_file_and_a_repeat_changes_nothing() {
    let home = TempHome::new();
    let path = home.root().join("cfg").join("config.toml");
    assert_eq!(
        add(&path, "HUB.raouf.me"),
        Ok(Change::Added("hub.raouf.me".into()))
    );
    let expected = format!("{MANAGED}[dashboard]\nhosts = [\"hub.raouf.me\"]\n");
    assert_eq!(fs::read_to_string(&path).expect("file"), expected);
    assert_eq!(
        add(&path, "hub.raouf.me"),
        Ok(Change::Unchanged("hub.raouf.me".into()))
    );
    assert_eq!(fs::read_to_string(&path).expect("file"), expected);
}

#[test]
fn hosts_stay_sorted_and_remove_takes_one_out() {
    let home = TempHome::new();
    let path = home.root().join("config.toml");
    add(&path, "b.example").expect("add");
    add(&path, "a.example").expect("add");
    assert_eq!(
        read(&path),
        Ok(vec!["a.example".into(), "b.example".into()])
    );
    assert_eq!(
        remove(&path, "A.example"),
        Ok(Change::Removed("a.example".into()))
    );
    assert_eq!(read(&path), Ok(vec!["b.example".into()]));
}

#[test]
fn removing_a_host_that_is_not_listed_changes_nothing() {
    let home = TempHome::new();
    let path = home.root().join("config.toml");
    assert_eq!(
        remove(&path, "x.example"),
        Ok(Change::Unchanged("x.example".into()))
    );
    assert!(!path.exists());
}

#[test]
fn a_broken_file_errors_and_keeps_its_bytes() {
    let home = TempHome::new();
    let path = home.root().join("config.toml");
    fs::write(&path, "[dashboard\nhosts = nope").expect("write");
    assert!(read(&path).is_err());
    assert!(add(&path, "x.example").is_err());
    assert!(remove(&path, "x.example").is_err());
    assert_eq!(
        fs::read_to_string(&path).expect("file"),
        "[dashboard\nhosts = nope"
    );
}

#[test]
fn a_file_with_an_invalid_host_is_an_error() {
    let home = TempHome::new();
    let path = home.root().join("config.toml");
    fs::write(&path, "[dashboard]\nhosts = [\"https://x\"]\n").expect("write");
    assert!(read(&path).is_err());
}

#[test]
fn config_dir_honours_xdg_config_home() {
    let env = Env::with(&[("HOME", "/home/u"), ("XDG_CONFIG_HOME", "/x/cfg")]);
    let paths = Paths::from_env(&env).expect("paths");
    assert_eq!(
        config_path(&paths).to_str(),
        Some("/x/cfg/huddle/config.toml")
    );
}

#[test]
fn config_dir_falls_back_to_home_config() {
    for env in [
        Env::with(&[("HOME", "/home/u")]),
        Env::with(&[("HOME", "/home/u"), ("XDG_CONFIG_HOME", "")]),
    ] {
        let paths = Paths::from_env(&env).expect("paths");
        assert_eq!(
            config_path(&paths).to_str(),
            Some("/home/u/.config/huddle/config.toml")
        );
    }
}

#[test]
fn t2_the_default_config_is_written_once_and_a_hand_edit_stays() {
    let home = TempHome::new();
    let path = home.root().join("config.toml");

    assert!(write_default_if_missing(&path).expect("first"));
    let text = fs::read_to_string(&path).expect("read");
    assert!(text.contains("[history]"), "{text}");
    assert!(text.contains("keep_days = 90"), "{text}");
    assert_eq!(read(&path).expect("parses"), Vec::<String>::new());

    fs::write(&path, "[history]\nkeep_days = 7\n").expect("edit");
    assert!(!write_default_if_missing(&path).expect("second"));
    assert_eq!(
        fs::read_to_string(&path).expect("read"),
        "[history]\nkeep_days = 7\n"
    );
}

#[test]
fn t2_adding_a_dashboard_host_keeps_the_other_settings() {
    let home = TempHome::new();
    let path = home.root().join("config.toml");
    fs::write(
        &path,
        "note = \"mine\"\n\n[history]\nkeep_days = 30\n\n[extra]\nflag = true\n",
    )
    .expect("write");

    add(&path, "a.example").expect("add");
    let table: toml::Table =
        toml::from_str(&fs::read_to_string(&path).expect("read")).expect("toml");
    assert_eq!(table["history"]["keep_days"].as_integer(), Some(30));
    assert_eq!(table["note"].as_str(), Some("mine"));
    assert_eq!(table["extra"]["flag"].as_bool(), Some(true));
    assert_eq!(read(&path), Ok(vec!["a.example".into()]));

    remove(&path, "a.example").expect("remove");
    let table: toml::Table =
        toml::from_str(&fs::read_to_string(&path).expect("read")).expect("toml");
    assert_eq!(table["history"]["keep_days"].as_integer(), Some(30));
    assert_eq!(table["note"].as_str(), Some("mine"));
    assert_eq!(table["extra"]["flag"].as_bool(), Some(true));
    assert_eq!(read(&path), Ok(vec![]));
}
