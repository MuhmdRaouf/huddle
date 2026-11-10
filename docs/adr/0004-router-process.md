# ADR-4: huddle-router runs in its own process

Status: accepted, 2026-10-25. It sets D2 and D10 in [DECISIONS.md](../DECISIONS.md).

## Context

- Every Claude Code request rides a CONNECT tunnel the router holds open while the answer streams;
  a cut mid-answer is an error the harness does not retry.
- Anything that shares a process with the router — a restart, an update, a crash, a memory ceiling
  — closes every tunnel in flight with it.
- The router changes rarely; the sessions and the words around it change often.
- The router already owned its lock, its health path, its record, its handover socket and its RSS
  ceiling; only the process boundary had to be drawn.

## Decision

- huddle-router is its own process, `huddle router serve`. No other part of the tree hosts it, and
  nothing else serves model traffic; the router is the tree's only long-lived
  process.
- `huddle router start` probes first: a daemon that answers its health path is named and never
  started over, with or without a provider. A start whose record names another build takes the
  running router over through the handover socket, and the port never closes during the takeover.
- The router's record (`~/.agents/huddle-router/run/router.json`) names its pid, port and build. It
  lands before the first health answer, so a router seen healthy always has a pid to show.
- A stop sends no signal when the record names no live daemon on the recorded port.
- The RSS ceiling (1 GiB by default, `HUDDLE_ROUTER_RSS_CEILING_MB`, checked every 30 s) hands the
  listening socket to a fresh copy of the binary the same way.

## Consequences

- A session restart, an update of any other part, or a crashed session never touches the router;
  Claude streams keep going through it.
- The router changes process only by its own handover: a start from another build, or its RSS
  ceiling. The listening port is bound with a backlog that absorbs dials before any answer, so a
  takeover under load refuses no dial.
- The router comes back on its own: the session start starts the binary detached, and every prompt
  probes the health path within its budget
  (`crates/huddle/tests/session_hook.rs:a_session_start_brings_a_stopped_router_back_without_waiting`,
  `crates/huddle/tests/prompt_probe.rs:a_prompt_brings_a_dead_router_back_without_waiting`).
- Route events keep landing under the router's own events directory whatever happens around it.
