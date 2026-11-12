# Usage

How to set huddle up, what every word does, what each file holds, and where to look when something fails.

## Install

1. Install the plugin in Claude Code:

   ```text
   /plugin marketplace add MuhmdRaouf/huddle
   /plugin install huddle@muhmdraouf
   ```

2. Run `/huddle:setup`. It gets the `huddle` binary for this OS and CPU — the release download, checked against `SHA256SUMS`, or a cargo build of the plugin checkout while no release is published — and installs it as `~/.local/share/huddle/bin/huddle-<version>` with the link `~/.local/share/huddle/bin/huddle` to it, and links `~/.local/bin/huddle` to that, so a bare `huddle` works in a terminal whose `PATH` has `~/.local/bin` (setup says so when it does not, and carries on when the folder cannot be written). It then creates the local CA in `~/.local/share/huddle/ca/`, starts the router and patches `~/.claude/settings.json` with the proxy settings. Nothing changes before it runs. An existing 0.0.4 install runs `huddle uninstall` first, then `/huddle:setup` with 0.0.5; nothing moves from `~/.agents`. An install that has the five old provider plugins runs `/huddle:setup` again: it names each one to remove in `/plugin`, and the providers' commands are part of the `huddle` plugin.

3. Restart Claude Code once so it reads the proxy settings. From here on the session-start hook keeps the router alive: a later session finds it down and brings it back.

4. Add a provider, for example Z.ai GLM. In Claude Code:

   ```text
   /huddle:zai:setup
   ```

   With no stored key, a one-time key page opens on `127.0.0.1`: paste the key there. In a terminal:

   ```sh
   huddle provider zai
   ```

   The guide asks for the key with the echo off, checks it with the provider, enables the lane and adds the models to `/model` and the agents to `~/.claude/agents/`. A pipe works too: `huddle provider zai key`. The four other providers work the same way. A key the provider refuses is never stored and never replaces a stored one.

A provider model picked in `/model` sends that request through the router to the provider; every `claude-*` request still reaches Anthropic untouched. The router forwards a provider request unchanged except the destination, the key swap and the hop-by-hop headers; a message-threads beta answers 400, and Qwen's `context_management` field is the one body edit.

## Words

One row per word, with the exit codes it answers. An unknown word or action is never echoed back. `<p>` is `zai`, `kimi`, `deepseek`, `minimax` or `qwen`.

| Word | What it does | Exits |
|---|---|---|
| `huddle setup [harness]` | Every harness's readiness table, or one harness's report; `--smoke` runs one real turn, `--json` is for scripts. | `0` ready, `6` not ready, `2` unknown harness, `1` error |
| `huddle` | Where you stand: the router, each provider's key and lane, and the next command to run. `huddle help` lists every word. | `0` |
| `huddle setup --install` | The local CA, the router start, the settings patch, the `~/.local/bin/huddle` link, one restart line. | `0`, `1` |
| `huddle run <harness> <task>` | Submits the task, runs the harness in the current folder or the one `--dir <path>` names (a second write job in a folder is refused), starts the harness's detached driver, waits and prints the result; `--background` returns at once, `--readonly`/`--exec`/`--write` set the access, `--setup`, `--gate`, `--scope` and `--forbid` add checks. | `0` review or clean, `3` failed, `2` usage or unknown harness, `6` binary missing, `1` error |
| `huddle status [<id>]` | The job table for this repository, or one job in full with its log tail. | `0`, `4` unknown id, `2` usage, `1` error |
| `huddle result <id>` | One job's review packet; `--diff` adds the patch, `--accept` lands it as a commit (`--no-commit` leaves the working tree), `--return <feedback>` sends it back. | `0`, `4` unknown id, `2` usage, `1` error |
| `huddle cancel <id>` | Stops a job; the folder it ran in stays as it is. | `0`, `2` wrong state, `4` unknown id, `1` error |
| `huddle router serve` | The router daemon in this foreground process; `--takeover` takes a running daemon's socket. | `0` stopped, handed over or already served, `1`, `2` bad port |
| `huddle router start` | One daemon for this home, detached. | `0`, `1`, `2` bad port |
| `huddle router status` | What answers on the port, with the record's pid and version and the dashboard hosts; `--json` is for scripts. | `0` running, `6` not running, `2` bad port |
| `huddle router log` | The last 40 event lines from `router-events.log`, oldest first; `--since <n>m\|<n>h` limits by time and reads the older files, `--requests` prints request lines from `router.log`, `--all` merges both. Reads files only, so no router is needed. | `0`, `2` usage |
| `huddle router stop` | One SIGTERM to the recorded pid, then a wait for the port to go. | `0` stopped, `6` not running, `1` past 10 s, `2` bad port |
| `huddle add-dashboard-host <host>` | Lists a bare host name in `~/.config/huddle/config.toml`; the router reads the dashboard's page, state and live paths for it within 2 s. A repeat changes nothing. | `0`, `2` not a bare host name or usage, `1` |
| `huddle remove-dashboard-host <host>` | Takes the host back out; one that is not listed changes nothing. | `0`, `2`, `1` |
| `huddle provider` | Lists the providers. | `0` |
| `huddle provider <p>` | The guided setup: a terminal gets the menu and the masked key prompt, a pipe or a script gets the next steps printed. Without a key it never waits. | `0` ready, `6` not ready, `1` |
| `huddle provider <p> key` | Reads the key from hidden stdin into the keystore. | `0`, `2` empty or invalid, `1` |
| `huddle provider <p> setup` | Checks the key with the provider, enables the lane, writes its agents, starts the router, reports ready or not; with no key it opens the one-time key page. | `0` ready, `6` not ready, `1` |
| `huddle provider <p> remove` | Takes the lane, the `/model` entries, the agents and the stored key back out; the other providers keep serving. | `0`, `1` |
| `huddle provider <p> usage` | Requests, tokens and estimated cost per model, today and the last 7 and 30 days. | `0` |
| `huddle hook <event>` | Claude Code's `session-start` and `prompt-submit`: starts the router, re-applies the provider wiring, refreshes the agent files. Never blocks a session. | `0` |
| `huddle uninstall` | Undoes every patch huddle made, the `~/.local/bin/huddle` link and the provider agents, one line per step. | `0`, `1` |

Inside Claude Code the plugin's commands run the words for you. The launcher answers `run /huddle:setup first` with exit 127 while no binary is installed.

## Files

| Path | What writes it | Who reads it |
|---|---|---|
| `~/.config/huddle/config.toml` | `setup --install` writes the commented defaults once; `add-dashboard-host` and `remove-dashboard-host` edit `dashboard.hosts` in place, keeping your comments; you edit `[history] keep_days` (90 by default) | The daemon |
| `~/.config/huddle/providers.toml` | `provider <p> setup` and `remove`: the enabled providers, edited in place | The daemon, re-read every 2 s |
| `~/.config/huddle/harnesses.toml` | You: one entry per extra CLI | `setup`, `run`, `status`, the driver |
| `~/.local/share/huddle/bin/` | The launcher on `/huddle:setup`: `huddle-<version>` and the `huddle` link to it | Every word and hook |
| `~/.local/bin/huddle` | `setup --install`: a link to `~/.local/share/huddle/bin/huddle`; it never replaces a file that is not its own link | Your terminal's `PATH`; `uninstall` removes it |
| `~/.local/share/huddle/ca/` | `setup --install`, or the daemon's first start: `huddle-ca.pem` and `huddle-ca-key.pem`, the local CA | Claude Code through `NODE_EXTRA_CA_CERTS`, the daemon's leaf signer |
| `~/.cache/huddle/` | The launcher: the download in flight and `build.log` of a local build | You, when a build fails |
| `~/.local/state/huddle/huddle.db` | The daemon: one row per finished request (no prompt, key or header value), pruned past `keep_days` | `provider <p> usage`, the dashboard's history views |
| `~/.local/state/huddle/router.log` | The daemon: every line, rolled at 4 MB, five older files (`.1` to `.5`) | `router log --requests`, you when the router misbehaves |
| `~/.local/state/huddle/router-events.log` | The daemon, the stop word and the hooks: event lines only, rolled at 1 MB, five older files | `router log` |
| `$XDG_RUNTIME_DIR/huddle/` (else `~/.local/state/huddle/run/`) | The daemon: `router.toml` (pid, port, version, binary, start time), `router.lock`, `handover.sock` | `router start`, `status`, `stop` |
| `~/.local/state/huddle/settings-ledger.toml` | `setup --install`: what the proxy patch changed | `uninstall` |
| `~/.local/state/huddle/settings.json.pristine` | `setup --install`: settings.json as the user had it | `uninstall`, byte for byte |
| `~/.local/state/huddle/providers/<p>-ledger.toml` | `provider <p> setup`: the `/model` entries it added | `provider <p> remove`, `uninstall` |
| `~/.local/state/huddle/jobs/<repo-slug>/<id>/` | `run` and each job's detached driver: `job.toml`, task, transcript, checks | `status`, `result`, `cancel` |
| `~/.claude/settings.json` | `setup --install` and `provider <p> setup`: the proxy env and the `/model` entries | Claude Code, at startup |
| `~/.claude/agents/` | The session-start hook: one managed file per Claude family; `provider <p> setup`: the provider's two model agents, removed by `remove` | Claude Code's agent panel |

## Environment variables

| Variable | What it does |
|---|---|
| `HUDDLE_ROUTER_PORT` | The port the router serves, `4835` by default. Plain digits 1-65535; setup writes the matching URL, so the two always agree. |
| `HUDDLE_CONFIG_HOME` | huddle's own config folder, `~/.config/huddle` by default. Else `$XDG_CONFIG_HOME/huddle`. |
| `HUDDLE_DATA_HOME` | huddle's own data folder (`bin/`, `ca/`), `~/.local/share/huddle` by default. Else `$XDG_DATA_HOME/huddle`. |
| `HUDDLE_STATE_HOME` | huddle's own state folder (`huddle.db`, log, ledgers, `jobs/`, and `run/` when no runtime folder is set), `~/.local/state/huddle` by default. Else `$XDG_STATE_HOME/huddle`. |
| `HUDDLE_CACHE_HOME` | huddle's own cache folder, `~/.cache/huddle` by default. Else `$XDG_CACHE_HOME/huddle`. |
| `HUDDLE_RUNTIME_DIR` | The folder for `router.toml`, `router.lock` and `handover.sock`. Else `$XDG_RUNTIME_DIR/huddle`, else `run/` under the state folder (macOS sets no `XDG_RUNTIME_DIR`). |
| `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` | Move that kind's parent folder when the matching `HUDDLE_*_HOME` is unset; huddle uses `<value>/huddle`. An empty or relative value counts as unset. |
| `XDG_RUNTIME_DIR` | The parent of huddle's runtime folder, when `HUDDLE_RUNTIME_DIR` is unset. A relative value counts as unset. |
| `HUDDLE_ROUTER_ANTHROPIC_URL` | Where `claude-*` requests go, Anthropic by default. Must name a scheme and a host; the daemon checks it before it binds. |
| `HUDDLE_ROUTER_RSS_CEILING_MB` | The resident size in MiB at which the daemon hands its socket over, `1024` by default. A value that is not a positive integer keeps the default. |
| `HUDDLE_DOWNLOAD_BASE` | Moves the release URL the launcher downloads from. |
| `HUDDLE_SOURCE` | Names the checkout the launcher builds with cargo while no release is published. |
| `CLAUDE_CONFIG_DIR` | Moves Claude Code's config directory, `~/.claude` by default. Setup patches `settings.json` and `agents/` there. |
| `<P>_API_KEY` | The provider key setup checks before the keystore: `ZAI_API_KEY`, `KIMI_API_KEY`, `DEEPSEEK_API_KEY`, `MINIMAX_API_KEY`, `QWEN_API_KEY`; qwen also takes `DASHSCOPE_API_KEY`. |
| `HUDDLE_<ID>_BIN` | One harness's binary, for example `HUDDLE_CURSOR_AGENT_BIN`. Checked before the harness's own variable, `PATH` and the fixed install paths. |
| `ZCODE_CLI` | zcode's own binary override. |

## Reading router.log

`huddle router log` prints the event lines below from `router-events.log`; `router.log` has them among the request lines. There are three line shapes for an Anthropic failure.

- A failed request: `... anthropic-pass error ETIMEDOUT ms=16333 phase=connect peer=160.79.104.10:443 conn=fresh dns_ms=4`. The phase is the step that failed: `dns` (the name lookup), `connect` (the TCP dial), `tls` (the handshake), `send` (the request going out), `headers` (waiting for the answer), `body` (the answer broke part way). The `*_ms` fields are the finished dial steps. `conn=reused` is a pooled socket.
- The path is failing (three failures within a minute): `huddle-router: Anthropic path failing (ETIMEDOUT at connect to 160.79.104.10:443): check the network, the firewall or the VPN`. Every 15 s after it, `huddle-router: probe api.anthropic.com dns=ok(3 ms, 160.79.104.10) connect=timeout(5000 ms)` shows whether DNS and a bare connect still work.
- The path is back: `huddle-router: Anthropic path recovered after 6m 56s; 53 requests failed; now via 160.79.104.12:443 (fresh)`.

Lifecycle lines say why the router changed: `start v0.0.6 pid=91414 port=4835 by=prompt-submit`, `handing over to pid=91414 (asked by prompt-submit)`, `took over from pid=6540`, `exit pid=6540 reason=handover`. Every request to `/huddle-router/*` except the dashboard's own reads is one line with its method, path and status. Header values, bodies and query strings are never logged.

## Troubleshooting

- **The router will not start.** `huddle router status` says what answers on the port, and `~/.local/state/huddle/router.log` names the failure.
- **Requests fail with 407 or 502.** The settings patch is gone or stale. Run `/huddle:setup` again, then check `huddle router status`.
- **A key is refused.** The report names the status the provider sent. Check the key at the provider's key page and run `/huddle:<p>:setup` again; the stored key stays as it was.
- **`huddle` is not found.** Put `~/.local/bin` on your `PATH`, or run `~/.local/share/huddle/bin/huddle`.
- **Restore the settings.** `huddle uninstall` stops the router, takes the proxy env and the `/model` entries back out, and puts `settings.json` back the way it found it, one line per step.
