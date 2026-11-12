use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::path::PathBuf;
use std::process::Output;

fn setup(home: &TempHome) -> Output {
    home.command(env!("CARGO_BIN_EXE_huddle"))
        .arg("setup")
        .current_dir(home.root())
        .env("PATH", "/usr/bin:/bin")
        .output()
        .expect("run huddle")
}

fn installed_plugins(home: &TempHome) -> PathBuf {
    let dir = home.root().join(".claude").join("plugins");
    fs::create_dir_all(&dir).unwrap();
    dir.join("installed_plugins.json")
}

fn stdout(out: &Output) -> String {
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).into_owned()
}

#[test]
fn k5_setup_names_each_old_provider_plugin() {
    let home = TempHome::new();
    fs::write(
        installed_plugins(&home),
        r#"{"version":2,"plugins":{"huddle@muhmdraouf":[{}],"zai-plugin-cc@muhmdraouf":[{}]}}"#,
    )
    .unwrap();
    let text = stdout(&setup(&home));
    assert!(
        text.contains("The zai plugin is part of huddle.\n"),
        "{text}"
    );
    assert!(
        text.contains("Remove the old one: /plugin uninstall zai-plugin-cc@muhmdraouf\n"),
        "{text}"
    );
    assert!(
        text.contains("Then use /huddle:zai:setup, /huddle:zai:remove, /huddle:zai:usage\n"),
        "{text}"
    );
    assert!(!text.contains("The huddle plugin"), "{text}");
    assert!(!text.contains("The kimi plugin"), "{text}");
}

#[test]
fn k5_setup_says_nothing_without_old_plugins() {
    for content in [
        None,
        Some("not json {"),
        Some(r#"{"version":2,"plugins":{"huddle@muhmdraouf":[{}]}}"#),
    ] {
        let home = TempHome::new();
        if let Some(content) = content {
            fs::write(installed_plugins(&home), content).unwrap();
        }
        let text = stdout(&setup(&home));
        assert!(!text.contains("is part of huddle"), "{content:?}: {text}");
    }
}

#[test]
fn k5_setup_leaves_claude_code_plugin_files_untouched() {
    let home = TempHome::new();
    let file = installed_plugins(&home);
    fs::write(
        &file,
        r#"{"version":2,"plugins":{"qwen-plugin-cc@muhmdraouf":[]}}"#,
    )
    .unwrap();
    let before = (
        fs::read(&file).unwrap(),
        fs::metadata(&file).unwrap().modified().unwrap(),
    );
    let text = stdout(&setup(&home));
    assert!(
        text.contains("The qwen plugin is part of huddle."),
        "{text}"
    );
    let after = (
        fs::read(&file).unwrap(),
        fs::metadata(&file).unwrap().modified().unwrap(),
    );
    assert_eq!(before, after);
}
