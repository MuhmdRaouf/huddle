# Changelog

## 0.0.6

The five providers are part of huddle, and a key goes in from a page or a terminal guide.

### Providers

- The zai, kimi, deepseek, minimax and qwen commands live in the `huddle` plugin: `/huddle:<p>:setup`, `/huddle:<p>:remove` and `/huddle:<p>:usage`. The marketplace lists one plugin.
- `/huddle:<p>:setup` writes the provider's two agents into `~/.claude/agents/`, named by the bare model (`glm-5.3`, `glm-5.3-flash`). `remove` and `huddle uninstall` delete only huddle's own agent files. A provider you never set up shows no agents.
- `/huddle:setup` names each old provider plugin it finds and the command that removes it. It never touches Claude Code's plugin files.
- A key is checked with the provider before it is stored. A refused key never replaces a stored one, and turns nothing on.
- When the key check gets an answer it does not know, setup names the HTTP status the provider sent. "Could not reach" means no answer came back.
- The catalog has no key file field; keys live in the OS keystore.

### Keys

- With no stored key, `/huddle:<p>:setup` opens a one-time key page on `127.0.0.1`. It needs its token, host and origin, loads nothing from outside, and ends after the key is saved or its time runs out. A second setup stops the first page, and never signals a process that is not huddle's helper.
- `huddle provider <p>` guides a terminal: a menu, a masked key prompt, backspace, Ctrl-C that stores nothing and restores the terminal, and three refused keys end with exit 6. Off a terminal it prints the next steps and never waits. A piped key is stored as before.

### Terminal

- `huddle` alone shows where you stand: the router, each provider's key and lane, and the next command. `huddle help` lists every word.
- `huddle` is linked into `~/.local/bin/huddle` on setup, never over a file that is not its own link, and removed by `huddle uninstall`. Setup says when `~/.local/bin` is not on `PATH`, and carries on when the folder cannot be written.
- An unknown word or action is answered with the valid ones and is never echoed back, in case it was a key.

### Dashboard

- The Plugins page lists the five providers, without an installed column, and the sidebar badges read as ratios with the good part green.

### Tests

- A test home stops the key page helpers it started when it is dropped.

### Upgrading from 0.0.5

Remove the five provider plugins (zai, kimi, deepseek, minimax and qwen) in `/plugin`, update huddle and run `/huddle:setup` again. Then use `/huddle:<p>:setup`. A key already in the keystore is kept.

## 0.0.5

huddle keeps its files in the XDG folders and remembers every request.

### Layout

- Files move out of `~/.agents`: config in `~/.config/huddle/` (`config.toml`, `providers.toml`, `harnesses.toml`), data in `~/.local/share/huddle/` (`bin/`, `ca/`), state in `~/.local/state/huddle/` (`huddle.db`, `router.log`, the ledgers, `jobs/`).
- The router's lock, port and handover socket live in `$XDG_RUNTIME_DIR/huddle/`, or `~/.local/state/huddle/run/` when it is unset (macOS). The release download and the setup build log live in `~/.cache/huddle/`.
- `HUDDLE_CONFIG_HOME`, `HUDDLE_DATA_HOME`, `HUDDLE_STATE_HOME`, `HUDDLE_CACHE_HOME` and `HUDDLE_RUNTIME_DIR` point at huddle's own folder for a kind; the matching `XDG_*` variable moves it when those are unset. A relative path in any of them is ignored, as the XDG Base Directory spec says. `HUDDLE_HOME` and `HUDDLE_ROUTER_HOME` are gone.
- Every file huddle owns is TOML, and every folder and file is created on demand. The CA files are `huddle-ca.pem` and `huddle-ca-key.pem`.
- `setup --install` creates `config.toml`, `providers.toml` and `harnesses.toml` with their defaults when they are missing, and never overwrites one.
- Folders are created 0755 and files 0644. The CA folder stays 0700; the CA key, the settings ledger and `settings.json.pristine` stay 0600.
- `add-dashboard-host`, `remove-dashboard-host` and the provider setup and remove words edit `config.toml` and `providers.toml` in place: your comments, blank lines and key order stay.
- Upgrading from 0.0.4: run `huddle uninstall` first, then `/huddle:setup` with 0.0.5. Nothing is migrated.

### History

- Every finished request is one row in `huddle.db`: time, session, agent, model, lane, status, tokens, cache, time to first token, duration, stop reason and upstream id. No prompt, key or header value is stored. The events files are gone.
- Rows older than `[history] keep_days` in `config.toml` (90 by default) are pruned once a day. Rows without usage are filled from Claude Code's session logs.
- `GET /huddle-router/history` answers range buckets (15 min, 24 h, 7 d, 30 d), session and project totals and a filtered, paged search over 24 h to 90 d.
- Claude models are priced at API list rates and shown as API-equivalent.

### Delegation

- A job runs in your folder or the one `--dir <path>` names. huddle makes no worktree and no branch; `--diff`, `--accept`, `--return` and `/huddle:cancel` work on that folder. A second write job in the same folder is refused.
- A readonly job that changes a file fails.
- A job's record is `job.toml` under the state folder.

### Dashboard

- Overview, Usage and Models switch between 15 min, 24 h, 7 d and 30 d; past 15 min they read `huddle.db`.
- Sessions and Projects show 30-day totals and trends beside the live view.
- Requests and Errors search the whole history over 24 h to 90 d, filtered by model, lane, session, agent, project and status, 100 rows a page, each with its estimated cost.

## 0.0.4

The dashboard reads sessions, agents and tokens the way Claude Code really writes them.

### Dashboard

- A session is working while a request is in flight or a tool runs, for up to 10 minutes; a turn the user interrupts is waiting. Subagents follow the same rule, so running agents show as running.
- A session lists only the subagents active in the last 2 hours, at most 50, which keeps each snapshot near 45 KB instead of 340 KB.
- A context past 200k tokens has the 1M limit, so the fill never reads over 100%.
- Claude's tokens, cache tokens and stop reason come from the session logs by `request-id` and count in the request rows, the charts, the model table, output speed and the lane totals.
- A plugin installed from the marketplace's git checkout shows the release its `dist/run` carries, so the binary check no longer asks for `/huddle:update` by mistake.
- A router hop under a millisecond reads `<1 ms`.

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
