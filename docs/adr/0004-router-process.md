# ADR-4: huddle-router runs in its own process

Status: accepted, 2026-10-25. It sets D2 and D9 in [DECISIONS.md](../DECISIONS.md).

## Context

- Every Claude Code request goes through huddle-router: `HTTPS_PROXY` points at `127.0.0.1:18786`, and each
  request rides a CONNECT tunnel the router holds open while the answer streams.
- The router ran inside `huddle serve`, the hub. A hub stop closed every tunnel in flight. At 01:28:15Z on
  2026-10-25 a `huddle down` during a deploy cut a session mid-answer with `API Error: Connection lost
  mid-response`, which Claude Code does not retry ([INCIDENTS.md](../INCIDENTS.md)). Six hub restarts ran that
  night.
- The hub changes far more often than the router: it carries channels, the registry, Radar, Kanban and the
  dashboard. A panic in one of the hub's goroutines, or the hub's 1 GiB memory limit, ended the router with it.
- The two already shared no lock and no memory, and talked only through files (the spool,
  `budget-status.json`, `providers.json`). Only the hosting crossed the process boundary.
- The standalone `huddle router serve` already existed and was tested: the router lock, the health path, the
  socket handover for a binary update and the RSS ceiling.

## Decision

- huddle-router is its own process, `huddle router serve`, and the hub never hosts it. Each user runs at most one
  router and one hub, each a singleton.
- `huddle up` starts the router through `routerd.Start`, then the hub. With no provider set up there is nothing to
  route and no router starts; the providers' session start, their setup and each session's watchdog start it as
  before.
- The code that ran the router inside the hub (`internal/huddle/server/router.go`) and the handover that moved a
  standalone router's socket into the hub are gone.
- `huddle status` and the hub's `/health` answer name both pids. The router's own record
  (`~/.agents/huddle-router/run/router.json`) is where its pid comes from, checked against the live process and
  its health path.

## Consequences

- A hub restart, update or crash never touches the router, so Claude streams keep going through it. The deploy
  recipe restarts the hub only.
- The router changes process only by its own handover: a start from another build, or its RSS ceiling. The port
  never closes during either.
- Two processes to supervise instead of one. Each already has its own singleton lock and its own way back after a
  crash: the hooks bring the hub back, and the session watchdog brings the router back within about 5 s.
- The router keeps its own RSS ceiling (1 GiB, `HUDDLE_ROUTER_RSS_CEILING_MB`); the hub keeps its Go memory limit.
  Neither one's memory counts against the other.
- Radar reads the router's spool from files, as before: nothing changes for it.
