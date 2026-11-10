use huddle_config::env::Env;
use huddle_harness::check::{
    Report, SmokeRunner, check_all, check_entry, render_all_markdown, render_json, render_markdown,
};
use huddle_harness::entries::{builtin, find};
use huddle_harness::resolve::override_var;
use huddle_harness::table::Entry;
use huddle_testkit::temp_home::TempHome;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;

fn tool(root: &Path, name: &str, version_line: &str, exit_code: i32) -> String {
    let path = root.join("bin").join(name);
    fs::create_dir_all(path.parent().expect("parent")).expect("dir");
    // Stage and rename: exec'ing an inode that still reads as write-open is
    // Text file busy on overlayfs, so the final path never sees a writer.
    let staged = root.join("bin").join(format!("{name}.staged"));
    fs::write(
        &staged,
        format!("#!/bin/sh\necho '{version_line}'\nexit {exit_code}\n"),
    )
    .expect("write");
    fs::set_permissions(&staged, fs::Permissions::from_mode(0o755)).expect("chmod");
    fs::rename(&staged, &path).expect("rename");
    path.display().to_string()
}

fn with_override(entry: &Entry, path: &str) -> Env {
    let var = override_var(&entry.id);
    Env::with(&[(var.as_str(), path), ("PATH", "/usr/bin:/bin")])
}

#[test]
fn a_found_harness_with_a_version_is_ready() {
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let path = tool(home.root(), "omp", "omp/18.6.1", 0);
    let report = check_entry(&entry, &with_override(&entry, &path), None);
    assert!(report.ready);
    assert_eq!(report.id, "omp");
    assert_eq!(report.label, "Oh My Pi");
    assert_eq!(report.checks[0].name, "binary");
    assert_eq!(report.checks[0].detail, path);
    assert_eq!(
        report.checks[1],
        huddle_harness::check::CheckLine {
            name: "version".into(),
            ok: true,
            detail: "omp/18.6.1".into()
        }
    );
    assert!(report.flags_verified);
    assert_eq!(report.report_kind, "checked");
    assert_eq!(report.access, vec!["readonly", "exec", "write"]);
    assert!(report.resume);
    assert!(report.next_steps.is_empty());
    assert_eq!(report.checks.len(), 2);
}

#[test]
fn a_missing_binary_skips_the_version_and_tells_what_to_install() {
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let env = Env::with(&[("PATH", home.root().to_str().expect("utf8"))]);
    let report = check_entry(&entry, &env, None);
    assert!(!report.ready);
    assert!(!report.checks[0].ok);
    assert_eq!(report.checks[0].detail, "omp is not on PATH");
    assert_eq!(report.checks[1].detail, "not run: no binary");
    assert_eq!(report.next_steps, vec![entry.check.install_hint.clone()]);
}

#[test]
fn a_failing_version_probe_points_at_login() {
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let path = tool(home.root(), "omp", "boom", 1);
    let report = check_entry(&entry, &with_override(&entry, &path), None);
    assert!(!report.ready);
    assert!(!report.checks[1].ok);
    assert!(report.checks[1].detail.contains("--version"));
    assert_eq!(report.next_steps, vec![entry.check.login_hint.clone()]);
}

struct FakeSmoke(Result<String, String>);
impl SmokeRunner for FakeSmoke {
    fn smoke(&self, _entry: &Entry) -> Result<String, String> {
        self.0.clone()
    }
}

#[test]
fn a_smoke_run_joins_the_ready_rule_and_shows_its_line() {
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let path = tool(home.root(), "omp", "omp/18.6.1", 0);
    let env = with_override(&entry, &path);
    let ok = FakeSmoke(Ok(
        "ok: omp answered on zai/glm-5.3 (1 turn, $0.0001)".into()
    ));
    let report = check_entry(&entry, &env, Some(&ok));
    assert!(report.ready);
    assert_eq!(report.checks[2].name, "smoke");

    let bad = FakeSmoke(Err("crashed; run omp once and log in".into()));
    let report = check_entry(&entry, &env, Some(&bad));
    assert!(!report.ready);
    assert_eq!(report.checks[2].detail, "crashed; run omp once and log in");
}

#[test]
fn the_smoke_never_runs_without_a_binary() {
    struct NeverRun;
    impl SmokeRunner for NeverRun {
        fn smoke(&self, _: &Entry) -> Result<String, String> {
            panic!("must not run")
        }
    }
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let env = Env::with(&[("PATH", home.root().to_str().expect("utf8"))]);
    let report = check_entry(&entry, &env, Some(&NeverRun));
    assert_eq!(
        report.checks[2].detail,
        "not run: fix the lines above first"
    );
}

#[test]
fn unverified_flags_are_said_out_loud() {
    let home = TempHome::new();
    let entry = find("grok").expect("grok");
    let path = tool(home.root(), "grok", "grok 1.0", 0);
    let report = check_entry(&entry, &with_override(&entry, &path), None);
    assert!(!report.flags_verified);
    assert!(report.next_steps.iter().any(|s| s.contains("unverified")));
    let text = render_markdown(&report);
    assert!(text.contains("Flags: unverified until a smoke run"));
    assert!(text.contains("Report: free text"));
}

#[test]
fn the_markdown_report_reads_as_specified() {
    let report = Report {
        id: "omp".into(),
        label: "Oh My Pi".into(),
        ready: false,
        checks: vec![
            huddle_harness::check::CheckLine {
                name: "binary".into(),
                ok: false,
                detail: "omp is not on PATH".into(),
            },
            huddle_harness::check::CheckLine {
                name: "version".into(),
                ok: false,
                detail: "not run: no binary".into(),
            },
        ],
        flags_verified: true,
        report_kind: "checked".into(),
        resume: true,
        access: vec!["readonly".into(), "exec".into(), "write".into()],
        notes: vec!["a note".into()],
        next_steps: vec!["Install Oh My Pi and make sure `omp` is on PATH.".into()],
    };
    let expected = "# Oh My Pi (omp)\nStatus: needs attention\nChecks:\n- binary: FAILED: omp is not on PATH\n- version: FAILED: not run: no binary\nFlags: checked\nReport: checked\nResume: yes\nAccess: readonly, exec, write\nNotes:\n- a note\nNext steps:\n- Install Oh My Pi and make sure `omp` is on PATH.\n";
    assert_eq!(render_markdown(&report), expected);
}

#[test]
fn every_builtin_entry_gets_a_report() {
    let home = TempHome::new();
    let env = Env::with(&[("PATH", home.root().to_str().expect("utf8"))]);
    let reports = check_all(&builtin(), &env);
    assert_eq!(reports.len(), builtin().len());
    let table = render_all_markdown(&reports);
    assert!(table.starts_with("| Harness | Status | Binary | Version | Flags | Report | Access |\n|---|---|---|---|---|---|---|\n"));
    assert!(table.contains("| Oh My Pi (omp) | needs attention | FAILED: omp is not on PATH |"));
    assert!(table.contains("| Grok (grok) | needs attention |"));
    let json: serde_json::Value =
        serde_json::from_str(&huddle_harness::check::render_all_json(&reports)).expect("json");
    assert_eq!(json.as_array().map(|a| a.len()), Some(reports.len()));
    let one: serde_json::Value = serde_json::from_str(&render_json(&reports[0])).expect("json");
    assert_eq!(one["id"], "amp");
}

#[test]
fn a_version_probe_never_runs_in_the_callers_directory() {
    let home = TempHome::new();
    let entry = find("omp").expect("omp");
    let bin = home.root().join("probe-bin").join("omp");
    fs::create_dir_all(bin.parent().expect("parent")).expect("dir");
    fs::write(
        &bin,
        "#!/bin/sh\npwd > probe_cwd.txt\nprintf 'omp/18.6.1\\n'\n",
    )
    .expect("write");
    fs::set_permissions(&bin, fs::Permissions::from_mode(0o755)).expect("chmod");
    let report = {
        let var = override_var(&entry.id);
        let path = bin.display().to_string();
        let env = Env::with(&[
            (var.as_str(), path.as_str()),
            ("HOME", home.root().to_str().expect("utf8")),
            ("PATH", "/usr/bin:/bin"),
        ]);
        check_entry(&entry, &env, None)
    };
    let failed: Vec<String> = report
        .checks
        .iter()
        .filter(|line| !line.ok)
        .map(|line| format!("{}: {}", line.name, line.detail))
        .collect();
    assert!(failed.is_empty(), "failing checks: {failed:?}");
    assert!(
        home.root().join("probe_cwd.txt").exists(),
        "the probe runs in the env's HOME"
    );
    assert!(
        !Path::new("probe_cwd.txt").exists(),
        "the probe never runs in the caller's cwd"
    );
}
