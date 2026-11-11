# Changelog

## 0.0.3

An admin panel for the router, a dashboard at your own domain, and one command to update.

### Dashboard

- The router's page is an admin panel: a sidebar with 14 pages (Overview, Sessions, Agents, Requests, Errors, Models, Providers, Usage, Projects, Worktrees, Jobs, Plugins, Harnesses, Router), live badges, `⌘K` search, sortable tables, a detail sheet per session, request and project, and an Auto, Light and Dark switch. It loads nothing from outside the router.
- Sessions show their title, path, branch, state (working, waiting and for how long, idle), context fill, prompt cache warmth and subagents, read from Claude Code's session logs only while a page is open.
- Each request is credited to its agent through Anthropic's `request-id`, with time to first token, duration, cache tokens and stop reason. Models get 30-second buckets, p50 and p95 latency and output speed. Errors group by status; a 499 never counts.
- Plugins, health checks and the coding CLIs' readiness show on the page, with the next step to copy. The Claude plan windows show when Anthropic's replies carry them.
- The wiring and plugin parts show "Checking…" before their first check instead of a false failure.
- `huddle add-dashboard-host <host>` and `/huddle:add-dashboard-host` let a domain of yours open the dashboard's page and its data, and nothing else; `remove-dashboard-host` takes it back out. The hosts live in `~/.config/huddle/config.toml`.

### Update

- `/huddle:update` installs the binary of the version the plugin carries, checks it against `SHA256SUMS`, switches the link and hands the router over without dropping a connection. It leaves `settings.json` alone.

### Docs

- The README covers every command, the dashboard and updates; each provider plugin has its own banner and README.

## 0.0.2

A live page for the router, and route events for every request.

### Router

- The router serves a live page at `http://127.0.0.1:4835/`: health, Claude Code's wiring, one card per lane, the 200 latest model requests, requests in flight, and delegated jobs. It keeps everything in memory, shows no prompts and no keys, and does nothing while no page is open.
- `/huddle-router/state` answers the page's data as JSON; `/huddle-router/live` streams it.
- Model requests that reach the router through Claude Code's decrypted connection write their route events, so `huddle provider <p> usage` counts them. Before this, only plain-HTTP requests did.

## 0.0.1

First release of huddle: one local router for the provider models, and the delegation words that hand work to other CLI harnesses.

### Router

- One router daemon, huddle-router, on 127.0.0.1:4835: one lane per set-up provider (zai, kimi, deepseek, minimax, qwen), and `claude-*` requests reach Anthropic untouched.
- A provider request forwards unchanged except the destination, the key swap and the hop-by-hop headers; a message-threads beta answers 400, and Qwen's `context_management` field is the one body edit.
- The key comes from the environment or the OS keystore (macOS Keychain, the Secret Service on Linux); it never travels in a chat, an argument or an output line.
- `huddle provider <p> usage` reports requests, tokens and estimated cost per model, from one route line a day.
- The daemon re-reads `providers.json` every 2 s, so enabling and removing a provider needs no restart. A binary update or a resident size past 1 GiB (`HUDDLE_ROUTER_RSS_CEILING_MB`) hands the socket to a new process; the requests in flight drain.
- Setup points Claude Code at the router through `HTTPS_PROXY` and the local CA in `~/.agents/ca`; a proxy or CA of your own is never touched.

### Providers

- The five provider plugins (zai, kimi, deepseek, minimax, qwen) each set up with one command and add their models to `/model`; removing one takes its lane, its models and its key back out.
- The five provider plugins delegate to GLM, Kimi, DeepSeek, MiniMax and Qwen models through the same words.

### Delegation

- `huddle run <harness> <task>` hands work to any CLI harness in its own worktree: the driver runs detached, `huddle result` shows the report and the diff, `--accept` lands it as a commit, `--return` sends it back with feedback, `huddle cancel` removes the worktree and branch.
- `--background` returns at once; `--setup`, `--gate`, `--scope` and `--forbid` attach checks whose evidence follows the result.
- `huddle setup` reports every harness's readiness before you delegate; `--smoke` runs one real turn.

### Install

- Install the plugin in Claude Code and run `/huddle:setup`: it gets the binary for this OS and CPU — the release download, checked against `SHA256SUMS`, or a cargo build of the checkout while no release is published — creates the local CA, starts the router and patches `settings.json`. Nothing changes before it runs.
- One restart, then every session: the session-start hook brings the router back after a crash or a reboot.
- macOS and Linux, amd64 and arm64. On Windows, huddle runs inside WSL with the Linux binary.

### Verify a download

Download `SHA256SUMS` beside the binary from the release page and run `sha256sum -c SHA256SUMS --ignore-missing` (on macOS, `shasum -a 256 -c SHA256SUMS --ignore-missing`).
