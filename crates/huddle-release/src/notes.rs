//! The changelog words: release notes and one section.

use std::fmt;
use std::fs;
use std::path::{Path, PathBuf};

const FENCE: &str = "```";
const HEADING: &str = "## ";

pub const NOTES_USAGE: &str = "usage: huddle-release notes <version> <changelog> <sums> <commit>";
pub const SECTION_USAGE: &str = "usage: huddle-release section <version> <changelog>";

/// Every expected failure of the changelog words.
pub enum Error {
    Usage(&'static str),
    Read(PathBuf),
    MissingSection(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Usage(line) => f.write_str(line),
            Error::Read(path) => write!(f, "huddle-release: cannot read {}", path.display()),
            Error::MissingSection(version) => {
                write!(f, "huddle-release: no CHANGELOG.md section for {version}")
            }
        }
    }
}

/// The release notes: the version's section with wrapped prose lines joined, the SHA256SUMS file
/// in a code block and the commit.
pub fn notes(version: &str, changelog: &Path, sums: &Path, commit: &str) -> Result<String, Error> {
    let text = read(changelog)?;
    let sums_text = read(sums)?;
    let lines = extract(version, &text).ok_or_else(|| missing(version))?;
    Ok(render(
        &unwrap_lines(&lines),
        sums_text.trim_end_matches('\n'),
        commit,
    ))
}

/// One section alone, for a tag message: the raw lines between its heading and the next one,
/// outer blank lines trimmed.
pub fn section(version: &str, changelog: &Path) -> Result<String, Error> {
    let text = read(changelog)?;
    let lines = extract(version, &text).ok_or_else(|| missing(version))?;
    let mut out = String::new();
    for line in lines {
        out.push_str(&line);
        out.push('\n');
    }
    Ok(out)
}

fn read(path: &Path) -> Result<String, Error> {
    fs::read_to_string(path).map_err(|_| Error::Read(path.to_path_buf()))
}

fn missing(version: &str) -> Error {
    Error::MissingSection(version.to_string())
}

/// The version's raw section lines: from after its `## <version>` heading to the next `## `
/// heading, fenced blocks tracked, outer blank lines trimmed. A missing or empty section is
/// `None`.
fn extract(version: &str, text: &str) -> Option<Vec<String>> {
    let mut code = false;
    let mut lines: Option<Vec<String>> = None;
    for line in text.lines() {
        let trimmed = line.trim();
        match lines.as_mut() {
            None => {
                if trimmed.starts_with(FENCE) {
                    code = !code;
                } else if !code
                    && let Some(rest) = trimmed.strip_prefix(HEADING)
                    && first_word(rest) == version
                {
                    code = false;
                    lines = Some(Vec::new());
                }
            }
            Some(collected) => {
                if trimmed.starts_with(FENCE) {
                    code = !code;
                } else if !code && trimmed.starts_with(HEADING) {
                    break;
                }
                collected.push(line.to_string());
            }
        }
    }
    trim_blanks(lines?)
}

/// The first space- or tab-separated token. A leading separator yields an empty token, so a
/// heading written as `##  1.2.3` never matches the version `1.2.3`.
fn first_word(rest: &str) -> &str {
    rest.split([' ', '\t']).next().unwrap_or_default()
}

fn trim_blanks(mut lines: Vec<String>) -> Option<Vec<String>> {
    while lines.first().is_some_and(|l| l.trim().is_empty()) {
        lines.remove(0);
    }
    while lines.last().is_some_and(|l| l.trim().is_empty()) {
        lines.pop();
    }
    if lines.is_empty() { None } else { Some(lines) }
}

/// Joins wrapped prose lines onto their predecessor. Headings, bullets, blank lines and fenced
/// blocks keep their own lines, and nothing joins onto them.
fn unwrap_lines(lines: &[String]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut code = false;
    for line in lines {
        let trimmed = line.trim();
        if trimmed.starts_with(FENCE) {
            code = !code;
            out.push(line.clone());
        } else if code {
            out.push(line.clone());
        } else if trimmed.is_empty() {
            out.push(String::new());
        } else if trimmed.starts_with("- ") || trimmed.starts_with('#') {
            out.push(trimmed.to_string());
        } else {
            match out.last_mut() {
                Some(prev)
                    if !prev.is_empty() && !prev.starts_with('#') && !prev.starts_with(FENCE) =>
                {
                    prev.push(' ');
                    prev.push_str(trimmed);
                }
                _ => out.push(trimmed.to_string()),
            }
        }
    }
    out
}

/// The notes payload: the section, the checksums in a code block and the commit line.
fn render(section_lines: &[String], sums: &str, commit: &str) -> String {
    let mut out = String::new();
    for line in section_lines {
        out.push_str(line);
        out.push('\n');
    }
    out.push_str("\n### Checksums\n\n```\n");
    out.push_str(sums);
    out.push_str("\n```\nBuilt from ");
    out.push_str(commit);
    out.push_str(".\n");
    out
}
