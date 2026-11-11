//! One harness turn: resolve the binary, build the launch, write the prompt file, start the
//! child, and hand back the outcome. The four file wires read stdout once through a reader; the
//! two RPC wires run the handshake, the prompt turns with correction, and the close.

pub use crate::table::Brief;

use crate::argv::{ArgvError, Launch, build};
use crate::correction::{OmpDriver, PiDriver, run_report_turns};
use crate::resolve::{ResolveError, Resolved, resolve};
use crate::rpc_common::{
    PlainDecoder, RpcConn, RpcError, RpcErrorKind, RpcLog, abort_soft_stop, shared_counter,
};
use crate::rpc_omp::{
    OmpDecoder, OmpRun, expected_tools as omp_expected_tools, handshake as omp_handshake,
    is_benign_stderr as omp_benign_stderr, output as omp_output,
};
use crate::rpc_pi::{
    PiRun, handshake as pi_handshake, is_benign_stderr as pi_benign_stderr, output as pi_output,
};
use crate::table::{
    Entry, ExitInfo, OneShotReader, RunEvent, RunStats, SessionRef, Wire, WireOutput,
};
use crate::wire_jsondoc::JsonDocReader;
use crate::wire_jsonl::JsonlReader;
use crate::wire_stream_json::StreamJsonReader;
use crate::wire_text::TextReader;
use huddle_config::atomic::write_atomic;
use huddle_config::env::Env;
use huddle_config::files::{DIR_MODE, create_dir};
use huddle_procs::stream::{
    Forced, Limits, NextLine, ProcExit, Spec, Stdin, StopHandle, Streaming, spawn_streaming,
};
use std::fmt;
use std::fs;
use std::io;
use std::path::PathBuf;
use std::sync::{Arc, atomic::AtomicU64};
use std::time::Duration;

/// Everything one turn runs with: the environment, the job's directories, the stop settings and
/// the callbacks a driver uses to record the process and stream its events.
pub struct RunCtx<'a> {
    pub env: &'a Env,
    /// The job's worktree.
    pub cwd: PathBuf,
    /// Created if missing (0700): prompt.md, stdout.log, rpc-frames.jsonl and rpc-commands.jsonl
    /// go here.
    pub log_dir: PathBuf,
    pub grace: Duration,
    /// None: no wall clock (never Some(zero)).
    pub timeout: Option<Duration>,
    /// Called once, right after the process starts, with its group-leader pid and a stop handle.
    pub on_spawn: Option<&'a (dyn Fn(u32, StopHandle) + 'a)>,
    pub on_event: Option<&'a (dyn Fn(&RunEvent) + 'a)>,
}

impl<'a> RunCtx<'a> {
    /// Grace 10_000 ms, timeout 7_200_000 ms, no callbacks.
    pub fn new(env: &'a Env, cwd: PathBuf, log_dir: PathBuf) -> RunCtx<'a> {
        RunCtx {
            env,
            cwd,
            log_dir,
            grace: Duration::from_millis(10_000),
            timeout: Some(Duration::from_millis(7_200_000)),
            on_spawn: None,
            on_event: None,
        }
    }
}

/// How one turn went: the report, the session the harness named, how it ended, and the wire
/// reader's verdict on all of it.
#[derive(Clone, Debug, PartialEq)]
pub struct Outcome {
    pub report_text: String,
    pub session_id: Option<String>,
    pub exit: ExitInfo,
    /// Why the run failed, one line; None when it did not.
    pub failure: Option<String>,
    pub stats: RunStats,
}

/// Why a turn could not run.
#[derive(Debug)]
pub enum RunError {
    Resolve(ResolveError),
    Argv(ArgvError),
    /// `spawn_streaming` failed, or the prompt file could not be written. The message names what.
    Start(String),
    /// A log file or the log dir could not be created.
    Io(String),
    /// Only RPC wires: the handshake failed in a way that is not an exit.
    Protocol(String),
}

impl fmt::Display for RunError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RunError::Resolve(err) => err.fmt(f),
            RunError::Argv(err) => err.fmt(f),
            RunError::Start(message) | RunError::Io(message) | RunError::Protocol(message) => {
                f.write_str(message)
            }
        }
    }
}

impl std::error::Error for RunError {}

/// Runs one turn of `entry` on its wire. The process starts in its own group, and the turn ends
/// when the process does: a file wire reads stdout once; an RPC wire talks until the session
/// settles and closes stdin.
pub fn run_turn(entry: &Entry, brief: &Brief, ctx: &RunCtx) -> Result<Outcome, RunError> {
    let resolved = resolve(entry, ctx.env).map_err(RunError::Resolve)?;
    create_dir(&ctx.log_dir, DIR_MODE)
        .map_err(|err| RunError::Io(format!("cannot create {}: {err}", ctx.log_dir.display())))?;
    let launch = build(entry, brief, &ctx.cwd, &ctx.log_dir).map_err(RunError::Argv)?;
    write_prompt_file(&launch)?;
    match &entry.wire {
        Wire::Text => one_shot(
            entry,
            brief,
            ctx,
            launch,
            resolved,
            Box::new(TextReader::new()),
        ),
        Wire::JsonDoc => one_shot(
            entry,
            brief,
            ctx,
            launch,
            resolved,
            Box::new(JsonDocReader::new()),
        ),
        Wire::Jsonl => one_shot(
            entry,
            brief,
            ctx,
            launch,
            resolved,
            Box::new(JsonlReader::new()),
        ),
        Wire::StreamJson => one_shot(
            entry,
            brief,
            ctx,
            launch,
            resolved,
            Box::new(StreamJsonReader::new()),
        ),
        Wire::OmpRpc { handshake } => run_omp(entry, brief, ctx, launch, resolved, *handshake),
        Wire::PiRpc { handshake } => run_pi(entry, brief, ctx, launch, resolved, *handshake),
    }
}

/// Runs one turn on a file wire: the child's stdout feeds the reader line by line, and the exit
/// settles the output.
fn one_shot(
    entry: &Entry,
    brief: &Brief,
    ctx: &RunCtx,
    launch: Launch,
    resolved: Resolved,
    mut reader: Box<dyn OneShotReader>,
) -> Result<Outcome, RunError> {
    let mut extra = launch.env.clone();
    extra.extend(resolved.env.iter().cloned());
    let env = crate::env::harness_env(ctx.env, &extra);
    let mut args = resolved.prefix_args.clone();
    args.extend(launch.args.iter().cloned());
    let stdin = Stdin::Text(launch.stdin.unwrap_or_default());
    let mut stream = spawn_streaming(
        Spec {
            program: resolved.program,
            args,
            cwd: ctx.cwd.clone(),
            env,
            stdin,
            stdout_log: Some(ctx.log_dir.join("stdout.log")),
            benign_stderr: None,
            reap_group: entry.run.reap_group,
            soft_stop: None,
        },
        Limits {
            grace: ctx.grace,
            timeout: ctx.timeout,
            output_cap: entry.run.line_cap,
        },
    )
    .map_err(|err| RunError::Start(err.to_string()))?;

    if let Some(on_spawn) = ctx.on_spawn {
        on_spawn(stream.pid(), stream.stop_handle());
    }

    read_lines(&mut stream, reader.as_mut(), ctx);

    let exit = stream.wait();
    let info = exit_info(entry, brief, &exit);
    let output = reader.finish(&info);
    Ok(Outcome {
        report_text: output.text,
        session_id: output.session_id,
        exit: info,
        failure: output.failure,
        stats: output.stats,
    })
}

/// Starts one RPC harness: stdin stays open for the protocol, stdout feeds only the frame
/// decoder, and the shared counter drives the attempt soft stop.
fn spawn_rpc(
    entry: &Entry,
    ctx: &RunCtx,
    launch: Launch,
    resolved: Resolved,
    counter: Arc<AtomicU64>,
    benign_stderr: fn(&str) -> bool,
) -> Result<Streaming, RunError> {
    let mut extra = launch.env.clone();
    extra.extend(resolved.env.iter().cloned());
    let env = crate::env::harness_env(ctx.env, &extra);
    let mut args = resolved.prefix_args;
    args.extend(launch.args);
    spawn_streaming(
        Spec {
            program: resolved.program,
            args,
            cwd: ctx.cwd.clone(),
            env,
            stdin: Stdin::Piped,
            stdout_log: None,
            benign_stderr: Some(benign_stderr),
            reap_group: entry.run.reap_group,
            soft_stop: Some(abort_soft_stop(counter)),
        },
        Limits {
            grace: ctx.grace,
            timeout: ctx.timeout,
            output_cap: entry.run.line_cap,
        },
    )
    .map_err(|err| RunError::Start(err.to_string()))
}

/// Why an RPC handshake gave up, in the runner's terms. The handshake killed the process on
/// every failure but an exit, so only the exit leaves a failed outcome behind.
fn rpc_handshake_error(error: RpcError) -> RunError {
    match error.kind {
        RpcErrorKind::Start => RunError::Start(error.message),
        _ => RunError::Protocol(error.message),
    }
}

/// The outcome of a handshake that found the child gone: no turn ran, so the exit is the story —
/// and a stop that forced the exit is the story over the exit's own.
fn exited_outcome(
    entry: &Entry,
    brief: &Brief,
    exit: ProcExit,
    message: String,
) -> Result<Outcome, RunError> {
    Ok(stopped_failure(
        entry,
        Outcome {
            report_text: String::new(),
            session_id: None,
            exit: exit_info(entry, brief, &exit),
            failure: Some(message),
            stats: RunStats::default(),
        },
    ))
}

/// A turn the stop settings cut short has no failure of its own; the stop is the story, whatever
/// the exit and the turn itself reported.
fn stopped_failure(entry: &Entry, mut outcome: Outcome) -> Outcome {
    if let Some(forced) = outcome.exit.forced.clone() {
        outcome.failure = Some(format!("{} was stopped ({forced})", entry.id));
    }
    outcome
}

/// The exit of one turn as the outcome carries it: a resume the engine does not know is flagged.
fn exit_info(entry: &Entry, brief: &Brief, exit: &ProcExit) -> ExitInfo {
    let session_missing = matches!(&brief.session, SessionRef::Resume { .. })
        && entry.run.session.is_missing(&exit.stderr_tail);
    ExitInfo {
        code: exit.code,
        signal: exit.signal.clone(),
        stderr_tail: exit.stderr_tail.clone(),
        forced: exit.forced.map(|forced| forced.as_str().to_string()),
        session_missing,
    }
}

/// The usage stats of an omp tally.
fn omp_stats(tally: &crate::rpc_omp::Tally) -> RunStats {
    RunStats {
        turns: tally.turns,
        model: tally.model.clone(),
        input_tokens: tally.input_tokens,
        output_tokens: tally.output_tokens,
        cache_read_tokens: tally.cache_read_tokens,
        cache_write_tokens: tally.cache_write_tokens,
        cost_usd: tally.cost_usd,
        duration_ms: 0,
        api_error_status: None,
    }
}

/// The usage stats of a pi tally.
fn pi_stats(tally: &crate::rpc_pi::Tally) -> RunStats {
    RunStats {
        turns: tally.turns,
        model: tally.model.clone(),
        input_tokens: tally.input_tokens,
        output_tokens: tally.output_tokens,
        cache_read_tokens: tally.cache_read_tokens,
        cache_write_tokens: tally.cache_write_tokens,
        cost_usd: tally.cost_usd,
        duration_ms: 0,
        api_error_status: None,
    }
}

/// Runs one turn of omp over its RPC wire: the handshake negotiates protocol 2 and reads the
/// session, the prompt turns run with correction, and the close drains the exit.
fn run_omp(
    entry: &Entry,
    brief: &Brief,
    ctx: &RunCtx,
    launch: Launch,
    resolved: Resolved,
    handshake_timeout: Duration,
) -> Result<Outcome, RunError> {
    let counter = shared_counter();
    let log = RpcLog::open(
        &ctx.log_dir.join("rpc-frames.jsonl"),
        &ctx.log_dir.join("rpc-commands.jsonl"),
    )
    .map_err(RunError::Io)?;
    let prompt = launch.rpc_prompt.clone().unwrap_or_default();
    let stream = spawn_rpc(
        entry,
        ctx,
        launch,
        resolved,
        counter.clone(),
        omp_benign_stderr,
    )?;
    if let Some(on_spawn) = ctx.on_spawn {
        on_spawn(stream.pid(), stream.stop_handle());
    }
    let mut conn = RpcConn::new(stream, OmpDecoder::new(), "omp", counter);
    conn.attach_log(log);
    let expected = omp_expected_tools(entry, brief.access);
    let handshake = match omp_handshake(&mut conn, &expected, handshake_timeout) {
        Ok(handshake) => handshake,
        Err(error) => match error.kind {
            RpcErrorKind::Exited => {
                return exited_outcome(entry, brief, conn.stream().wait(), error.message);
            }
            _ => return Err(rpc_handshake_error(error)),
        },
    };

    let mut on_event = |event: &RunEvent| {
        if let Some(callback) = ctx.on_event {
            callback(event);
        }
    };
    let mut run = OmpRun::new(conn, handshake, &mut on_event);
    let turned = {
        let mut driver = OmpDriver::new(&mut run, &mut on_event);
        run_report_turns(&mut driver, &prompt).map(|turns| (turns, driver.last_turn().cloned()))
    };
    let (turns, last) = match turned {
        Ok(turned) => turned,
        Err(error) => {
            let exit = run.close(&mut on_event);
            let info = exit_info(entry, brief, &exit);
            let stats = omp_stats(run.tally());
            return Ok(stopped_failure(
                entry,
                Outcome {
                    report_text: run.final_text().to_string(),
                    session_id: run.session_id().map(str::to_string),
                    exit: info,
                    failure: Some(error.message),
                    stats,
                },
            ));
        }
    };
    let exit = run.close(&mut on_event);
    let info = exit_info(entry, brief, &exit);
    let session = run.session_id().map(str::to_string);
    let tally = run.tally().clone();
    let span = turns.ended_at_ms.saturating_sub(turns.started_at_ms);
    let output = match last {
        Some(turn) => omp_output(session.as_deref(), &tally, &turn, span),
        // A good run settles at least one turn; this only keeps a bug from panicking.
        None => WireOutput {
            text: run.final_text().to_string(),
            session_id: session,
            failure: Some("omp settled no turn".to_string()),
            stats: omp_stats(&tally),
        },
    };
    Ok(stopped_failure(
        entry,
        Outcome {
            report_text: output.text,
            session_id: output.session_id,
            exit: info,
            failure: output.failure,
            stats: output.stats,
        },
    ))
}

/// Runs one turn of pi over its RPC wire: the handshake reads the session, the prompt turns run
/// with correction, and the close drains the exit.
fn run_pi(
    entry: &Entry,
    brief: &Brief,
    ctx: &RunCtx,
    launch: Launch,
    resolved: Resolved,
    handshake_timeout: Duration,
) -> Result<Outcome, RunError> {
    let counter = shared_counter();
    let log = RpcLog::open(
        &ctx.log_dir.join("rpc-frames.jsonl"),
        &ctx.log_dir.join("rpc-commands.jsonl"),
    )
    .map_err(RunError::Io)?;
    let prompt = launch.rpc_prompt.clone().unwrap_or_default();
    let stream = spawn_rpc(
        entry,
        ctx,
        launch,
        resolved,
        counter.clone(),
        pi_benign_stderr,
    )?;
    if let Some(on_spawn) = ctx.on_spawn {
        on_spawn(stream.pid(), stream.stop_handle());
    }
    let mut conn = RpcConn::new(stream, PlainDecoder, "pi", counter);
    conn.attach_log(log);
    let handshake = match pi_handshake(&mut conn, handshake_timeout) {
        Ok(handshake) => handshake,
        Err(error) => match error.kind {
            RpcErrorKind::Exited => {
                return exited_outcome(entry, brief, conn.stream().wait(), error.message);
            }
            _ => return Err(rpc_handshake_error(error)),
        },
    };

    let mut on_event = |event: &RunEvent| {
        if let Some(callback) = ctx.on_event {
            callback(event);
        }
    };
    let mut run = PiRun::new(conn, handshake, &mut on_event);
    let turned = {
        let mut driver = PiDriver::new(&mut run, &mut on_event);
        run_report_turns(&mut driver, &prompt)
    };
    let turns = match turned {
        Ok(turns) => turns,
        Err(error) => {
            let exit = run.close(&mut on_event);
            let info = exit_info(entry, brief, &exit);
            let stats = pi_stats(run.tally());
            return Ok(stopped_failure(
                entry,
                Outcome {
                    report_text: run.final_text().to_string(),
                    session_id: Some(run.session_id().to_string()),
                    exit: info,
                    failure: Some(error.message),
                    stats,
                },
            ));
        }
    };
    let exit = run.close(&mut on_event);
    let info = exit_info(entry, brief, &exit);
    let span = turns.ended_at_ms.saturating_sub(turns.started_at_ms);
    let output = pi_output(run.session_id(), run.tally(), span);
    Ok(stopped_failure(
        entry,
        Outcome {
            report_text: output.text,
            session_id: output.session_id,
            exit: info,
            failure: output.failure,
            stats: output.stats,
        },
    ))
}

/// The prompt file of a file transport, written atomically at 0600.
fn write_prompt_file(launch: &Launch) -> Result<(), RunError> {
    let Some((path, text)) = &launch.prompt_file else {
        return Ok(());
    };
    write_atomic(path, text.as_bytes(), 0o600).map_err(|err| {
        RunError::Start(format!(
            "cannot write the prompt file {}: {err}",
            path.display()
        ))
    })
}

/// Feeds every stdout line to the reader, stops the child once its output goes over budget, and
/// drains the events each line produced.
fn read_lines(stream: &mut Streaming, reader: &mut dyn OneShotReader, ctx: &RunCtx) {
    let mut events: Vec<RunEvent> = Vec::new();
    let mut stopped = false;
    loop {
        match stream.next_line(None) {
            NextLine::Line(line) => {
                reader.feed_line(&line, &mut events);
                for event in events.drain(..) {
                    if let Some(on_event) = ctx.on_event {
                        on_event(&event);
                    }
                }
                if reader.over_budget() && !stopped {
                    stopped = true;
                    stream.stop_handle().interrupt(Forced::Stopped);
                }
            }
            // next_line(None) waits without a clock; a timeout cannot come back.
            NextLine::Timeout => {}
            NextLine::Eof => break,
        }
    }
}

/// A fresh random UUID v4 (from /dev/urandom), for `SessionRef::New` keys: Claude requires one.
pub fn new_session_key() -> Result<String, RunError> {
    let mut bytes = [0u8; 16];
    fs::File::open("/dev/urandom")
        .and_then(|mut pool| io::Read::read_exact(&mut pool, &mut bytes))
        .map_err(|err| RunError::Io(format!("cannot read /dev/urandom: {err}")))?;
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    let hex: String = bytes.iter().map(|byte| format!("{byte:02x}")).collect();
    Ok(format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    ))
}
