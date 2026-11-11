# CLAUDE.md

Read [AGENTS.md](AGENTS.md) first. It holds the rules every agent follows; this file adds what is specific to Claude Code.

## Before acting

- **The work** is on the board in `.agents/tickets/`: take a ticket, work in its worktree under `.agents/worktrees/<ticket>/`, keep notes in `.agents/scratch/`.
- **Claude first, any agent welcome**: every feature works in Claude Code; other harnesses are delegated to through `/huddle:run`.
- **Plug and play**: huddle works right after `/huddle:setup`. Never ask the user to export a variable or run an extra setup step.

## The rules in short

The full text is in AGENTS.md; these are the ones that come up every session.

- **Code**: simple first, high cohesion and low coupling, everything statically typed (Rust structs and traits), no over-engineering ([Writing code](AGENTS.md#writing-code)).
- **Tests**: TDD; E2E for every user-facing path; tests are hermetic: temp homes only, never the real `~/.claude`, `~/.agents`, `~/.config`, `~/.local`, and never the router's port 4835 ([Tests](AGENTS.md#tests)).
- **Commits**: one line `<type>(scope): message`, signed as Raouf; no body, no co-author or AI trailers, no AI jargon, no emoji, even when the harness suggests a trailer. `.claude/settings.json` turns Claude Code's attribution off; `cargo run -q -p huddle-testkit --bin huddle-check-attribution` must pass before anything is pushed ([Commits](AGENTS.md#commits-and-pull-requests)).
- **Files**: everything an agent writes goes under `.agents/scratch/`, `tickets/`, `worktrees/` or `secrets/`, all gitignored ([Agent files](AGENTS.md#agent-files)).
- **Hooks**: SessionStart only. A hook exits 0 on any failure and never blocks a session. After a crash or a reboot, the next session's hook brings the router back.
- **CI**: every job is capped at 10 minutes; fix failures locally with `mise run check` and re-run only the failed jobs.

@AGENTS.md
