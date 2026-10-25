<p align="center">
  <img src="../assets/banner.svg" alt="Huddle: one local hub where AI coding sessions work as a team" width="100%"/>
</p>

<p align="center">
  <a href="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml"><img src="https://github.com/MuhmdRaouf/huddle/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-GPL--3.0--or--later-blue.svg" alt="License: GPL-3.0-or-later"></a>
  <a href="https://code.claude.com/docs/en/plugins"><img src="https://img.shields.io/badge/Claude_Code-plugins-cba6f7" alt="Claude Code plugins"></a>
  <a href="../README.md#requirements"><img src="https://img.shields.io/badge/Platforms-macOS%20%C2%B7%20Linux%20%28amd64%2C%20arm64%29-5891ae" alt="Platforms: macOS and Linux, amd64 and arm64"></a>
</p>

# Huddle

**One local hub where your AI coding sessions work as a team, from Claude Code or any agent that speaks MCP.**

| Need | Huddle gives you |
|---|---|
| See every session | The hub's Now list: every session on every machine, grouped by state — needs you, working, idle, stuck, ended. |
| Work as one team | Channels with shared events, tasks that wait on each other, messages and knowledge, over MCP. |
| Answer from anywhere | Approvals from your phone with a passkey; a permission prompt holds 10 minutes. |
| Watch the spend | Radar: every request with its model, latency, tokens and cost. Watching spends no tokens. |
| Move work forward | A kanban board per repository that sessions claim cards from; the first claim wins. |

## Find what you need

| Goal | Read |
|---|---|
| Install and connect your sessions | [Quick start](#quick-start) |
| Run the commands | [Commands](#commands) |
| Learn the workflow | [How it works](#how-it-works) |
| Change a setting, find a file | [Configuration](#configuration) · [Storage](#storage) |
| Use the CLI, other agents, the gateway, your phone | [The main README](../README.md) |
| Turn it off | [Uninstalling](#uninstalling) |

## Quick start

1. **Install the plugin** (once per machine), then restart Claude Code:

   ```text
   /plugin marketplace add MuhmdRaouf/huddle
   /plugin install huddle@muhmdraouf
   ```

2. **Nothing else to install.** The launcher gets one static binary (macOS and Linux, amd64 and arm64) onto the
   machine: while no release is published it builds the marketplace checkout's own commit once with `go`, and a
   release download, when one exists, is checked against the committed manifest. No JS runtime is needed on the
   PATH.

3. **Set it up** from a session in a project that should take part:

   ```text
   /huddle:setup
   ```

   That is the only step; it asks nothing.
   - It names the channel and this session after the project folder; `/huddle:setup shop api` names them yourself.
   - It writes `.agents/huddle/huddle.json` (kept out of git) and turns autostart on: the SessionStart hook brings the
     hub back whenever it is down, after a reboot too.
   - It starts the hub, joins and prints two lines: a `join:` line for other sessions, and a `dashboard:` link that
     signs the browser in once, within 5 minutes. The first sign-in offers the authenticator QR; "Later" skips it.

4. **Bring in each other project.** Paste the join line into a session there (`/huddle:join 127.0.0.1:<port>
   --token …`), or run `/huddle:invite` for a new one (24 h by default). A project with no settings joins the Huddle
   that already runs instead of starting a second one; `/huddle:setup --new` starts a separate one on purpose.

## Requirements

- Claude Code with plugin support.
- macOS or Linux, amd64 or arm64. No runtime to install: the launcher builds the static `huddle` binary of the
  checkout's own commit with `go` while no release is published (a release download is checked against the
  committed manifest when one exists). Without Go it names the install-Go line and stops; a hook never blocks.

## Commands

| Command | What it does |
|---|---|
| `/huddle:setup [channel] [session]` | Set up Huddle on this machine and join this project to a channel (or to the Huddle you already run) |
| `/huddle:invite` | Make a join line that brings another Claude session into this Huddle |
| `/huddle:join <host:port> --token …` | Join a Huddle with the join line `/huddle:invite` showed in a session that is in it |
| `/huddle:open` | Ensure the hub is running and print its dashboard URL (Now, Radar and Kanban tabs) |
| `/huddle:status` | One table: hub, radar, board server, gateway; running or not, address |
| `/huddle:stop` | Stop the hub, radar and the board server this user started |
| `/huddle:kanban-init` | Create this project's kanban board (`.agents/kanban`), or migrate an old backlog board into it |
| `/huddle:kanban-lint` | Check every ticket against the bar; exit 1 when any fails |
| `/huddle:kanban-refresh` | Rescan the board now and reload every open board page |
| `/huddle:kanban-export` | Write the board as one self-contained HTML page for sharing |

`/huddle:setup` also takes `--no-autostart`, `--port <n>`, `--new` and `--restart`; `/huddle:invite` takes `--ttl 2h`,
`--single-use` and `--can-invite`; `/huddle:join` takes `--as <name>`.

## How it works

- **Channels.** One channel per piece of shared work: events, tasks with cross-session dependencies, messages and
  shared knowledge, over MCP — subscribe to a topic, or long-poll up to 60 s. A subagent joins as `<parent>.<role>`.
- **The hub.** Every session on every machine in one Now list — each machine plays a role: `solo`, `hub` or
  `satellite` — grouped by state: needs you, working, idle, stuck (10 quiet minutes), ended.
- **Needs you.** A session waiting on a permission sits in needs you with one push when it appears and one
  escalation after 5 minutes open; its prompt holds 10 minutes. Answer in the dashboard, or from your phone with
  a passkey.
- **Radar.** Every request with its model, latency, input, output and cache tokens and its cost, across eleven views,
  fed by hooks, transcripts and the spool the router appends to. Watching spends no tokens.
- **Kanban.** One board per repository (`.agents/kanban`): sessions claim cards, the first claim wins, and review and
  testing gates sit on the way to done. Two machines share one board when they share the repository.
- **In the session.** Each Claude Code event runs one hook process — huddle's, radar's and kanban's parts ride inside
  it — so there is nothing to start by hand. The hook runs the stable `~/.agents/huddle-bin/current` binary once the
  first build exists, so a session's hooks keep running after a plugin update replaces the plugin's directory. The MCP server `huddle` exposes the operations, the `huddle` and `kanban`
  skills teach the protocol, and the `huddle-worker` agent does one bounded task and shares its findings.
- **The router.** huddle-router is the daemon at 127.0.0.1:18786 that the provider plugins route through: one lane
  per enabled provider, `claude-*` to Anthropic untouched, the gateway under `/gateway`. It is its own process
  beside the hub, so a hub restart never cuts a Claude answer, and each session's `huddle mcp` restarts it after
  5 s of silence.

## Configuration

### The home

One home per user, never a folder of the project: `$HUDDLE_HOME`, else `$XDG_CONFIG_HOME/huddle`, else
`~/.config/huddle`. Every session on the machine reports to the same server.

### `config.toml`

| Key | Default | Meaning |
|---|---|---|
| `role` | `solo` | this machine's role: `solo`, `hub` or `satellite` |
| `port` | `18860` | the server's port; `18861` to `18869` next, then the OS's pick; saved back with `port_auto` when huddle picked it itself |
| `machine` | the hostname, up to 32 characters | this machine's name in the hub |
| `hub.url`, `hub.listen`, `hub.host` | (none) | a satellite's hub: its address, and the hub's own listen address and name |
| `cert.dns_provider` | (none) | the DNS provider answering the DNS-01 challenge for the hub's public name |
| `remote.permissions`, `remote.messages` | `true` | whether a paired device may answer permissions and send messages |
| `remote.allow` | `["*"]` | the tool patterns a paired device may allow; the floor (`rm`, force push, secrets paths) is enforced in code |

Keys huddle does not know are kept on every rewrite; the file is written atomically and private (0600).

### A project's settings

`.agents/huddle/huddle.json`, kept out of git:

```json
{ "channel": "shop", "as": "api", "role": "builds the API", "autostart": true }
```

`HUDDLE_URL`, `HUDDLE_CHANNEL` and `HUDDLE_AS` override it. A git worktree shares its main checkout's file.

### The gateway

`$HUDDLE_HOME/gateway.json` names the routes for other agents' model requests. The huddle-router daemon serves
the gateway under `/gateway`; `huddle gateway status` answers its address.

## Storage

| Where | What |
|---|---|
| the home | `config.toml`; `channels/<name>.db`, one SQLite file per channel; `auth.json`, the credential digests; `sessions/`, the sessions' credentials; `run/`, the pid, port, log and owner key; `gateway.json`; `harness-ledger.json`; `pki/`, the local CA; `imports.json`, the one-time import's record |
| a project | `.agents/huddle/huddle.json` (the settings) and `.agents/kanban/` (the board: one Markdown file per ticket, `kanban.db`) |
| a legacy `<repo>/.agents/huddle` home | imports once into the user's home: each channel copied through SQLite's backup API, the old folder left untouched |

The hub is the only writer of a channel's database, so a copy of the file is a complete backup of the channel.

## The CLI

The `huddle` binary the launcher runs is the whole product: `up` starts the hub, `status` reports every part, and
`radar …` and `kanban …` run the dashboard's and the board's commands. It runs without Claude Code too: the same
binary, the same home.

[The main README](../README.md) covers what this page does not: the four ways to sign in (an authenticator code,
recovery codes, a passkey, a sign-in link), wiring other agents in (`codex`, `gemini`, `qwen`, `opencode`, `crush`),
routing their model requests through the gateway, serving the hub on a local name, and the dashboard from your phone.

## Update

- Run `/plugin marketplace update huddle`, then restart Claude Code. The plugin carries no version: an update is
  the marketplace refresh and a restart, and the launcher builds the refreshed checkout's own commit once (a
  release binary is verified against its manifest when one is published), so plugins and binary always come from
  the same commit.

## Uninstalling

Run `/plugin uninstall huddle@muhmdraouf`.

- A server the plugin started watches Claude Code's plugin registry: two consecutive checks, 10 s apart, that agree
  the plugin is gone or disabled stop it. A hub started by hand (`huddle serve`) never watches.
- On the way out it lets the requests it already took finish (up to 30 s), checkpoints every channel's WAL and closes
  it — each file is a complete backup of its channel — and removes the run files, `hooks.log` and `auth.json`.
- The channels stay: they are the conversations. `LEFT-BEHIND.md`, written beside them, names where they are and how
  to delete them (`rm -rf <home>/channels`).
- Nothing in a project is touched: `.agents/huddle/huddle.json` and `.agents/kanban/` stay as they are.

## Development

- The repository's [Development section](../README.md#development) holds the check and build commands.
- Bugs and ideas: [open an issue](https://github.com/MuhmdRaouf/huddle/issues).

## Security

- Report security problems privately through [GitHub security advisories](https://github.com/MuhmdRaouf/huddle/security/advisories/new); never open a public issue.

## Trademarks

This is an independent project, not affiliated with or endorsed by Anthropic. Claude and Claude Code are
trademarks of Anthropic, PBC. Codex, Gemini, Qwen, opencode, crush, omp, pi and ZCode belong to their respective
owners; they are named only to say what Huddle works with.

## Support

<p align="center">
  If Huddle makes your day a little easier, you can say thanks here:
</p>

<p align="center">
  <a href="https://github.com/sponsors/MuhmdRaouf"><img src="https://img.shields.io/badge/Sponsor_on_GitHub-EA4AAA?style=for-the-badge&logo=githubsponsors&logoColor=white" height="36" alt="Sponsor on GitHub"></a>&nbsp;&nbsp;<a href="https://buymeacoffee.com/muhmdraouf"><img src="https://img.shields.io/badge/Buy_me_a_coffee-FFDD00?style=for-the-badge&logo=buymeacoffee&logoColor=black" height="36" alt="Buy me a coffee"></a>
</p>

## License

[GPL-3.0-or-later](LICENSE). Copyright © 2026 [Raouf](https://github.com/MuhmdRaouf).
