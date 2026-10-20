# Changelog

## Unreleased

One repository, one binary, one daemon: the five provider plugins move in beside huddle, and everything they
ran separately is now one process set.

### One repository, one marketplace

- zai, kimi, deepseek, minimax and qwen now ship from this repository (marketplace `muhmdraouf`) as thin plugin
  shells: agents, slash commands and a launcher — no hooks and no binary of their own. Installing a provider
  plugin installs huddle with it, and the launcher builds the checkout's own commit with Go while no release is
  published.
- The five per-provider routers, their peer hops between ports, and the per-session `router watch` processes are
  gone. One daemon, huddle-router on 127.0.0.1:18786, holds one lane per enabled provider and forwards every
  `claude-*` request to Anthropic untouched; the plugins meet it only through `providers.json`, the spool and
  `budget-status.json`.
- One hook process runs per Claude Code event: huddle's handler and report, radar, kanban's session start and
  the providers' parts ride inside it, each with its own deadline. The provider plugins ship no hooks.

### The router daemon

- The listening socket is never closed to swap the process: a new binary takes the socket over by handover and
  the old process drains what it already accepted for up to 10 minutes. The same handover runs when the
  daemon's resident memory passes 1 GiB (`HUDDLE_ROUTER_RSS_CEILING_MB` overrides; checked every 30 s) and when
  the full edition's hub takes a standalone router over.
- Each session's `huddle mcp` bridge checks the daemon every 2 s and restarts it after 5 s of silence, replacing
  a hung holder first.
- Setup writes proxy mode: `HTTPS_PROXY` at the router, `NODE_EXTRA_CA_CERTS` at the shared local CA in
  `~/.agents/ca`, loopback entries in `NO_PROXY`. A proxy or CA of your own is never touched; removing a
  provider keeps the shared keys while another is set up, and restores `settings.json` after the last.

### Jobs

- The job engine is unchanged: briefs, gates, verdicts, detached drivers, the AIMD concurrency limiter and the
  omp, opencode, pi and zcode engines behave as before. Each provider's job state stays under `~/.agents/<p>/`,
  and keys keep their keystore names, so keys and jobs from 0.0.1 keep working.

## 0.0.1 (2026-10-18)

First release: one local hub where AI coding sessions work as a team, from Claude Code or any agent that speaks
MCP. One static binary, `huddle` — `huddle radar …` and `huddle kanban …` run the dashboard's and the board's
commands — and one Claude Code plugin with ten slash commands. The binary takes its version from the root
`VERSION` file, cross-compiles with `CGO_ENABLED=0` and `-trimpath` for darwin and linux on amd64 and arm64,
and ships a `release/v<VERSION>.json` manifest of SHA-256 digests the launcher checks.

### The hub

- Three roles: `solo` answers loopback only; `huddle role hub --listen 127.0.0.2:8443 --host hub.example.dev`
  serves paired devices and satellites on one local name; `huddle join-hub <url> <invite>` turns a satellite
  invite into the machine's credential. The first server takes port 18860.
- Invites are one use: `huddle pair --satellite` mints one that lives 10 minutes, a session's join line
  (`/huddle:invite`) one that lives 24 h unless `--ttl` says otherwise. A spent or made-up token is refused.
- Sessions tied to one channel see each other: each reads the other's digest exactly once, and the channel
  lists its members present.
- The Now list carries every session on every machine, grouped by state — needs you, working, idle, stuck
  (10 quiet minutes), ended — and its rows group by machine or project.
- A session waiting on a permission sits in needs you, pushes once to the paired devices, and its prompt holds
  10 minutes.

### Sign-in

- The authenticator code is the main sign-in: six digits every 30 s, set up by scanning one QR (Google
  Authenticator, 1Password, Authy) at the first link sign-in, in Settings, or with `huddle totp setup`; ten
  one-time recovery codes are shown once, and each signs in like a code.
- Five wrong codes in 5 minutes lock code sign-in for 15 minutes; the hub has one owner, so the lock is global.
- After a code sign-in the page offers to save the browser's lock — Touch ID, Face ID, a fingerprint — as a
  passkey; a saved passkey signs in with one touch, "Not now" asks again in 30 days, and the code keeps
  working. `huddle pair` pairs another device.
- `huddle open` and `/huddle:setup` print a fresh one-time link that lives 5 minutes: loopback on a solo
  machine, the hub's own https address on a hub. The signed-out landing page carries the sign-in card, the
  start-a-session tabs and a four-step tour; the first-run checklist ticks four items — authenticator,
  session, channel, budget — from server state and stands down when all four hold.
- A device session rolls forward while used and ends after 7 days idle; removing the device in Settings ends
  it at once. A hub started by hand (`huddle serve`, from a terminal or a service manager) leaves its owner
  key in `<home>/run/owner.key` (mode 0600), so `huddle open` on the same machine still signs the owner in.

### Certificates

- A public name gets its certificate over ACME DNS-01 (Let's Encrypt), renewed on its own.
- A local name gets one from huddle's own CA: a 10-year root, a 7-day intermediate and 12-hour leaves, each
  leaf renewed at two thirds of its life. `huddle trust` installs the root into the OS's and Firefox's trust
  stores; `huddle untrust` removes it.
- `huddle cert --import cert.pem key.pem` installs a pair you already hold (mkcert, a company CA); the hub
  serves it, never renews it, and its log names the expiry once a day from 30 days out.
- `huddle trust --phone` walks a phone through trusting the root — a QR, the downloaded profile and the
  fingerprint to compare — served for 10 minutes and installing nothing on the computer; Settings carries the
  same "Add a phone" QR.

### Radar

- Every request with its model, latency, input, output, cache-read and cache-write tokens and its cost, fed by
  hooks, transcripts and the routers' spool. Watching spends no tokens.
- Twelve views: Overview, Agents, Requests (a per-request inspector), Tools, Timeline, Models, Costs, Usage,
  Blocks, Alerts, Router and Settings.
- Usage charts daily tokens, cost and cache savings by model and project, splits tokens and cost by agent and
  copies each agent's resume command; codex rollouts and opencode sessions count too, tagged by agent.
- Blocks follows the active 5-hour metering window: its tokens, cost, burn rate and projection to its end.
- Budgets stop and lift from Settings — on a hub, from a paired phone; a stopped budget lands in
  `budget-status.json`, which the provider plugins' routers read before serving a provider model.

### Kanban

- One board per repository (`.agents/kanban/kanban.db`): sessions claim cards, and the first claim wins — every
  other pull, from either machine, gets the board's refusal. `set` writes a card's tags; the filters are
  faceted menus with a Clear all.
- A board's key is its repository's origin, so every clone names one board. `huddle pair --satellite` on the
  hub and `huddle join-hub` on the second machine relay that machine's claims and moves to the hub, which
  holds the one database and the audit; a claim is never queued while the hub is away, and `new` stays local.
- The Kanban tab reads the hub's board with its live event stream; a satellite's tab falls back to its local
  copy while the hub is away.

### Agents beyond Claude Code

- MCP over Streamable HTTP at `/mcp/<channel>` and stdio through `huddle mcp`: channels, tasks, claims and
  needs; `publish`, `subscribe` and a long-polling `poll` (up to 60 s); resources and resource subscriptions.
- `huddle harness install <name>` wires codex, gemini, qwen, opencode and crush — the MCP entry plus each
  harness's own lifecycle hooks — so their sessions sit in the Now list beside Claude Code's, badged by agent.
  `huddle harness remove` puts the config back to its previous bytes through the ledger.
- `huddle gateway start` routes other agents' model requests — the Anthropic Messages, OpenAI Chat
  Completions and Responses APIs — by model glob to the upstreams you name, on the first free port in
  18870-18879, forwarding byte for byte while radar records the model, latency and tokens.

### Install

- `/plugin marketplace add MuhmdRaouf/huddle` and `/plugin install huddle@huddle`: the first session's hooks
  start the servers, nothing runs by hand, and every server comes back on the next hook after a crash or a
  reboot.
- The launcher downloads the binary from the GitHub release, checks its sha256 against the committed manifest
  and runs it — no JS runtime needed on the PATH.
- Ten slash commands: setup, invite, join, open, status, stop, kanban-init, kanban-lint, kanban-refresh,
  kanban-export.

### Platforms

- macOS and Linux, amd64 and arm64, one static binary built with CGO off.

### Verify a download

```sh
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify huddle-<os>-<arch> --repo MuhmdRaouf/huddle
```

The binaries are built reproducibly: the same sources and version give the same bytes on any host, and CI
checks that a macOS rebuild matches the Linux one.
