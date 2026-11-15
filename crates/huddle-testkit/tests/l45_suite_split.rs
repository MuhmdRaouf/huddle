//! The unit/e2e split of the test suite (ticket L45).
//!
//! Every integration test target of the workspace belongs to exactly one suite. The e2e suite is
//! every integration target of package `huddle` (each starts the real binary), plus the targets
//! `release/e2e-tests` names. The unit suite is everything else. A target in neither set never
//! runs in the split tasks; a target in both runs twice.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .unwrap()
        .to_path_buf()
}

/// Every integration test target: one `(package, target)` per top-level `.rs` file under
/// `crates/<package>/tests/`. Cargo derives one test target per such file.
fn integration_targets(root: &Path) -> Vec<(String, String)> {
    let crates = root.join("crates");
    let mut packages: Vec<PathBuf> = fs::read_dir(&crates)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .filter(|dir| dir.join("tests").is_dir())
        .collect();
    packages.sort();
    let mut targets = Vec::new();
    for dir in packages {
        let package = dir.file_name().unwrap().to_string_lossy().into_owned();
        let mut files: Vec<PathBuf> = fs::read_dir(dir.join("tests"))
            .unwrap()
            .map(|entry| entry.unwrap().path())
            .filter(|path| path.is_file() && path.extension().is_some_and(|ext| ext == "rs"))
            .collect();
        files.sort();
        for file in files {
            let target = file.file_stem().unwrap().to_string_lossy().into_owned();
            targets.push((package.clone(), target));
        }
    }
    targets
}

/// The e2e-only extras: one `(package, target)` per non-comment line of `release/e2e-tests`.
fn e2e_extras(root: &Path) -> Vec<(String, String)> {
    let file = root.join("release/e2e-tests");
    let text = fs::read_to_string(&file).unwrap_or_else(|_| {
        panic!(
            "{} is missing: the unit/e2e split is not established",
            file.display()
        )
    });
    let mut extras = Vec::new();
    for (at, line) in text.lines().enumerate() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let mut parts = line.split_whitespace();
        let (package, target) = match (parts.next(), parts.next(), parts.next()) {
            (Some(package), Some(target), None) => (package, target),
            _ => panic!(
                "{}:{}: expected `<package> <target>`, found `{line}`",
                file.display(),
                at + 1
            ),
        };
        extras.push((package.to_string(), target.to_string()));
    }
    extras
}

#[test]
fn l45_unit_and_e2e_sets_cover_every_test_target() {
    let root = repo_root();
    let targets = integration_targets(&root);
    assert!(
        !targets.is_empty(),
        "no integration test targets found under crates/*/tests"
    );

    // `huddle` targets are e2e by rule; everything else is unit until the extras file claims it.
    let mut e2e: BTreeMap<(String, String), bool> = targets
        .into_iter()
        .map(|(package, target)| {
            let is_e2e = package == "huddle";
            ((package, target), is_e2e)
        })
        .collect();

    for (package, target) in e2e_extras(&root) {
        match e2e.get(&(package.clone(), target.clone())) {
            Some(true) => panic!(
                "l45: `{package} {target}` is in both sets: every integration target of package huddle is already e2e"
            ),
            Some(false) => {
                e2e.insert((package.clone(), target.clone()), true);
            }
            None => panic!(
                "l45: `{package} {target}` is in neither set: release/e2e-tests names no test target under crates/{package}/tests"
            ),
        }
    }

    let e2e_count = e2e.values().filter(|is_e2e| **is_e2e).count();
    println!(
        "l45: {} unit targets, {e2e_count} e2e targets, {} in total",
        e2e.len() - e2e_count,
        e2e.len()
    );
}
