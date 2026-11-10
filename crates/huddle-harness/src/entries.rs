//! The builtin harness table: the spec's 22 target harnesses as `Entry` values.
//!
//! Five entries whose CLIs huddle checked (claude, omp, opencode, pi, zcode) carry `verified:
//! true`. The other 17 carry the flag spellings of paradev's per-agent table, `verified: false`,
//! until a smoke run checks them: every one of them is text-wire — stdout is the answer, no
//! session id, so resume args would be dead data.

use std::collections::BTreeMap;
use std::time::Duration;

use crate::table::{
    Access, AccessSpec, Binary, Caps, Check, Entry, FixedPath, Flag, Launcher, Prompt, ReportKind,
    Run, Session, SessionOwner, Transport, Wire,
};

/// One mebibyte: the default stdout line cap.
const MIB: u64 = 1_048_576;

/// The 22 spec targets, in the spec's order.
pub fn builtin() -> Vec<Entry> {
    vec![
        text_entry(TextRow {
            id: "amp",
            label: "Amp Code",
            bin: "amp",
            args: &["-x"],
            prompt: stdin_prompt(),
            write: &[],
            readonly: None,
            effort: Some(&["--effort", "{value}"]),
        }),
        text_entry(TextRow {
            id: "agy",
            label: "Antigravity CLI",
            bin: "agy",
            args: &[],
            prompt: argv_prompt("-p"),
            write: &["--mode", "accept-edits"],
            readonly: None,
            effort: Some(&["--effort", "{value}"]),
        }),
        claude(),
        text_entry(TextRow {
            id: "codex",
            label: "Codex CLI",
            bin: "codex",
            args: &["exec", "--skip-git-repo-check"],
            prompt: argv_plain(),
            write: &[
                "--dangerously-bypass-approvals-and-sandbox",
                "--dangerously-bypass-hook-trust",
            ],
            // codex exec is sandboxed read-only by default: readonly exists with no extra args.
            readonly: Some(&[]),
            effort: Some(&["-c", "model_reasoning_effort={value}"]),
        }),
        text_entry(TextRow {
            id: "cursor-agent",
            label: "Cursor Agent",
            bin: "cursor-agent",
            args: &["--trust"],
            prompt: argv_prompt("-p"),
            write: &[],
            readonly: Some(&["--mode", "ask"]),
            effort: None,
        }),
        text_entry(TextRow {
            id: "droid",
            label: "Droid",
            bin: "droid",
            args: &["exec"],
            prompt: argv_plain(),
            write: &[],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "fx",
            label: "fx",
            bin: "fx",
            args: &["ask", "--auto"],
            prompt: argv_plain(),
            write: &[],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "gemini",
            label: "Gemini CLI",
            bin: "gemini",
            args: &["--skip-trust"],
            prompt: argv_prompt("-p"),
            write: &["--approval-mode=auto_edit"],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "copilot",
            label: "GitHub Copilot",
            bin: "copilot",
            args: &[],
            prompt: argv_prompt("-p"),
            write: &["--allow-tool=write"],
            readonly: None,
            effort: Some(&["--effort", "{value}"]),
        }),
        text_entry(TextRow {
            id: "grok",
            label: "Grok",
            bin: "grok",
            args: &[],
            prompt: argv_prompt("-p"),
            write: &["--always-approve"],
            readonly: Some(&["--permission-mode", "plan"]),
            effort: None,
        }),
        text_entry(TextRow {
            id: "hermes",
            label: "Hermes",
            bin: "hermes",
            args: &["chat"],
            prompt: argv_prompt("-q"),
            write: &["--yolo"],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "muse",
            label: "Muse Code",
            bin: "muse",
            args: &["exec"],
            prompt: argv_plain(),
            write: &[],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "devin",
            label: "Devin",
            bin: "devin",
            args: &["--respect-workspace-trust", "false"],
            prompt: argv_prompt("-p"),
            write: &["--permission-mode", "dangerous"],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "kimi",
            label: "Kimi Code",
            bin: "kimi",
            args: &[],
            prompt: argv_prompt("-p"),
            write: &["--auto"],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "kiro",
            label: "Kiro",
            bin: "kiro-cli",
            args: &["chat", "--no-interactive"],
            prompt: argv_plain(),
            write: &["--trust-all-tools"],
            readonly: None,
            effort: None,
        }),
        text_entry(TextRow {
            id: "mastracode",
            label: "Mastra Code",
            bin: "mastracode",
            args: &[],
            prompt: argv_prompt("--prompt"),
            write: &[],
            readonly: Some(&["--mode", "plan"]),
            effort: Some(&["--thinking-level", "{value}"]),
        }),
        text_entry(TextRow {
            id: "vibe",
            label: "Mistral Vibe",
            bin: "vibe",
            args: &["--trust"],
            prompt: argv_prompt("-p"),
            write: &["--auto-approve"],
            readonly: Some(&["--agent", "plan"]),
            effort: None,
        }),
        omp(),
        opencode(),
        pi(),
        text_entry(TextRow {
            id: "polygraph",
            label: "Polygraph",
            bin: "polygraph",
            args: &["session", "start", "--"],
            prompt: argv_plain(),
            write: &[],
            readonly: None,
            effort: None,
        }),
        zcode(),
    ]
}

/// The builtin entry with this id, if there is one.
pub fn find(id: &str) -> Option<Entry> {
    builtin().into_iter().find(|e| e.id == id)
}

/// The check block of a verified entry: the CLI ran and answered.
fn checked(id: &str, label: &str, name: &str) -> Check {
    Check {
        version: vec!["--version".to_string()],
        install_hint: format!("Install {label} and make sure `{name}` is on PATH."),
        login_hint: format!("Run `{name}` once and log in, then run /huddle:setup {id} again."),
        verified: true,
    }
}

fn strings(items: &[&str]) -> Vec<String> {
    items.iter().map(|item| item.to_string()).collect()
}

fn pairs(items: &[(&str, &str)]) -> Vec<(String, String)> {
    items
        .iter()
        .map(|(name, value)| (name.to_string(), value.to_string()))
        .collect()
}

/// An access level with these args and no env.
fn spec(args: &[&str]) -> AccessSpec {
    AccessSpec {
        args: strings(args),
        env: vec![],
    }
}

/// An access level with these args, set through env values.
fn spec_env(args: &[&str], env: &[(&str, &str)]) -> AccessSpec {
    AccessSpec {
        args: strings(args),
        env: pairs(env),
    }
}

/// Argv transport: the prompt follows this flag.
fn argv_prompt(flag: &str) -> Prompt {
    Prompt {
        transport: Transport::Argv,
        flag: Some(flag.to_string()),
        argv_text: None,
    }
}

/// Argv transport with no flag: the prompt is a bare positional argument.
fn argv_plain() -> Prompt {
    Prompt {
        transport: Transport::Argv,
        flag: None,
        argv_text: None,
    }
}

/// Stdin transport: the prompt is written to the process's stdin.
fn stdin_prompt() -> Prompt {
    Prompt {
        transport: Transport::Stdin,
        flag: None,
        argv_text: None,
    }
}

/// One paradev-table row with a text wire: stdout is the answer, no session.
struct TextRow {
    id: &'static str,
    label: &'static str,
    bin: &'static str,
    args: &'static [&'static str],
    prompt: Prompt,
    /// AccessSpec args for Access::Write.
    write: &'static [&'static str],
    /// Some: AccessSpec args for Access::Readonly; None: the row refuses readonly.
    readonly: Option<&'static [&'static str]>,
    /// Effort flag args; values stay empty, so any value passes through.
    effort: Option<&'static [&'static str]>,
}

fn text_entry(row: TextRow) -> Entry {
    let mut access = BTreeMap::from([(Access::Write, spec(row.write))]);
    if let Some(readonly) = row.readonly {
        access.insert(Access::Readonly, spec(readonly));
    }
    Entry {
        id: row.id.to_string(),
        label: row.label.to_string(),
        binary: Binary {
            names: vec![row.bin.to_string()],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: Check {
            version: vec!["--version".to_string()],
            install_hint: format!(
                "Install {} and make sure `{}` is on PATH.",
                row.label, row.bin
            ),
            login_hint: format!("Run `{}` once and log in.", row.bin),
            verified: false,
        },
        run: Run {
            args: strings(row.args),
            cwd_flag: None,
            prompt: row.prompt,
            effort: row.effort.map(|args| Flag {
                args: strings(args),
                values: vec![],
            }),
            access,
            after_access: vec![],
            session: Session {
                owner: SessionOwner::None,
                new: vec![],
                resume: vec![],
                missing: vec![],
            },
            line_cap: MIB,
            reap_group: true,
        },
        wire: Wire::Text,
        caps: Caps {
            report: ReportKind::FreeText,
            notes: vec![],
        },
    }
}

/// The user's own Claude Code on its `-p` stream-json output.
fn claude() -> Entry {
    Entry {
        id: "claude".to_string(),
        label: "Claude Code".to_string(),
        binary: Binary {
            names: vec!["claude".to_string()],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: checked("claude", "Claude Code", "claude"),
        run: Run {
            args: strings(&["-p", "--output-format", "stream-json", "--verbose"]),
            cwd_flag: None,
            prompt: stdin_prompt(),
            effort: Some(Flag {
                args: strings(&["--effort", "{value}"]),
                values: strings(&["low", "high", "max"]),
            }),
            access: BTreeMap::from([
                (Access::Readonly, spec(&["--permission-mode", "plan"])),
                (
                    Access::Exec,
                    spec(&["--permission-mode", "bypassPermissions"]),
                ),
                (
                    Access::Write,
                    spec(&["--permission-mode", "bypassPermissions"]),
                ),
            ]),
            after_access: vec![],
            session: Session {
                owner: SessionOwner::Caller,
                new: strings(&["--session-id", "{id}"]),
                resume: strings(&["--resume", "{id}"]),
                missing: vec![strings(&["no conversation found"])],
            },
            line_cap: u64::MAX,
            reap_group: true,
        },
        wire: Wire::StreamJson,
        caps: Caps {
            report: ReportKind::Extracted,
            notes: vec![],
        },
    }
}

/// Oh My Pi over its RPC mode; access narrows the `--tools` allowlist.
fn omp() -> Entry {
    Entry {
        id: "omp".to_string(),
        label: "Oh My Pi".to_string(),
        binary: Binary {
            names: vec!["omp".to_string()],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: checked("omp", "Oh My Pi", "omp"),
        run: Run {
            args: strings(&["--mode", "rpc", "--no-ui"]),
            cwd_flag: None,
            prompt: Prompt {
                transport: Transport::Rpc,
                flag: None,
                argv_text: None,
            },
            effort: Some(Flag {
                args: strings(&["--thinking", "{value}"]),
                values: strings(&["low", "high", "max"]),
            }),
            access: BTreeMap::from([
                (Access::Readonly, spec(&["--tools", "read,grep,glob"])),
                (Access::Exec, spec(&["--tools", "read,grep,glob,bash"])),
                (
                    Access::Write,
                    spec(&["--tools", "read,grep,glob,edit,write,bash,todo"]),
                ),
            ]),
            after_access: strings(&["--approval-mode", "yolo", "--no-title"]),
            session: Session {
                owner: SessionOwner::Engine,
                new: vec![],
                resume: strings(&["--resume", "{id}"]),
                missing: vec![strings(&["session \"", "\" not found"])],
            },
            line_cap: MIB,
            reap_group: false,
        },
        wire: Wire::OmpRpc {
            handshake: Duration::from_millis(60_000),
        },
        caps: Caps {
            report: ReportKind::Checked,
            notes: strings(&[
                "omp has no readonly mode; the tool allowlist is narrowed to read,grep,glob instead.",
            ]),
        },
    }
}

/// OpenCode in one-shot JSON mode; access goes through the OPENCODE_CONFIG_CONTENT env value.
fn opencode() -> Entry {
    Entry {
        id: "opencode".to_string(),
        label: "OpenCode".to_string(),
        binary: Binary {
            names: vec!["opencode".to_string()],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: checked("opencode", "OpenCode", "opencode"),
        run: Run {
            args: strings(&["run", "--format", "json"]),
            cwd_flag: None,
            prompt: argv_plain(),
            effort: Some(Flag {
                args: strings(&["--variant", "{value}"]),
                values: strings(&["low", "high", "max"]),
            }),
            access: BTreeMap::from([
                (
                    Access::Readonly,
                    spec_env(
                        &[],
                        &[(
                            "OPENCODE_CONFIG_CONTENT",
                            r#"{"permission":{"edit":"deny","bash":"deny","question":"deny"}}"#,
                        )],
                    ),
                ),
                (
                    Access::Exec,
                    spec_env(
                        &[],
                        &[(
                            "OPENCODE_CONFIG_CONTENT",
                            r#"{"permission":{"edit":"deny","bash":"allow","question":"deny"}}"#,
                        )],
                    ),
                ),
                (
                    Access::Write,
                    spec_env(
                        &[],
                        &[(
                            "OPENCODE_CONFIG_CONTENT",
                            r#"{"permission":{"edit":"allow","bash":"allow","question":"deny"}}"#,
                        )],
                    ),
                ),
            ]),
            after_access: strings(&["--auto"]),
            session: Session {
                owner: SessionOwner::Engine,
                new: vec![],
                resume: strings(&["--session", "{id}"]),
                missing: vec![strings(&["session not found"])],
            },
            line_cap: MIB,
            reap_group: true,
        },
        wire: Wire::Jsonl,
        caps: Caps {
            report: ReportKind::Extracted,
            notes: strings(&[
                "Access is set through OPENCODE_CONFIG_CONTENT, merged over your own config.",
            ]),
        },
    }
}

/// Pi over its RPC mode; the caller owns the session id, and write needs no tool flags.
fn pi() -> Entry {
    Entry {
        id: "pi".to_string(),
        label: "Pi".to_string(),
        binary: Binary {
            names: vec!["pi".to_string()],
            env_var: None,
            fixed: vec![],
            launcher: Launcher::None,
            launch_env: vec![],
        },
        check: checked("pi", "Pi", "pi"),
        run: Run {
            args: strings(&["--mode", "rpc"]),
            cwd_flag: None,
            prompt: Prompt {
                transport: Transport::Rpc,
                flag: None,
                argv_text: None,
            },
            effort: Some(Flag {
                args: strings(&["--thinking", "{value}"]),
                values: strings(&["off", "minimal", "low", "medium", "high", "xhigh", "max"]),
            }),
            access: BTreeMap::from([
                (
                    Access::Readonly,
                    spec(&["--tools", "read,grep,find,ls", "--no-mcp"]),
                ),
                (
                    Access::Exec,
                    spec(&["--tools", "read,grep,find,ls,bash", "--no-mcp"]),
                ),
                (Access::Write, spec(&[])),
            ]),
            after_access: vec![],
            session: Session {
                owner: SessionOwner::Caller,
                new: strings(&["--session-id", "{id}"]),
                resume: strings(&["--session-id", "{id}"]),
                missing: vec![],
            },
            line_cap: 64 * MIB,
            reap_group: false,
        },
        wire: Wire::PiRpc {
            handshake: Duration::from_millis(60_000),
        },
        caps: Caps {
            report: ReportKind::Checked,
            notes: strings(&[
                "pi has no readonly mode; the tool allowlist is narrowed to read,grep,find,ls instead.",
            ]),
        },
    }
}

/// ZCode, the desktop app's bundled CLI, run as plain node; one JSON document comes back.
fn zcode() -> Entry {
    Entry {
        id: "zcode".to_string(),
        label: "ZCode".to_string(),
        binary: Binary {
            names: vec!["zcode".to_string()],
            env_var: Some("ZCODE_CLI".to_string()),
            fixed: vec![
                FixedPath {
                    os: "macos".to_string(),
                    path: "/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs".to_string(),
                },
                FixedPath {
                    os: "macos".to_string(),
                    path: "~/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs".to_string(),
                },
            ],
            launcher: Launcher::ElectronOrNode,
            launch_env: pairs(&[("ELECTRON_RUN_AS_NODE", "1")]),
        },
        check: checked("zcode", "ZCode", "zcode"),
        run: Run {
            args: strings(&["--json", "--no-color", "--mode", "yolo"]),
            cwd_flag: Some("--cwd".to_string()),
            prompt: Prompt {
                transport: Transport::File,
                flag: Some("--attach".to_string()),
                argv_text: Some((
                    "--prompt".to_string(),
                    "Read the attached task brief and follow it. End with the required JSON report."
                        .to_string(),
                )),
            },
            effort: Some(Flag { args: vec![], values: strings(&["default"]) }),
            access: BTreeMap::from([
                (Access::Readonly, spec(&["--disallowed-tools", "Bash,Write,Edit,NotebookEdit"])),
                (Access::Exec, spec(&["--disallowed-tools", "Write,Edit,NotebookEdit"])),
                (Access::Write, spec(&[])),
            ]),
            after_access: vec![],
            session: Session {
                owner: SessionOwner::Engine,
                new: vec![],
                resume: strings(&["--resume", "{id}"]),
                missing: vec![
                    strings(&["session", "not found"]),
                    strings(&["session", "does not exist"]),
                    strings(&["unknown session"]),
                ],
            },
            line_cap: 8 * MIB,
            reap_group: true,
        },
        wire: Wire::JsonDoc,
        caps: Caps {
            report: ReportKind::Extracted,
            notes: strings(&["Access is a tool denylist (--disallowed-tools), not an OS sandbox."]),
        },
    }
}
