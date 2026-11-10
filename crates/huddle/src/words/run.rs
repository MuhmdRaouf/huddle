//! The run word: submit a task to a harness, start its detached driver, and wait for the job
//! or return at once. `--drive` is the driver's own entry.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::thread;
use std::time::Duration;

use huddle_config::env::Env;
use huddle_config::paths::Paths;
use huddle_harness::check::check_entry;
use huddle_harness::user_entries::all_entries;
use huddle_jobs::checks::{self, Checks};
use huddle_jobs::checks_render;
use huddle_jobs::driver::{self, DriveError, SubmitInput};
use huddle_jobs::git::{self, ChangeSet, DiffMode, GitError};
use huddle_jobs::id::{new_job_id, now_ms};
use huddle_jobs::liveness;
use huddle_jobs::model::{Access, Job, JobState};
use huddle_jobs::render::{self, ReviewView};
use huddle_jobs::store::JobStore;

const USAGE: &str = "usage: huddle run <harness> [--background] [--readonly|--exec|--write] [--effort <e>] [--setup <cmd>]... [--gate <cmd>]... [--scope <glob>]... [--forbid <glob>]... <task>";

/// The wait polls at this delay first, doubling up to MAX_POLL_MS.
const FIRST_POLL_MS: u64 = 1_000;
const MAX_POLL_MS: u64 = 5_000;

pub fn run(args: &[String], env: &Env) -> i32 {
    if args.first().map(String::as_str) == Some("--drive") {
        return drive_job(args, env);
    }
    let parsed = match parse_args(
        args,
        &["background", "readonly", "exec", "write", "help"],
        &["effort", "forbid", "gate", "scope", "setup"],
    ) {
        Ok(parsed) => parsed,
        Err(message) => {
            let _ = writeln!(std::io::stderr(), "huddle: {message}");
            let _ = writeln!(std::io::stderr(), "{USAGE}");
            return 2;
        }
    };
    if parsed.has("help") {
        let _ = writeln!(std::io::stdout(), "{USAGE}");
        return 0;
    }
    if parsed.positionals.len() < 2 {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: run needs a harness id and a task\n{USAGE}"
        );
        return 2;
    }
    let id = parsed.positionals[0].clone();
    let task = parsed.positionals[1..].join(" ");
    if task.trim().is_empty() {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: run needs a harness id and a task\n{USAGE}"
        );
        return 2;
    }
    let access = match (
        parsed.has("readonly"),
        parsed.has("exec"),
        parsed.has("write"),
    ) {
        (false, false, false) | (false, false, true) => Access::Write,
        (true, false, false) => Access::Readonly,
        (false, true, false) => Access::Exec,
        _ => {
            let _ = writeln!(
                std::io::stderr(),
                "huddle: --readonly, --exec and --write exclude each other\n{USAGE}"
            );
            return 2;
        }
    };
    let paths = match Paths::from_env(env) {
        Ok(paths) => paths,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "{error}");
            return 1;
        }
    };
    let entries = match all_entries(&paths.huddle_dir.join("harnesses.json")) {
        Ok(entries) => entries,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let Some(entry) = entries.iter().find(|entry| entry.id == id) else {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: no harness named \"{id}\": /huddle:setup lists them"
        );
        return 2;
    };
    let supported: Vec<&str> = entry
        .run
        .accepted_access()
        .iter()
        .map(|level| level.as_str())
        .collect();
    if !supported.contains(&access_word(access)) {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: {id} cannot run with {} access: it supports {}",
            access_word(access),
            supported.join(", ")
        );
        return 2;
    }
    let report = check_entry(entry, env, None);
    // The run gates on the binary alone: a version probe can fail on a harness that still
    // works a turn, and the attempt then carries the harness's own error.
    let failed = report
        .checks
        .iter()
        .find(|line| !line.ok && line.name == "binary");
    if let Some(line) = failed {
        let _ = writeln!(
            std::io::stderr(),
            "huddle: {id} is not ready: {}: /huddle:setup {id} shows what to fix",
            line.detail
        );
        return 6;
    }
    let job_id = match new_job_id(now_ms()) {
        Ok(job_id) => job_id,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let cwd = match std::env::current_dir() {
        Ok(cwd) => cwd,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let input = SubmitInput {
        id: job_id,
        harness: id,
        task,
        access,
        effort: parsed.value("effort").map(str::to_string),
        cwd,
    };
    let (store, job) = match driver::submit(&paths, &input) {
        Ok(pair) => pair,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            return 1;
        }
    };
    let job_checks = Checks {
        setup: parsed.all("setup"),
        gates: parsed.all("gate"),
        scope: parsed.all("scope"),
        forbid: parsed.all("forbid"),
    };
    if !job_checks.is_empty()
        && let Err(error) = checks::write_checks(&store.job_dir(&job.id), &job_checks)
    {
        let _ = writeln!(std::io::stderr(), "huddle: {error}");
        return 1;
    }
    let _ = writeln!(
        std::io::stdout(),
        "huddle job {} started: {} ({}, {})",
        job.id,
        job.title,
        job.harness,
        access_word(job.access)
    );
    let exe = match std::env::current_exe() {
        Ok(exe) => exe,
        Err(error) => return cannot_start(&job.id, &error.to_string()),
    };
    if let Err(error) = driver::spawn_driver(&exe, &store.job_dir(&job.id), env) {
        return cannot_start(&job.id, &format!("{error:?}"));
    }
    if parsed.has("background") {
        let _ = writeln!(
            std::io::stdout(),
            "Running detached: /huddle:status shows it, /huddle:result {} once it awaits review.",
            job.id
        );
        return 0;
    }
    wait_and_report(&store, &job.id)
}

/// Drives one job whose directory the caller names: the detached driver's own path. A missing
/// job directory or a job in another state is a usage exit; the driver prints nothing here.
fn drive_job(args: &[String], env: &Env) -> i32 {
    let Some(dir) = args.get(1) else {
        return 2;
    };
    let path = PathBuf::from(dir);
    let Some((store, job_id)) = JobStore::for_job_dir(&path) else {
        return 2;
    };
    if !path.is_dir() {
        return 2;
    }
    match driver::drive(&store, &job_id, env) {
        Ok(_) => 0,
        Err(DriveError::WrongState { .. }) => 2,
        Err(error) => {
            let _ = writeln!(std::io::stderr(), "huddle: {error}");
            1
        }
    }
}

/// Sleeps and polls until the job settles, sweeping each round so a dead driver fails its
/// job, then prints the result packet. The exit carries the state: 0 for a review or a clean
/// completion, 3 otherwise.
fn wait_and_report(store: &JobStore, job_id: &str) -> i32 {
    let mut delay = Duration::from_millis(FIRST_POLL_MS);
    loop {
        thread::sleep(delay);
        delay = (delay * 2).min(Duration::from_millis(MAX_POLL_MS));
        let _ = liveness::sweep(store, now_ms());
        let Ok(job) = store.get(job_id) else {
            continue;
        };
        if job.state.is_active() {
            continue;
        }
        match review_view(&job, false) {
            Ok(view) => {
                let _ = writeln!(std::io::stdout(), "{}", render::result_packet(&job, &view));
                let verification = job
                    .last_attempt()
                    .map(|attempt| checks::read_verification(&store.job_dir(job_id), attempt.n));
                if let Some(Ok(Some(v))) = verification {
                    let _ = writeln!(std::io::stdout());
                    let _ = write!(
                        std::io::stdout(),
                        "{}",
                        checks_render::verification_section(&v)
                    );
                }
            }
            Err(error) => {
                let _ = writeln!(std::io::stderr(), "huddle: {error}");
            }
        }
        return match job.state {
            JobState::AwaitingReview | JobState::Completed => 0,
            _ => 3,
        };
    }
}

/// Changes and stat of a job's worktree when it still exists and the job awaits review or
/// failed; with_diff adds the capped patch. Otherwise an empty ChangeSet and no diff.
pub(crate) fn review_view(job: &Job, with_diff: bool) -> Result<ReviewView, GitError> {
    let mut view = ReviewView {
        changes: ChangeSet::default(),
        diff_stat: None,
        diff: None,
    };
    let open = job.state == JobState::AwaitingReview || job.state == JobState::Failed;
    let worktree = Path::new(&job.worktree);
    if !open || !worktree.exists() {
        return Ok(view);
    }
    view.changes = git::changes(worktree, &job.base_sha)?;
    let stat = git::diff(worktree, &job.base_sha, DiffMode::Stat)?;
    view.diff_stat = Some(stat);
    if with_diff {
        let patch = git::diff(worktree, &job.base_sha, DiffMode::Patch)?;
        view.diff = Some(render::cap_diff(&patch, render::DIFF_CAP_CHARS));
    }
    Ok(view)
}

/// One parsed command line, for the words of this crate.
pub(crate) struct Parsed {
    pub positionals: Vec<String>,
    flags: Vec<String>,
    values: Vec<(String, String)>,
}

impl Parsed {
    pub(crate) fn has(&self, name: &str) -> bool {
        self.flags.iter().any(|flag| flag == name)
    }

    pub(crate) fn value(&self, name: &str) -> Option<&str> {
        self.values
            .iter()
            .rev()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    }

    /// Every value of `name`, in the order the command line gave them.
    pub(crate) fn all(&self, name: &str) -> Vec<String> {
        self.values
            .iter()
            .filter(|(key, _)| key == name)
            .map(|(_, value)| value.clone())
            .collect()
    }
}

/// Long options only. Every token before `--` that starts with `--` splits once on `=`: a
/// value option takes the inline value or the next token whatever it is, a bool option takes
/// none, and any other name is unknown. `--` ends options; a lone `-`, an empty string and
/// everything after `--` are positionals, in order; any other dash token is an unknown option
/// named by its first character.
pub(crate) fn parse_args(
    args: &[String],
    bool_flags: &[&str],
    value_flags: &[&str],
) -> Result<Parsed, String> {
    let mut parsed = Parsed {
        positionals: Vec::new(),
        flags: Vec::new(),
        values: Vec::new(),
    };
    let mut options = true;
    let mut index = 0;
    while index < args.len() {
        let token = args[index].as_str();
        if options && token == "--" {
            options = false;
        } else if options && token.starts_with("--") {
            let (raw_name, inline) = match token.split_once('=') {
                Some((name, value)) => (name, Some(value.to_string())),
                None => (token, None),
            };
            let name = raw_name.strip_prefix("--").unwrap_or(raw_name);
            if value_flags.contains(&name) {
                match inline {
                    Some(value) => parsed.values.push((name.to_string(), value)),
                    None => {
                        let Some(value) = args.get(index + 1) else {
                            return Err(format!("Option '--{name} <value>' argument missing"));
                        };
                        parsed.values.push((name.to_string(), value.clone()));
                        index += 1;
                    }
                }
            } else if bool_flags.contains(&name) {
                if inline.is_some() {
                    return Err(format!("Option '--{name}' does not take an argument"));
                }
                parsed.flags.push(name.to_string());
            } else {
                return Err(format!("Unknown option '--{name}'"));
            }
        } else if options && token.starts_with('-') && token.len() > 1 {
            let first = token[1..].chars().next().unwrap_or('-');
            return Err(format!("Unknown option '-{first}'"));
        } else {
            parsed.positionals.push(token.to_string());
        }
        index += 1;
    }
    Ok(parsed)
}

/// The access in words, as the started line and the messages read it.
fn access_word(access: Access) -> &'static str {
    match access {
        Access::Readonly => "readonly",
        Access::Exec => "exec",
        Access::Write => "write",
    }
}

/// The one line a queued job whose driver never started gets.
fn cannot_start(id: &str, error: &str) -> i32 {
    let _ = writeln!(
        std::io::stderr(),
        "huddle: job {id} is queued, but its driver could not start: {error}"
    );
    1
}
