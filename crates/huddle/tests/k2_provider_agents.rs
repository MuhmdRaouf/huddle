use huddle_testkit::fake_upstream::{FakeUpstream, Step};
use huddle_testkit::{ports::free_port, temp_home::TempHome};
use std::{
    fs,
    os::unix::fs::PermissionsExt,
    path::PathBuf,
    process::{Command, Output, Stdio},
};

fn huddle(home: &TempHome, port: u16, args: &[&str], extra: &[(&str, &str)]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_huddle"))
        .args(args)
        .env_clear()
        .envs(home.env())
        .env("HUDDLE_ROUTER_PORT", port.to_string())
        .envs(extra.iter().copied())
        .stdin(Stdio::null())
        .output()
        .expect("run")
}

fn fake_answering(status: u16) -> FakeUpstream {
    tokio::runtime::Runtime::new()
        .expect("runtime")
        .block_on(FakeUpstream::start(move |_, _| Step::answer(status, "{}")))
}

fn stored_zai_key(home: &TempHome) {
    let dir = home.root().join(".config/zai-plugin-cc");
    fs::create_dir_all(&dir).expect("mkdir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("chmod");
    let file = dir.join("env");
    fs::write(&file, "ZAI_API_KEY=abcdefghijklmnop\n").expect("write");
    fs::set_permissions(&file, fs::Permissions::from_mode(0o600)).expect("chmod");
}

fn agent(home: &TempHome, id: &str) -> PathBuf {
    home.root().join(".claude/agents").join(format!("{id}.md"))
}

fn setup(home: &TempHome, port: u16, status: u16) -> String {
    let fake = fake_answering(status);
    let output = huddle(
        home,
        port,
        &["provider", "zai", "setup"],
        &[("ZAI_BASE_URL", fake.url().as_str())],
    );
    let _ = huddle(home, port, &["router", "stop"], &[]);
    String::from_utf8_lossy(&output.stdout).into_owned()
}

#[test]
fn k2_setup_writes_the_providers_two_agents() {
    let home = TempHome::new();
    let port = free_port();
    stored_zai_key(&home);
    let stdout = setup(&home, port, 200);
    assert!(
        stdout.contains("agents: glm-5.3, glm-5.3-flash"),
        "{stdout}"
    );
    for id in ["glm-5.3", "glm-5.3-flash"] {
        let text = fs::read_to_string(agent(&home, id)).expect("agent file");
        assert!(text.contains(&format!("\nname: {id}\n")), "{text}");
        assert!(text.contains(&format!("\nmodel: {id}\n")), "{text}");
        assert!(text.contains("\nmanaged-by: huddle\n"), "{text}");
        assert!(!text.contains("zai:"), "{text}");
        assert!(!text.contains("/zai:setup"), "{text}");
    }
    let again = setup(&home, port, 200);
    assert!(again.contains("agents: glm-5.3, glm-5.3-flash"), "{again}");
}

#[test]
fn k2_a_refused_key_writes_no_agents() {
    let home = TempHome::new();
    let port = free_port();
    stored_zai_key(&home);
    let stdout = setup(&home, port, 401);
    assert!(stdout.contains("refused"), "{stdout}");
    assert!(!agent(&home, "glm-5.3").exists());
    assert!(!agent(&home, "glm-5.3-flash").exists());
}

#[test]
fn k2_remove_deletes_only_huddles_agent_files() {
    let home = TempHome::new();
    let port = free_port();
    stored_zai_key(&home);
    let dir = home.root().join(".claude/agents");
    fs::create_dir_all(&dir).expect("mkdir");
    let theirs = "---\nname: glm-5.3\n---\nmine\n";
    fs::write(agent(&home, "glm-5.3"), theirs).expect("write");

    let stdout = setup(&home, port, 200);
    assert!(
        stdout.contains("glm-5.3.md exists and is not huddle's; left as it is"),
        "{stdout}"
    );
    assert_eq!(
        fs::read_to_string(agent(&home, "glm-5.3")).expect("read"),
        theirs
    );
    assert!(agent(&home, "glm-5.3-flash").exists());

    let removed = huddle(&home, port, &["provider", "zai", "remove"], &[]);
    let out = String::from_utf8_lossy(&removed.stdout).into_owned();
    assert!(out.contains("took glm-5.3-flash out of agents"), "{out}");
    assert_eq!(
        fs::read_to_string(agent(&home, "glm-5.3")).expect("read"),
        theirs
    );
    assert!(!agent(&home, "glm-5.3-flash").exists());
}

#[test]
fn k2_uninstall_removes_provider_agents() {
    let home = TempHome::new();
    let port = free_port();
    stored_zai_key(&home);
    setup(&home, port, 200);
    assert!(agent(&home, "glm-5.3").exists());
    let _ = huddle(&home, port, &["uninstall"], &[]);
    let _ = huddle(&home, port, &["router", "stop"], &[]);
    assert!(!agent(&home, "glm-5.3").exists());
    assert!(!agent(&home, "glm-5.3-flash").exists());
}
