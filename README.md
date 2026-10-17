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

## Platforms

macOS and Linux, on amd64 and arm64. One static binary, downloaded and checked against pinned SHA-256 digests on first
use.

## Licence

GPL-3.0-or-later. See [LICENSE](LICENSE).
