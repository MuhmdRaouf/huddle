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

## Other agents

Huddle's MCP server works with any harness that speaks MCP. For five of them the binary wires itself in:

```sh
huddle harness list              # what is on this machine: installed, available or absent
huddle harness install <name>    # add huddle's MCP server to the harness's config
huddle harness remove <name>     # take it out again, the config back to its previous bytes
```

The names: `codex` (Codex CLI), `gemini` (Gemini CLI), `qwen` (Qwen Code), `opencode` and `crush`. Every change is
recorded in `~/.config/huddle/harness-ledger.json` (or `$HUDDLE_HOME/harness-ledger.json`) and undone exactly by
`remove`; an entry you edited by hand stays and remove says so. Any other MCP client can be pointed at the hub by
hand: the server speaks stdio through `huddle mcp` and HTTP through `huddle serve`.

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
