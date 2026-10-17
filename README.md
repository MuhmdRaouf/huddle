# huddle

One local hub where your AI coding sessions work as a team, from Claude Code or any agent that speaks MCP.

- **Hub**: every session on every machine in one list, what needs you first, approvals from your phone with a
  passkey, sessions linked so they share context.
- **Channels**: events, tasks with cross-session dependencies, messages and shared knowledge, over MCP pub/sub.
- **Radar**: requests, input and output tokens, cost and health per session, model, project and agent CLI, with
  graphs. No tokens spent to watch.
- **Kanban**: a board per repository that sessions claim cards from and move through review and testing.

## Install

In Claude Code:

```text
/plugin marketplace add MuhmdRaouf/huddle
/plugin install huddle@huddle
```

That is all. The first session starts the hub and prints its address. The provider plugins in
[claude-code-plugins](https://github.com/MuhmdRaouf/claude-code-plugins) install huddle for you.

## Commands

Ten slash commands, one product:

| Command | What it does |
|---|---|
| `/huddle:setup` | Set up Huddle on this machine and join this project to a channel |
| `/huddle:invite` | Make a join line that brings another Claude session into this Huddle |
| `/huddle:join` | Join a Huddle with the join line `/huddle:invite` showed |
| `/huddle:open` | Ensure the hub is running and print its dashboard URL (Hub, Radar and Kanban tabs) |
| `/huddle:status` | One table: hub, radar, board server, gateway; running or not, address |
| `/huddle:stop` | Stop the hub, radar and the board server this user started |
| `/huddle:kanban-init` | Create this project's kanban board, or migrate an old backlog board into it |
| `/huddle:kanban-lint` | Check every ticket against the bar; exit 1 when any fails |
| `/huddle:kanban-refresh` | Rescan the board now and reload every open board page |
| `/huddle:kanban-export` | Write the board as one self-contained HTML page for sharing |

The hooks start the servers, so there is nothing to start by hand: each event runs one hook process, and
huddle's, radar's and kanban's parts ride inside it.

## Shared board

Two machines work one board when they share the repository it belongs to:

- A board's key is its repository's origin, so every clone of one repository names one board. Join a second
  machine with `huddle pair --satellite` on the hub and `huddle join-hub` on the other machine: its sessions'
  claims and moves on that board relay to the hub, which holds the one `kanban.db` and the audit.
- A claim either machine asks for is one claim: the first pull wins, and every other pull — from either
  machine — gets the board's own refusal.
- The hub away, the satellite answers one plain line and changes nothing: a claim needs the hub's answer and
  is never queued.
- `new` stays local: a ticket is written to the clone the session sits in, never through the hub.
- Both machines' Kanban tabs show the hub's board, its live event stream included. While the hub is away, a
  satellite's tab shows its local copy.

## Other agents

Huddle's MCP server works with any harness that speaks MCP. For five of them the binary wires itself in:

```sh
huddle harness list              # what is on this machine: installed, available or absent
huddle harness install <name>    # add huddle's MCP server, and the harness's own lifecycle
                                 # hooks where it has them, to the harness's config
huddle harness remove <name>     # take both out again, the config back to its previous bytes
```

The names: `codex` (Codex CLI), `gemini` (Gemini CLI), `qwen` (Qwen Code), `opencode` and `crush`. Every change is
recorded in `~/.config/huddle/harness-ledger.json` (or `$HUDDLE_HOME/harness-ledger.json`) and undone exactly by
`remove`; an entry you edited by hand stays and remove says so. Any other MCP client can be pointed at the hub by
hand: the server speaks stdio through `huddle mcp` and HTTP through `huddle serve`.

### Lifecycle hooks

Install also wires each agent's own hook mechanism, so its sessions show up in the hub's Now list next to Claude
Code's — a session start, a prompt, a tool call and a session end report to the registry, and the row carries the
agent's name as a small badge:

- **codex** — inline `[[hooks.<Event>]]` tables in `~/.codex/config.toml`, in a marker-delimited block of huddle's
  own: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop and SessionEnd.
- **gemini** — the `hooks` object in `~/.gemini/settings.json`: SessionStart, BeforeAgent, BeforeTool, AfterTool,
  AfterAgent and SessionEnd.
- **qwen** — the same `hooks` object in `~/.qwen/settings.json`, under that CLI's Claude-Code-style event names.
- **opencode** — one `file://` entry in the `plugin` array of `opencode.json`, naming a small module huddle writes
  into its own home: it forwards `session.created` and `session.idle`, the only events that carry a session id.
- **crush** — the `hooks` object in `crush.json`. Crush's only hook event is PreToolUse, so a crush session reports
  tool calls and then ages idle; it never says it ended.

A hook never blocks the agent it runs in: it posts to the local hub and answers in silence, inside a 3 s deadline.
Remove takes the hooks out with the MCP server, through the same ledger.

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

## Route other agents through huddle

`huddle gateway start` runs a localhost endpoint that speaks the Anthropic Messages API and the OpenAI Chat
Completions and Responses APIs. Point a harness's base URL at it and every request goes to the upstream its model's
route names, unchanged, while radar records the model, latency and tokens. It takes the first free port in
18870-18879 and writes it to `$HUDDLE_HOME/gateway.port`.

1. Name your upstreams in `$HUDDLE_HOME/gateway.json`:

   ```json
   {
     "routes": [{"match": "glm-*", "upstream": "https://api.z.ai/api/anthropic"}],
     "anthropic": "https://api.anthropic.com",
     "openai": "https://api.openai.com"
   }
   ```

   A model goes to the longest glob that matches it; anything else goes to its API family's default. A request is
   forwarded byte for byte with its headers (authentication included — the gateway adds nothing and keeps nothing
   but the route line's metadata).

2. Start it and read the port:

   ```sh
   huddle gateway start        # or `huddle gateway` to keep it in the foreground
   huddle gateway status       # stop ends it
   ```

3. Give your harness the base URL `http://127.0.0.1:<port>`:

   - **Codex CLI** — `~/.codex/config.toml`, `model_providers.<x>.base_url`
   - **opencode** — the provider's `baseURL` in its config
   - **Crush** — `providers.<x>.base_url`

A client can name itself with an `X-Huddle-Harness` header, which lands on the route line next to `via: gateway`,
so radar tells the harnesses apart.

## Platforms

macOS and Linux, on amd64 and arm64. One static binary, downloaded and checked against pinned SHA-256 digests on first
use.

## Licence

GPL-3.0-or-later. See [LICENSE](LICENSE).
