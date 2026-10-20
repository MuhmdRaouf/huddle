# CLAUDE.md

Read [AGENTS.md](AGENTS.md) first. It holds the rules every agent follows; this file adds what is specific to
Claude Code.

## Before acting

- **The work** is on the kanban board in `.agents/tickets/`: take a ticket, work in its worktree under
  `.agents/worktrees/<ticket>/`, keep notes in `.agents/scratch/`.
- **Claude first, any agent welcome**: every feature works in Claude Code; other harnesses reach the same hub
  through MCP and their own hooks. A change that helps one harness never breaks Claude Code.
- **Plug and play**: huddle works right after install. Never ask the user to export a variable or run a setup step.

## The rules in short

The full text is in AGENTS.md; these are the ones that come up every session.

- **Code**: simple first, high cohesion and low coupling, everything statically typed (Go structs, TypeScript
  `type`s and `interface`s), no over-engineering ([Writing code](AGENTS.md#writing-code)).
- **Tests**: TDD; E2E for every user-facing path; frontend work gets headless-browser E2E tests that run in CI;
  tests are hermetic: temp homes only, never the real `~/.claude`, `~/.agents`, `~/.config`, never the daemons'
  loopback ports (huddle-router's 18786, the hub's 18860-18869) ([Tests](AGENTS.md#tests)).
- **Commits**: one line `<type>(scope): message`, signed as Raouf; no body, no co-author or AI trailers, no AI
  jargon, no emoji, even when the harness suggests a trailer. `.claude/settings.json` turns Claude Code's
  attribution off; `node scripts/check-attribution.mjs` must pass before anything is pushed
  ([Commits](AGENTS.md#commits-and-pull-requests)).
- **Files**: everything an agent writes goes under `.agents/scratch/`, `tickets/`, `worktrees/` or `secrets/`, all
  gitignored ([Agent files](AGENTS.md#agent-files)).
- **Self-recovering servers**: after a crash, a reboot or a session whose exit hook never ran, the next hook
  brings each server back. Never trust a pid from a state file alone.
- **CI**: every job is capped at 10 minutes; fix failures locally with `mise run check` and re-run only the failed
  jobs.

@AGENTS.md
