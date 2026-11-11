use huddle_catalog::providers::{Provider, provider_by_id};
use huddle_testkit::temp_home::TempHome;
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};

/// Every sibling compared against zai.
const SIBLINGS: &[&str] = &["kimi", "deepseek", "minimax", "qwen"];
const REFERENCE: &str = "zai";

fn plugin_dir(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join("plugins")
        .join(format!("{name}-plugin-cc"))
}

fn walk(root: &Path, dir: &Path, into: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).expect("read dir") {
        let entry = entry.expect("entry");
        let path = entry.path();
        if entry.file_type().expect("type").is_dir() {
            walk(root, &path, into);
        } else {
            into.push(path.strip_prefix(root).expect("strip").to_path_buf());
        }
    }
}

fn files(name: &str) -> Vec<PathBuf> {
    let root = plugin_dir(name);
    let mut out = Vec::new();
    walk(&root, &root, &mut out);
    out.sort_by_key(|p| p.to_string_lossy().into_owned());
    out
}

fn read(plugin: &str, relative: &Path) -> String {
    fs::read_to_string(plugin_dir(plugin).join(relative)).expect("read")
}

#[test]
fn zai_ships_exactly_the_kept_surface() {
    let want = [
        ".claude-plugin/plugin.json",
        "LICENSE",
        "README.md",
        "agents/glm-5.3-flash.md",
        "agents/glm-5.3.md",
        "commands/board.md",
        "commands/remove.md",
        "commands/review.md",
        "commands/setup.md",
        "commands/setup/claude.md",
        "commands/usage.md",
        "dist/run",
    ];
    let have: Vec<String> = files(REFERENCE)
        .iter()
        .map(|p| p.to_string_lossy().into_owned())
        .collect();
    assert_eq!(have, want.map(String::from).to_vec());
}

#[test]
fn zai_ships_no_hooks_or_engine_files() {
    let root = plugin_dir(REFERENCE);
    assert!(
        !root.join("hooks").exists(),
        "a provider plugin ships no hooks"
    );
    for engine in ["omp", "opencode", "pi", "zcode"] {
        assert!(
            !root.join(format!("agents/{engine}.md")).exists(),
            "{engine} agent"
        );
        assert!(
            !root.join(format!("commands/setup/{engine}.md")).exists(),
            "{engine} setup"
        );
    }
}

#[test]
fn zai_manifest_names_the_provider_and_depends_on_huddle() {
    let manifest: serde_json::Value =
        serde_json::from_str(&read(REFERENCE, Path::new(".claude-plugin/plugin.json")))
            .expect("json");
    assert_eq!(manifest["name"], "zai");
    assert_eq!(manifest["dependencies"], serde_json::json!(["huddle"]));
    assert_eq!(manifest["license"], "GPL-3.0-or-later");
    assert_eq!(manifest["author"]["name"], "Raouf");
    assert!(manifest.get("version").is_none());
}

#[test]
fn every_plugin_license_equals_the_repository_license() {
    let repo =
        fs::read(Path::new(env!("CARGO_MANIFEST_DIR")).join("../../LICENSE")).expect("license");
    assert_eq!(
        fs::read(plugin_dir(REFERENCE).join("LICENSE")).expect("license"),
        repo
    );
}

#[test]
fn t12_dist_run_ignores_a_relative_data_home() {
    use std::os::unix::fs::PermissionsExt;
    let home = TempHome::new();
    let bin_dir = home.root().join(".local/share/huddle/bin");
    fs::create_dir_all(&bin_dir).expect("bin dir");
    let fake = bin_dir.join("huddle");
    fs::write(&fake, "#!/bin/sh\necho \"ran $*\"\n").expect("fake huddle");
    fs::set_permissions(&fake, fs::Permissions::from_mode(0o755)).expect("chmod");
    for plugin in std::iter::once(&REFERENCE).chain(SIBLINGS) {
        let out = home
            .command("sh")
            .arg(plugin_dir(plugin).join("dist/run"))
            .arg("status")
            .env_remove("HUDDLE_DATA_HOME")
            .env("XDG_DATA_HOME", "relative-data")
            .env("HOME", home.root())
            .output()
            .expect("run dist/run");
        assert_eq!(
            String::from_utf8_lossy(&out.stdout),
            "ran status\n",
            "{plugin}: {}",
            String::from_utf8_lossy(&out.stderr)
        );
    }
}

#[test]
fn dist_run_execs_the_installed_binary() {
    let script = read(REFERENCE, Path::new("dist/run"));
    assert!(script.contains("exec \"$bin\" \"$@\""), "{script}");
    assert!(script.contains("bin=$data_home/bin/huddle"), "{script}");
    assert!(script.contains("run /huddle:setup first"), "{script}");
    for sibling in SIBLINGS {
        let text = read(sibling, Path::new("dist/run"));
        assert!(
            text.contains("bin=$data_home/bin/huddle"),
            "{sibling}: {text}"
        );
    }
    use std::os::unix::fs::PermissionsExt;
    assert_ne!(
        fs::metadata(plugin_dir(REFERENCE).join("dist/run"))
            .expect("stat")
            .permissions()
            .mode()
            & 0o111,
        0
    );
}

fn front_matter(text: &str) -> &str {
    text.strip_prefix("---\n")
        .and_then(|rest| rest.split_once("\n---\n"))
        .expect("front matter")
        .0
}

#[test]
fn every_command_declares_a_description_and_the_launcher_tool_and_calls_its_word() {
    let cases = [
        ("board", "status"),
        ("remove", "provider zai remove"),
        ("review", "result"),
        ("setup", "provider zai setup"),
        ("setup/claude", "provider zai setup"),
        ("usage", "provider zai usage"),
    ];
    for (command, word) in cases {
        let text = read(REFERENCE, Path::new(&format!("commands/{command}.md")));
        let head = front_matter(&text);
        assert!(head.contains("description:"), "{command}");
        assert!(head.contains("allowed-tools: Bash(sh:*)"), "{command}");
        assert!(
            text.contains(&format!("sh \"${{CLAUDE_PLUGIN_ROOT}}/dist/run\" {word}")),
            "{command} must call: {word}"
        );
    }
    let review = read(REFERENCE, Path::new("commands/review.md"));
    assert!(
        review.contains("argument-hint"),
        "only review takes an argument"
    );
    for command in ["board", "remove", "setup", "setup/claude", "usage"] {
        let text = read(REFERENCE, Path::new(&format!("commands/{command}.md")));
        assert!(!text.contains("argument-hint"), "{command}");
        assert!(!text.contains("$ARGUMENTS"), "{command}");
    }
}

#[test]
fn every_plugin_root_mention_is_the_quoted_launcher() {
    for file in files(REFERENCE) {
        if file.extension().and_then(|e| e.to_str()) != Some("md") {
            continue;
        }
        let text = read(REFERENCE, &file);
        for (at, _) in text.match_indices("${CLAUDE_PLUGIN_ROOT}") {
            let around = &text[at.saturating_sub(1)
                ..(at + "${CLAUDE_PLUGIN_ROOT}/dist/run\"".len()).min(text.len())];
            assert_eq!(
                around,
                "\"${CLAUDE_PLUGIN_ROOT}/dist/run\"",
                "{}",
                file.display()
            );
        }
    }
}

fn agent_field<'a>(text: &'a str, field: &str) -> &'a str {
    front_matter(text)
        .lines()
        .find_map(|line| line.strip_prefix(&format!("{field}: ")))
        .expect("field")
}

#[test]
fn zai_agents_are_on_their_fixed_model_ids() {
    let zai = provider_by_id("zai").expect("zai");
    for (file, model, tier_color) in [
        ("glm-5.3", zai.main.id, "green"),
        ("glm-5.3-flash", zai.flash.id, "cyan"),
    ] {
        let text = read(REFERENCE, Path::new(&format!("agents/{file}.md")));
        assert_eq!(agent_field(&text, "name"), file);
        assert_eq!(agent_field(&text, "model"), model);
        assert_eq!(agent_field(&text, "effort"), "max");
        assert_eq!(agent_field(&text, "color"), tier_color);
        assert!(
            text.contains(&format!(
                "It runs on {model} through huddle-router once /zai:setup has added it."
            )),
            "{file}"
        );
        assert!(!text.contains("sonnet"), "{file} still names sonnet");
    }
}

// The sibling guard: every sibling's files, normalised, must equal zai's.

fn brand_words(id: &str) -> &'static [&'static str] {
    match id {
        "zai" => &["GLM", "Z.ai"],
        "kimi" => &["Kimi", "Moonshot AI"],
        "deepseek" => &["DeepSeek"],
        "minimax" => &["MiniMax"],
        "qwen" => &["Qwen", "Alibaba Cloud"],
        _ => &[],
    }
}

fn capitalized(id: &str) -> String {
    let mut chars = id.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().collect::<String>() + chars.as_str(),
        None => String::new(),
    }
}

/// One provider's replacement pairs, longest first.
fn names_of(provider: &Provider) -> Vec<(String, String)> {
    let mut pairs: Vec<(String, String)> = Vec::new();
    for prefix in provider.model_prefixes {
        pairs.push((format!("`{prefix}*`"), "`<prefix>*`".to_string()));
    }
    pairs.push((provider.main.label.to_string(), "<MainLabel>".to_string()));
    pairs.push((provider.flash.label.to_string(), "<FlashLabel>".to_string()));
    pairs.push((provider.main.id.to_string(), "<main-id>".to_string()));
    pairs.push((provider.flash.id.to_string(), "<flash-id>".to_string()));
    pairs.push((provider.display.to_string(), "<Brand>".to_string()));
    for word in brand_words(provider.id) {
        pairs.push(((*word).to_string(), "<Brand>".to_string()));
    }
    pairs.push((provider.id.to_uppercase(), "<NAME>".to_string()));
    pairs.push((capitalized(provider.id), "<Brand>".to_string()));
    pairs.push((provider.id.to_string(), "<name>".to_string()));
    pairs.sort_by_key(|(from, _)| std::cmp::Reverse(from.len()));
    pairs
}

fn dewrap(text: &str) -> String {
    let chars: Vec<char> = text.chars().collect();
    let mut out = String::new();
    let mut i = 0;
    while i < chars.len() {
        let mut j = i;
        while j < chars.len() && (chars[j] == ' ' || chars[j] == '\t') {
            j += 1;
        }
        if j < chars.len() && chars[j] == '\n' && (j + 1 >= chars.len() || chars[j + 1] != '\n') {
            j += 1;
            while j < chars.len() && (chars[j] == ' ' || chars[j] == '\t') {
                j += 1;
            }
            out.push(' ');
            i = j;
            continue;
        }
        out.push(chars[i]);
        i += 1;
    }
    out
}

fn normalise(text: &str, names: &[(String, String)], path: &str) -> String {
    let mut text = if path.ends_with(".md") {
        dewrap(text)
    } else {
        text.to_string()
    };
    for (from, to) in names {
        text = text.replace(from, to);
    }
    text
}

fn allowed_difference(canonical: &str, sibling: &str) -> bool {
    match canonical {
        "README.md" | ".claude-plugin/plugin.json" => true,
        "agents/<flash-id>.md" => sibling == "minimax",
        _ => false,
    }
}

#[test]
fn the_five_provider_plugins_stay_copies_of_zai() {
    let reference_names = names_of(provider_by_id(REFERENCE).expect("zai"));
    let reference_files: BTreeMap<String, PathBuf> = files(REFERENCE)
        .into_iter()
        .map(|p| {
            (
                normalise(&p.to_string_lossy(), &reference_names, &p.to_string_lossy()),
                p,
            )
        })
        .collect();
    let mut failures: Vec<String> = Vec::new();
    for sibling in SIBLINGS {
        let names = names_of(provider_by_id(sibling).expect("sibling"));
        let sibling_files: BTreeMap<String, PathBuf> = files(sibling)
            .into_iter()
            .map(|p| {
                (
                    normalise(&p.to_string_lossy(), &names, &p.to_string_lossy()),
                    p,
                )
            })
            .collect();
        for (canonical, path) in &reference_files {
            if !sibling_files.contains_key(canonical) {
                failures.push(format!(
                    "{sibling}: missing {canonical} ({REFERENCE} has {})",
                    path.display()
                ));
            }
        }
        for (canonical, path) in &sibling_files {
            let Some(reference_path) = reference_files.get(canonical) else {
                failures.push(format!(
                    "{sibling}: {} has no counterpart in {REFERENCE}",
                    path.display()
                ));
                continue;
            };
            if allowed_difference(canonical, sibling) {
                continue;
            }
            let ours = normalise(&read(sibling, path), &names, &path.to_string_lossy());
            let theirs = normalise(
                &read(REFERENCE, reference_path),
                &reference_names,
                &reference_path.to_string_lossy(),
            );
            if ours != theirs {
                failures.push(format!(
                    "{sibling}: {} differs from {}",
                    path.display(),
                    reference_path.display()
                ));
            }
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
fn t1_every_dist_run_names_the_installed_binary_in_the_data_folder() {
    for name in std::iter::once(&REFERENCE).chain(SIBLINGS) {
        let text = read(name, Path::new("dist/run"));
        for part in [
            "HUDDLE_DATA_HOME",
            "XDG_DATA_HOME",
            "$HOME/.local/share",
            "bin=$data_home/bin/huddle",
        ] {
            assert!(text.contains(part), "{name}: {part}");
        }
    }
    let script =
        fs::read_to_string(Path::new(env!("CARGO_MANIFEST_DIR")).join("../../plugin/dist/run"))
            .expect("plugin dist/run");
    for part in [
        "HUDDLE_DATA_HOME",
        "XDG_DATA_HOME",
        "$HOME/.local/share",
        "bin_dir=$data_home/bin",
    ] {
        assert!(script.contains(part), "{part}");
    }
}
