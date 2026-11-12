use huddle_catalog::providers::providers;
use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_setup::agent_files::{provider_agent_text, sync, sync_provider};
use huddle_testkit::temp_home::TempHome;

fn paths_for(home: &TempHome) -> Paths {
    let root = home.root().to_string_lossy().into_owned();
    Paths::from_env(&Env::with(&[("HOME", root.as_str())])).expect("paths")
}

#[test]
fn k2_every_provider_has_two_agents_with_bare_names() {
    for provider in providers() {
        for model in [provider.main, provider.flash] {
            let text = provider_agent_text(provider, model.id);
            assert!(
                text.contains(&format!("\nname: {}\n", model.id)),
                "{}",
                provider.id
            );
            assert!(text.contains(&format!("\nmodel: {}\n", model.id)));
            assert!(text.contains("\nmanaged-by: huddle\n"));
            assert!(!text.contains(&format!("{}:", provider.id)), "{text}");
            assert!(!text.contains("/zai:setup"), "{text}");
            assert!(!text.contains("plugin's CLI"), "{text}");
            assert!(text.contains("Never run huddle's provider commands."));
        }
    }
}

#[test]
fn k2_the_family_sync_keeps_provider_agents() {
    let home = TempHome::new();
    let paths = paths_for(&home);
    let zai = &providers()[0];
    sync_provider(&paths, zai).expect("sync provider");
    sync(&paths).expect("sync");
    let dir = paths.claude_dir.join("agents");
    assert!(dir.join("glm-5.3.md").exists());
    assert!(dir.join("glm-5.3-flash.md").exists());
}
