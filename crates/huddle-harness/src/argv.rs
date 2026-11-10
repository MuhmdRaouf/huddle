//! One launch: the argv in the table's fixed order, the access env, the stdin text, the prompt
//! file and the RPC prompt, built from an `Entry` and a `Brief`.

use std::fmt;
use std::path::{Path, PathBuf};

use crate::table::{Brief, Entry, SessionOwner, SessionRef, Transport};

/// Everything a runner needs to start one harness process.
#[derive(Clone, Debug, PartialEq)]
pub struct Launch {
    /// Every argument after the resolved binary and its prefix args.
    pub args: Vec<String>,
    /// Text for the child's stdin (Transport::Stdin), closed after it.
    pub stdin: Option<String>,
    /// Added to the child env (the access env).
    pub env: Vec<(String, String)>,
    /// Transport::File: where the prompt goes and the text to write there (0600).
    pub prompt_file: Option<(PathBuf, String)>,
    /// Transport::Rpc: the first prompt, sent over the protocol.
    pub rpc_prompt: Option<String>,
}

/// Why an `Entry` and a `Brief` cannot launch together.
#[derive(Clone, Debug, PartialEq)]
pub enum ArgvError {
    AccessUnsupported {
        id: String,
        access: String,
        supported: Vec<String>,
    },
    EffortUnsupported {
        id: String,
        effort: String,
        accepted: Vec<String>,
    },
    NoEffortSetting {
        id: String,
        effort: String,
    },
    ResumeUnsupported {
        id: String,
    },
}

impl fmt::Display for ArgvError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ArgvError::AccessUnsupported {
                id,
                access,
                supported,
            } => {
                write!(
                    f,
                    "{id} cannot run with {access} access: it supports {}",
                    supported.join(", ")
                )
            }
            ArgvError::EffortUnsupported {
                id,
                effort,
                accepted,
            } => {
                write!(
                    f,
                    "{id} does not take effort {effort}: it accepts {}",
                    accepted.join(", ")
                )
            }
            ArgvError::NoEffortSetting { id, effort } => {
                write!(f, "{id} takes no effort setting (asked for {effort})")
            }
            ArgvError::ResumeUnsupported { id } => {
                write!(f, "{id} cannot resume a session")
            }
        }
    }
}

impl std::error::Error for ArgvError {}

/// The report contract every runner appends to its prompt: the three keys the report carries and
/// the schema it must satisfy.
pub const REPORT_SCHEMA_JSON: &str = r#"{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"summary":{"type":"string"},"files":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"why":{"type":"string"}},"required":["path","why"],"additionalProperties":false}},"open_items":{"type":"array","items":{"type":"string"}}},"required":["summary","files","open_items"],"additionalProperties":false}"#;

/// The text between the task and the schema: the rules the final report follows.
const REPORT_CONTRACT: &str = "\n\n## Final report\n\nWork unattended. Make reasonable task decisions within the brief; record blockers in open_items instead of asking for input.\nYour final message must contain only the report as one JSON object, and nothing else.\nThe report must satisfy this JSON schema:\n\n```json\n";

/// The prompt the runner hands the harness: the task, then the report contract and the schema,
/// indented the way the schema was written.
pub fn full_prompt(prompt: &str) -> String {
    format!("{prompt}{REPORT_CONTRACT}{}\n```", report_schema_pretty())
}

/// The schema at a two-space indent; the compact form stands in if the pretty render ever fails.
fn report_schema_pretty() -> String {
    serde_json::from_str::<serde_json::Value>(REPORT_SCHEMA_JSON)
        .and_then(|schema| serde_json::to_string_pretty(&schema))
        .unwrap_or_else(|_| REPORT_SCHEMA_JSON.to_string())
}

/// Builds one launch from the table's fixed order: cwd flag, run args, effort, access,
/// after-access args, session args, then the prompt on its transport.
pub fn build(
    entry: &Entry,
    brief: &Brief,
    cwd: &Path,
    log_dir: &Path,
) -> Result<Launch, ArgvError> {
    let run = &entry.run;
    let mut args: Vec<String> = Vec::new();
    if let Some(flag) = &run.cwd_flag {
        args.push(flag.clone());
        args.push(cwd.display().to_string());
    }
    args.extend(run.args.iter().cloned());

    if let Some(effort) = &brief.effort {
        let flag = run
            .effort
            .as_ref()
            .ok_or_else(|| ArgvError::NoEffortSetting {
                id: entry.id.clone(),
                effort: effort.clone(),
            })?;
        if !flag.values.is_empty() && !flag.values.iter().any(|accepted| accepted == effort) {
            return Err(ArgvError::EffortUnsupported {
                id: entry.id.clone(),
                effort: effort.clone(),
                accepted: flag.values.clone(),
            });
        }
        args.extend(flag.args.iter().map(|arg| arg.replace("{value}", effort)));
    }

    let access = run
        .access
        .get(&brief.access)
        .ok_or_else(|| ArgvError::AccessUnsupported {
            id: entry.id.clone(),
            access: brief.access.as_str().to_string(),
            supported: run
                .accepted_access()
                .iter()
                .map(|level| level.as_str().to_string())
                .collect(),
        })?;
    args.extend(access.args.iter().cloned());
    let env = access.env.clone();
    args.extend(run.after_access.iter().cloned());

    match &brief.session {
        SessionRef::New { key } => {
            if run.session.owner == SessionOwner::Caller {
                args.extend(run.session.new.iter().map(|arg| arg.replace("{id}", key)));
            }
        }
        SessionRef::Resume { id } => {
            if run.session.resume.is_empty() {
                return Err(ArgvError::ResumeUnsupported {
                    id: entry.id.clone(),
                });
            }
            args.extend(run.session.resume.iter().map(|arg| arg.replace("{id}", id)));
        }
    }

    let text = full_prompt(&brief.prompt);
    let mut stdin = None;
    let mut prompt_file = None;
    let mut rpc_prompt = None;
    match run.prompt.transport {
        Transport::Argv => {
            if let Some(flag) = &run.prompt.flag {
                args.push(flag.clone());
            }
            args.push(text);
        }
        Transport::Stdin => stdin = Some(text),
        Transport::File => {
            let path = log_dir.join("prompt.md");
            if let Some(flag) = &run.prompt.flag {
                args.push(flag.clone());
            }
            args.push(path.display().to_string());
            if let Some((flag, fixed)) = &run.prompt.argv_text {
                args.push(flag.clone());
                args.push(fixed.clone());
            }
            prompt_file = Some((path, text));
        }
        Transport::Rpc => rpc_prompt = Some(text),
    }

    Ok(Launch {
        args,
        stdin,
        env,
        prompt_file,
        rpc_prompt,
    })
}
