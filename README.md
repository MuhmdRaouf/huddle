# huddle

One local hub where your AI coding sessions work as a team, from Claude Code or any agent that speaks MCP.

- **Hub**: every session on every machine in one list, what needs you first, approvals from your phone with a
  passkey, sessions linked so they share context.
- **Channels**: events, tasks with cross-session dependencies, messages and shared knowledge, over MCP pub/sub.
- **Radar**: requests, input and output tokens, cost and health per session, model and project, with graphs. No
  tokens spent to watch.
- **Kanban**: a board per repository that sessions claim cards from and move through review and testing.

## Install

In Claude Code:

```text
/plugin marketplace add MuhmdRaouf/huddle
/plugin install huddle@huddle
```

That is all. The first session starts the hub and prints its address. The provider plugins in
[claude-code-plugins](https://github.com/MuhmdRaouf/claude-code-plugins) install huddle for you.

## Other agents

Huddle's MCP server works with any harness that supports MCP: Codex CLI, Gemini CLI, opencode, Crush, Goose, Qwen
Code and others. `huddle harness install <name>` adds the MCP server and the harness's hooks; `huddle harness
remove <name>` takes them out again.

A channel is a standard MCP pub/sub surface, so an agent can watch it without polling in a loop:

- `resources/list` names `huddle://<channel>/events` and one `huddle://<channel>/topic/<topic>` per topic the
  recent history shows, all `application/json`.
- `resources/read` on one of those uris returns the channel's events as one JSON array; add `?after=<seq>` to
  read only what came after a cursor, at most 100 events.
- `resources/subscribe` on a uri gets `notifications/resources/updated` every time the topic (or the channel)
  gets an event; `resources/unsubscribe` stops it. Subscriptions live in the bridge process and end with the
  session.
- The `poll` tool is the long poll for harnesses without subscriptions: `after` (a seq), `topics` (globs) and
  `timeout_ms` (at most 60000); it answers the events after the cursor, or `[]` on timeout.

Identify a session with `HUDDLE_SESSION`; no Claude Code variable is needed.

## Platforms

macOS and Linux, on amd64 and arm64. One static binary, downloaded and checked against pinned SHA-256 digests on first
use.

## Licence

GPL-3.0-or-later. See [LICENSE](LICENSE).
