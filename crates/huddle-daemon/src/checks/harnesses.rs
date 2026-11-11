//! The harness table the dashboard shows: the rows `/huddle:setup` prints.

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::check::{Report, check_all};
use huddle_harness::entries::builtin;
use huddle_harness::user_entries::all_entries;

use super::Phase;

#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Status {
    Ready,
    Fail,
    Missing,
}

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct HarnessRow {
    pub id: String,
    pub name: String,
    pub status: Status,
    pub bin: String,
    pub version: String,
    pub flags: &'static str,
    pub report: String,
    pub access: Vec<String>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize)]
pub struct HarnessesReport {
    pub state: Phase,
    pub rows: Vec<HarnessRow>,
}

/// One row from a harness's readiness report. The binary check decides `missing`; a binary
/// whose version probe fails is `fail`.
pub fn row(report: &Report) -> HarnessRow {
    let detail_of = |name: &str| {
        report
            .checks
            .iter()
            .find(|line| line.name == name && line.ok)
            .map(|line| line.detail.clone())
    };
    let bin = detail_of("binary");
    let version = detail_of("version");
    let status = if bin.is_none() {
        Status::Missing
    } else if report.ready {
        Status::Ready
    } else {
        Status::Fail
    };
    HarnessRow {
        id: report.id.clone(),
        name: report.label.clone(),
        status,
        bin: bin.unwrap_or_default(),
        version: version.unwrap_or_default(),
        flags: if report.flags_verified {
            "checked"
        } else {
            "unverified"
        },
        report: report.report_kind.clone(),
        access: report.access.clone(),
    }
}

/// Checks every harness, builtin then user entries, with no smoke run. Each version probe is
/// capped by the harness crate's own timeout, so one hung binary cannot hold the page. An
/// unreadable `harnesses.json` leaves the builtin table.
pub fn read(env: &Env, paths: &Paths) -> HarnessesReport {
    let entries =
        all_entries(&paths.huddle_dir.join("harnesses.json")).unwrap_or_else(|_| builtin());
    HarnessesReport {
        state: Phase::Ready,
        rows: check_all(&entries, env).iter().map(row).collect(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use huddle_harness::check::CheckLine;
    use huddle_testkit::temp_home::TempHome;

    fn line(name: &str, ok: bool, detail: &str) -> CheckLine {
        CheckLine {
            name: name.into(),
            ok,
            detail: detail.into(),
        }
    }

    fn report(binary: CheckLine, version: CheckLine) -> Report {
        Report {
            id: "codex".into(),
            label: "Codex CLI".into(),
            ready: binary.ok && version.ok,
            checks: vec![binary, version],
            flags_verified: true,
            report_kind: "extracted".into(),
            resume: true,
            access: vec!["readonly".into(), "write".into()],
            notes: Vec::new(),
            next_steps: Vec::new(),
        }
    }

    #[test]
    fn a_ready_harness_carries_its_binary_version_flags_and_access() {
        let r = report(
            line("binary", true, "/usr/bin/codex"),
            line("version", true, "codex 1.2.3"),
        );
        assert_eq!(
            row(&r),
            HarnessRow {
                id: "codex".into(),
                name: "Codex CLI".into(),
                status: Status::Ready,
                bin: "/usr/bin/codex".into(),
                version: "codex 1.2.3".into(),
                flags: "checked",
                report: "extracted".into(),
                access: vec!["readonly".into(), "write".into()],
            }
        );
    }

    #[test]
    fn no_binary_is_missing_and_a_failing_probe_is_fail() {
        let missing = row(&report(
            line("binary", false, "codex is not on PATH"),
            line("version", false, "not run: no binary"),
        ));
        assert_eq!(missing.status, Status::Missing);
        assert_eq!((missing.bin.as_str(), missing.version.as_str()), ("", ""));
        let failing = row(&report(
            line("binary", true, "/usr/bin/codex"),
            line("version", false, "command failed"),
        ));
        assert_eq!(failing.status, Status::Fail);
        assert_eq!(failing.bin, "/usr/bin/codex");
        assert_eq!(failing.version, "");
    }

    #[test]
    fn unverified_flags_say_so() {
        let mut r = report(line("binary", true, "/b"), line("version", true, "v"));
        r.flags_verified = false;
        assert_eq!(row(&r).flags, "unverified");
    }

    #[test]
    fn a_machine_with_an_empty_path_lists_the_table_and_finds_no_path_only_harness() {
        let home = TempHome::new();
        let root = home.root().display().to_string();
        let env = Env::with(&[("HOME", root.as_str()), ("PATH", "")]);
        let paths = Paths::from_env(&env).expect("paths");
        let report = read(&env, &paths);
        assert_eq!(report.state, Phase::Ready);
        assert!(report.rows.len() >= 5, "{} rows", report.rows.len());
        // Some entries also try fixed install paths on the real disk, so pin one that does not.
        let codex = report.rows.iter().find(|r| r.id == "codex").expect("codex");
        assert_eq!(codex.status, Status::Missing);
    }
}
