//! The setup and gate commands of a job's checks, and a finished run's verification.

use crate::checks::{Checks, GateResult, Verification, changed_paths, check_scope, derive_verdict};
use crate::git::{self, GitError};
use crate::model::Access;
use huddle_procs::stream::{DEFAULT_TOOL_OUTPUT, ToolSpec, run_tool};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

pub const COMMAND_TIMEOUT: Duration = Duration::from_secs(600);
pub const TAIL_CHARS: usize = 2000;

/// One command: `/bin/sh -c <run>` in `dir`. A spawn error is a failed result: exit code None, tail the error text.
/// `tail` is the last TAIL_CHARS characters of stdout then stderr, lossy UTF-8. `timed_out` is true when `signal`
/// is set and the elapsed time reached COMMAND_TIMEOUT.
pub fn run_command(run: &str, dir: &Path, env: &[(String, String)]) -> GateResult {
    let started = Instant::now();
    let output = run_tool(ToolSpec {
        program: PathBuf::from("/bin/sh"),
        args: vec!["-c".to_string(), run.to_string()],
        cwd: Some(dir.to_path_buf()),
        env: Some(env.to_vec()),
        stdin: None,
        timeout: Some(COMMAND_TIMEOUT),
        max_output: DEFAULT_TOOL_OUTPUT,
    });
    let duration_ms = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
    match output {
        Ok(out) => {
            let timed_out = out.signal.is_some() && started.elapsed() >= COMMAND_TIMEOUT;
            GateResult {
                run: run.to_string(),
                exit_code: out.code,
                timed_out,
                duration_ms,
                tail: tail_text(&out.stdout, &out.stderr),
            }
        }
        Err(error) => GateResult {
            run: run.to_string(),
            exit_code: None,
            timed_out: false,
            duration_ms,
            tail: error.to_string(),
        },
    }
}

/// Setup in order; the first failed setup is the last result and nothing else runs. Then every gate.
pub fn run_commands(checks: &Checks, dir: &Path, env: &[(String, String)]) -> Vec<GateResult> {
    let mut results = Vec::new();
    for run in &checks.setup {
        let result = run_command(run, dir, env);
        let failed = result.failed();
        results.push(result);
        if failed {
            return results;
        }
    }
    for run in &checks.gates {
        results.push(run_command(run, dir, env));
    }
    results
}

/// A finished harness run: changed paths and scope first, then the commands, then what the commands added.
pub fn verify_worktree(
    checks: &Checks,
    access: Access,
    worktree: &Path,
    base: &str,
    env: &[(String, String)],
) -> Result<Verification, GitError> {
    let changed = changed_paths(&git::changes(worktree, base)?);
    let scope = check_scope(checks, access, &changed);
    let gates = run_commands(checks, worktree, env);
    let after = changed_paths(&git::changes(worktree, base)?);
    let gate_touched = after.into_iter().filter(|p| !changed.contains(p)).collect();
    Ok(Verification {
        verdict: derive_verdict(&scope, &gates),
        scope,
        changed,
        gates,
        gate_touched,
    })
}

/// The last TAIL_CHARS characters of stdout then stderr, lossy UTF-8. The cut stays on a
/// character boundary.
fn tail_text(stdout: &[u8], stderr: &[u8]) -> String {
    let mut text = String::from_utf8_lossy(stdout).into_owned();
    text.push_str(&String::from_utf8_lossy(stderr));
    let skip = text.chars().count().saturating_sub(TAIL_CHARS);
    text.chars().skip(skip).collect()
}
