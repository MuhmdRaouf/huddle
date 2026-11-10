//! A fake CLI harness: one `sh` script that speaks a harness dialect on stdout and records argv.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use serde_json::{Value, json};

use crate::script::write_executable;
use crate::temp_home::TempHome;

/// One assistant turn the RPC fakes play.
#[derive(Clone, Debug)]
pub struct RpcTurn {
    pub text: String,
}

/// How an RPC fake behaves after the handshake.
#[derive(Clone, Debug, PartialEq)]
pub enum RpcEnd {
    Normal,
    HangUntilAbort,
    CrashMidTurn,
    DieAfterReady,
}

/// What the fake prints.
#[derive(Clone, Debug)]
pub enum Mode {
    Text(String),
    /// opencode's `run --format json` lines.
    Jsonl {
        session: String,
        text: String,
    },
    /// zcode's one pretty-printed document.
    JsonDoc {
        session: String,
        response: String,
    },
    /// `claude -p --output-format stream-json` lines.
    StreamJson {
        session: String,
        text: String,
    },
    OmpRpc {
        session: String,
        turns: Vec<RpcTurn>,
        end: RpcEnd,
    },
    PiRpc {
        session: String,
        turns: Vec<RpcTurn>,
        end: RpcEnd,
    },
}

/// One start of the fake.
#[derive(Clone, Debug)]
pub struct Call {
    pub argv: Vec<String>,
    pub env: Vec<(String, String)>,
    pub stdin: Vec<String>,
}

/// An installed fake. Its files live under `<home>/fake-harness/<name>/`.
pub struct FakeHarness {
    path: PathBuf,
    dir: PathBuf,
}

impl FakeHarness {
    /// Installs the executable `<home>/fake-harness/<name>/<name>`.
    pub fn install(
        home: &TempHome,
        name: &str,
        mode: Mode,
        exit_code: i32,
        stderr: &str,
    ) -> io::Result<FakeHarness> {
        let dir = home.root().join("fake-harness").join(name);
        fs::create_dir_all(&dir)?;
        let rpc = match &mode {
            Mode::OmpRpc {
                session,
                turns,
                end,
            } => {
                write_turns(&dir, &omp_handshake(session), &omp_turns(turns))?;
                Some((Dialect::Omp, end.clone()))
            }
            Mode::PiRpc {
                session,
                turns,
                end,
            } => {
                write_turns(&dir, &pi_handshake(session), &pi_turns(turns))?;
                Some((Dialect::Pi, end.clone()))
            }
            _ => {
                fs::write(dir.join("stdout"), stdout_of(&mode))?;
                None
            }
        };
        fs::write(dir.join("stderr"), stderr)?;
        let path = dir.join(name);
        write_executable(&path, &script(&dir, rpc.as_ref(), exit_code))?;
        Ok(FakeHarness { path, dir })
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Every start so far, oldest first.
    pub fn calls(&self) -> io::Result<Vec<Call>> {
        let count = match fs::read_to_string(self.dir.join("count")) {
            Ok(text) => text.trim().parse::<usize>().unwrap_or(0),
            Err(err) if err.kind() == io::ErrorKind::NotFound => 0,
            Err(err) => return Err(err),
        };
        let mut calls = Vec::with_capacity(count);
        for n in 1..=count {
            let argv = fs::read(self.dir.join(format!("argv.{n}")))?;
            let env = fs::read_to_string(self.dir.join(format!("env.{n}")))?;
            let stdin = fs::read_to_string(self.dir.join(format!("stdin.{n}"))).unwrap_or_default();
            calls.push(Call {
                argv: argv
                    .split(|byte| *byte == 0)
                    .filter(|part| !part.is_empty())
                    .map(|part| String::from_utf8_lossy(part).to_string())
                    .collect(),
                env: env
                    .lines()
                    .filter_map(|line| {
                        let (name, value) = line.split_once('=')?;
                        Some((name.to_string(), value.to_string()))
                    })
                    .collect(),
                stdin: stdin.lines().map(str::to_string).collect(),
            });
        }
        Ok(calls)
    }
}

enum Dialect {
    Omp,
    Pi,
}

fn stdout_of(mode: &Mode) -> String {
    match mode {
        Mode::Text(text) => text.clone(),
        Mode::Jsonl { session, text } => opencode_lines(session, text),
        Mode::JsonDoc { session, response } => {
            let doc = json!({
                "sessionId": session,
                "response": response,
                "usage": {"inputTokens": 10, "outputTokens": 5},
                "projection": {"status": "idle", "turnCount": 1}
            });
            format!("{}\n", to_pretty(&doc))
        }
        Mode::StreamJson { session, text } => stream_json_lines(session, text),
        Mode::OmpRpc { .. } | Mode::PiRpc { .. } => String::new(),
    }
}

fn opencode_lines(session: &str, text: &str) -> String {
    [
        json!({"type": "step_start", "timestamp": 1, "sessionID": session,
               "part": {"type": "step-start"}}),
        json!({"type": "text", "timestamp": 2, "sessionID": session,
               "part": {"type": "text", "text": text}}),
        json!({"type": "step_finish", "timestamp": 3, "sessionID": session,
               "part": {"type": "step-finish", "reason": "stop",
                        "tokens": {"input": 10, "output": 5, "cache": {"read": 0, "write": 0}},
                        "cost": 0.001}}),
    ]
    .iter()
    .map(|line| format!("{line}\n"))
    .collect()
}

fn stream_json_lines(session: &str, text: &str) -> String {
    [
        json!({"type": "system", "subtype": "init", "session_id": session}),
        json!({"type": "assistant", "session_id": session,
               "message": {"role": "assistant", "content": [{"type": "text", "text": text}]}}),
        json!({"type": "result", "subtype": "success", "is_error": false, "result": text,
               "num_turns": 1, "total_cost_usd": 0.001, "session_id": session}),
    ]
    .iter()
    .map(|line| format!("{line}\n"))
    .collect()
}

fn to_pretty(value: &Value) -> String {
    serde_json::to_string_pretty(value).unwrap_or_default()
}

fn omp_handshake(session: &str) -> Vec<(&'static str, Value)> {
    vec![
        (
            "negotiate_protocol",
            json!({"id": "__ID__", "type": "response", "command": "negotiate_protocol",
                   "success": true, "data": {"protocolVersion": 2}}),
        ),
        (
            "set_event_filter",
            json!({"id": "__ID__", "type": "response", "command": "set_event_filter",
                   "success": true, "data": {}}),
        ),
        (
            "get_state",
            json!({"id": "__ID__", "type": "response", "command": "get_state", "success": true,
                   "data": {"sessionId": session, "isSettled": true,
                            "dumpTools": [{"name": "read"}, {"name": "grep"}, {"name": "glob"},
                                          {"name": "edit"}, {"name": "write"}, {"name": "bash"},
                                          {"name": "todo"}]}}),
        ),
    ]
}

fn pi_handshake(session: &str) -> Vec<(&'static str, Value)> {
    vec![(
        "get_state",
        json!({"type": "response", "id": "__ID__", "command": "get_state", "success": true,
               "data": {"sessionId": session, "messageCount": 0}}),
    )]
}

fn omp_turns(turns: &[RpcTurn]) -> Vec<Vec<Value>> {
    turns
        .iter()
        .map(|turn| {
            vec![
                json!({"id": "__ID__", "type": "response", "command": "prompt", "success": true}),
                json!({"type": "message_update",
                       "assistantMessageEvent": {"type": "text_delta", "delta": turn.text},
                       "message": {"role": "assistant"}}),
                json!({"type": "message_end",
                       "message": {"role": "assistant",
                                   "content": [{"type": "text", "text": turn.text}],
                                   "provider": "zai", "model": "glm-5.3",
                                   "usage": {"input": 10, "output": 5, "cacheRead": 0,
                                             "cacheWrite": 0, "cost": {"total": 0.001}},
                                   "stopReason": "stop"}}),
                json!({"type": "prompt_result", "id": "__ID__", "agentInvoked": true,
                       "status": "completed", "sessionSettled": true}),
            ]
        })
        .collect()
}

fn pi_turns(turns: &[RpcTurn]) -> Vec<Vec<Value>> {
    turns
        .iter()
        .map(|turn| {
            vec![
                json!({"type": "response", "id": "__ID__", "command": "prompt", "success": true,
                       "data": {"disposition": "started"}}),
                json!({"type": "message_update",
                       "assistantMessageEvent": {"type": "text_delta", "delta": turn.text}}),
                json!({"type": "message_end",
                       "message": {"role": "assistant",
                                   "content": [{"type": "text", "text": turn.text}],
                                   "provider": "zai", "model": "glm-5.3",
                                   "usage": {"input": 10, "output": 5, "cacheRead": 0,
                                             "cacheWrite": 0, "cost": {"total": 0.001}},
                                   "stopReason": "stop"}}),
                json!({"type": "agent_settled"}),
            ]
        })
        .collect()
}

// Writes `answer.<command>` (one frame) and `turn.<n>` (one frame per line).
fn write_turns(
    dir: &Path,
    handshake: &[(&'static str, Value)],
    turns: &[Vec<Value>],
) -> io::Result<()> {
    for (command, frame) in handshake {
        fs::write(dir.join(format!("answer.{command}")), format!("{frame}\n"))?;
    }
    for (index, frames) in turns.iter().enumerate() {
        let body: String = frames.iter().map(|frame| format!("{frame}\n")).collect();
        fs::write(dir.join(format!("turn.{}", index + 1)), body)?;
    }
    Ok(())
}

// Reads answer.<cmd>, turn.<n>; replaces __ID__ with the request's id; `$n` is this start's number.
fn rpc_body(dialect: &Dialect, end: &RpcEnd) -> String {
    let ready = match dialect {
        Dialect::Omp => concat!(
            r#"printf '%s\n' '{"type":"ready","protocolVersion":1,"#,
            r#""supportedProtocolVersions":[1,2],"maxFrameBytes":1048576}'"#,
            "\n"
        ),
        Dialect::Pi => "",
    };
    let abort_frame = match dialect {
        Dialect::Omp => concat!(
            r#"printf '%s\n' '{"type":"prompt_result","id":"'"$turn_id"'","agentInvoked":true,"#,
            r#""status":"aborted","sessionSettled":true}'"#,
        ),
        Dialect::Pi => concat!(
            r#"printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[],"#,
            r#""stopReason":"aborted"}}'"#,
            "\n",
            r#"      printf '%s\n' '{"type":"agent_settled"}'"#,
            "\n",
            r#"      printf '%s\n' '{"type":"response","id":"'"$id"'","command":"abort","success":true}'"#,
        ),
    };
    let after_ready = if *end == RpcEnd::DieAfterReady {
        "cat \"$d/stderr\" >&2\nexit 1\n"
    } else {
        ""
    };
    let prompt_arm = match end {
        RpcEnd::HangUntilAbort => {
            "      turn_id=$id\n      sed -n 1p \"$d/turn.$((turns+1))\" | sed \"s/__ID__/$id/g\"\n"
        }
        RpcEnd::CrashMidTurn => {
            "      sed -n 1p \"$d/turn.$((turns+1))\" | sed \"s/__ID__/$id/g\"\n      cat \"$d/stderr\" >&2\n      exit 3\n"
        }
        _ => "      turns=$((turns+1))\n      sed \"s/__ID__/$id/g\" \"$d/turn.$turns\"\n",
    };
    format!(
        r#"{ready}{after_ready}turns=0
turn_id=
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$d/stdin.$n"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  type=$(printf '%s' "$line" | sed -n 's/.*"type":"\([a-z_]*\)".*/\1/p')
  case "$type" in
    prompt)
{prompt_arm}      ;;
    abort)
      {abort_frame}
      ;;
    *)
      if [ -f "$d/answer.$type" ]; then sed "s/__ID__/$id/g" "$d/answer.$type"; fi
      ;;
  esac
done
exit 0
"#
    )
}

fn script(dir: &Path, rpc: Option<&(Dialect, RpcEnd)>, exit_code: i32) -> String {
    let body = match rpc {
        Some((dialect, end)) => rpc_body(dialect, end),
        None => format!(
            "cat \"$d/stdout\"\ncat \"$d/stderr\" >&2\n[ -t 0 ] || cat > \"$d/stdin.$n\"\nexit {exit_code}\n"
        ),
    };
    format!(
        "#!/bin/sh\nd='{}'\nn=$(cat \"$d/count\" 2>/dev/null || echo 0)\nn=$((n+1))\necho \"$n\" > \"$d/count\"\nprintf '%s\\0' \"$0\" \"$@\" > \"$d/argv.$n\"\nenv > \"$d/env.$n\"\n{body}",
        dir.display()
    )
}
