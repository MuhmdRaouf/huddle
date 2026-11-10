//! One release tool: the changelog notes, one section, the binary size check.

mod notes;
mod size;

use std::ffi::{OsStr, OsString};
use std::fmt;
use std::io::{self, Write};
use std::path::Path;
use std::process::ExitCode;

const USAGE: &str = "usage: huddle-release notes <version> <changelog> <sums> <commit> | section <version> <changelog> | size <binary>...";

/// How a word's failure becomes an exit code.
trait Failure: fmt::Display {
    fn exit_code(&self) -> u8;
}

impl Failure for notes::Error {
    fn exit_code(&self) -> u8 {
        match self {
            notes::Error::Usage(_) => 2,
            notes::Error::Read(_) | notes::Error::MissingSection(_) => 1,
        }
    }
}

impl Failure for size::Error {
    fn exit_code(&self) -> u8 {
        match self {
            size::Error::Usage | size::Error::CannotRead(_) => 2,
            size::Error::CurrentDir
            | size::Error::LimitMissing(_)
            | size::Error::LimitRead(_)
            | size::Error::LimitParse(_) => 1,
        }
    }
}

fn main() -> ExitCode {
    run(std::env::args_os().skip(1).collect())
}

fn run(args: Vec<OsString>) -> ExitCode {
    let Some((word, rest)) = args.split_first() else {
        return top_usage();
    };
    match word.to_str() {
        Some("notes") => run_notes(rest),
        Some("section") => run_section(rest),
        Some("size") => run_size(rest),
        _ => top_usage(),
    }
}

fn top_usage() -> ExitCode {
    say(USAGE);
    ExitCode::from(2)
}

fn fail(error: &impl Failure) -> ExitCode {
    say(&error.to_string());
    ExitCode::from(error.exit_code())
}

fn say(line: &str) {
    let mut err = io::stderr().lock();
    let _ = writeln!(err, "{line}");
}

fn print(text: &str) -> ExitCode {
    let mut out = io::stdout().lock();
    if write!(out, "{text}").is_err() {
        return cannot_write();
    }
    ExitCode::SUCCESS
}

fn cannot_write() -> ExitCode {
    let mut err = io::stderr().lock();
    let _ = writeln!(err, "huddle-release: cannot write to standard output");
    ExitCode::from(1)
}

fn run_notes(rest: &[OsString]) -> ExitCode {
    let Some((version, changelog, sums, commit)) = notes_args(rest) else {
        return fail(&notes::Error::Usage(notes::NOTES_USAGE));
    };
    match notes::notes(version, Path::new(changelog), Path::new(sums), commit) {
        Ok(text) => print(&text),
        Err(error) => fail(&error),
    }
}

fn notes_args(rest: &[OsString]) -> Option<(&str, &OsStr, &OsStr, &str)> {
    let [version, changelog, sums, commit] = rest else {
        return None;
    };
    let version = version.to_str()?;
    let commit = commit.to_str()?;
    if version.is_empty() || commit.is_empty() {
        return None;
    }
    Some((version, changelog, sums, commit))
}

fn run_section(rest: &[OsString]) -> ExitCode {
    let Some((version, changelog)) = section_args(rest) else {
        return fail(&notes::Error::Usage(notes::SECTION_USAGE));
    };
    match notes::section(version, Path::new(changelog)) {
        Ok(text) => print(&text),
        Err(error) => fail(&error),
    }
}

fn section_args(rest: &[OsString]) -> Option<(&str, &OsStr)> {
    let [version, changelog] = rest else {
        return None;
    };
    let version = version.to_str()?;
    if version.is_empty() {
        return None;
    }
    Some((version, changelog))
}

fn run_size(rest: &[OsString]) -> ExitCode {
    let binaries: Vec<&OsStr> = rest.iter().map(OsString::as_os_str).collect();
    let (limit, checked) = match size::check(&binaries) {
        Ok(found) => found,
        Err(error) => return fail(&error),
    };
    let mut status = 0;
    let mut out = io::stdout().lock();
    for binary in checked {
        if binary.over {
            status = 1;
            say(&format!(
                "huddle-release: {} is {} bytes, over the limit of {limit}",
                binary.path.display(),
                binary.size
            ));
        } else if writeln!(
            out,
            "huddle-release: {} is {} bytes (limit {limit})",
            binary.path.display(),
            binary.size
        )
        .is_err()
        {
            return cannot_write();
        }
    }
    ExitCode::from(status)
}
