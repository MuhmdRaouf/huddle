# Using Huddle

The walk from a fresh install to a hub two machines share, without reading source code. The
[README](../README.md) says what each part is; this page says what you type. Every command below
runs the `huddle` binary the plugin's launcher downloaded (macOS and Linux, amd64 and arm64).

## First run

1. Install the plugin in Claude Code and restart it:

   ```text
   /plugin marketplace add MuhmdRaouf/huddle
   /plugin install huddle@muhmdraouf
   ```

   The launcher builds the binary of the marketplace checkout's own commit with `go` while no release is
   published (a release download is checked against the committed manifest when one exists). No JS runtime is
   needed on the PATH.

2. Start any session in any project. The first session's `SessionStart` hook starts the hub
   through the launcher; the next `SessionStart`, prompt or tool call brings it back after a
   crash or a reboot, so there is nothing to start or baby-sit by hand. One hook process runs per
   Claude Code event, and huddle's, radar's and kanban's parts ride inside it, each with its own
   deadline.

3. Ask for the dashboard:

   ```text
   /huddle:open
   ```

   It runs `huddle up` (starts the hub when it is down) and prints a one-time link that signs the
   browser in within 5 minutes. The dashboard carries the Now, Channels, Radar and Kanban tabs.

4. Work the first-run checklist at the top of the Now list. Four items tick themselves from
   server state, and the panel hides once all four hold:

   | Item | Where the button goes |
   |---|---|
   | Set up your authenticator app | Settings → Sign-in |
   | Connect a session | Channels |
   | Create a channel | Channels |
   | Set a budget | Settings (Radar) |

`/huddle:status` prints one table — hub, radar, gateway — with running or not and
the address. `/huddle:stop` stops the hub this user started; the next
session hook starts them again. The command table lives in [README · The plugin](../README.md#the-plugin).

## Delegate to a provider model

The same marketplace ships five provider plugins — `zai`, `kimi`, `deepseek`, `minimax`, `qwen` — that run GLM,
Kimi, DeepSeek, MiniMax and Qwen models inside Claude Code. Installing one (`/plugin install
zai-plugin-cc@muhmdraouf`) installs `huddle` with it; then run its setup:

1. Run `/zai:setup`. It takes the key from the environment, the OS keystore or a one-time key page, records
   zai with huddle-router, adds GLM to `/model` and moves the zai agents onto it.

2. Setup points Claude Code at the router in proxy mode: `HTTPS_PROXY` at `http://127.0.0.1:18786`, the shared
   local CA in `NODE_EXTRA_CA_CERTS` (it lives at `~/.agents/ca`, never in a system trust store), loopback
   entries in `NO_PROXY`. A proxy or CA of your own is never touched; routing falls back to
   `ANTHROPIC_BASE_URL` beside it. Picking a GLM model in `/model` sends that request through huddle-router to
   Z.ai with the zai key; every `claude-*` request still reaches Anthropic untouched.

3. Restart Claude Code when the report says so. From then on the router is the one daemon at 18786 for every
   provider you set up: a second provider's setup adds a lane to it and changes nothing else.

The router stays out of the way of a session: it adds under 5 ms to the first byte of a streamed answer and
under 1 ms to each chunk, on a provider lane and on the decrypted `api.anthropic.com` path alike, and with no
request in flight it uses under 0.5% of one core. `TestE2ERouterOverhead` and `TestRouterIdleCPU` hold it to
both budgets.

Turning one provider off with its `/<p>:remove` takes its lane, models and agents back out; the proxy keys stay
while another provider is still set up, and `settings.json` goes back to how it was after the last one. The
plugin's README ([zai](../plugins/zai-plugin-cc/README.md) and its siblings) has the full walk: verified
background jobs, delegation to omp, opencode, pi and zcode, and the cost report.

## Signing in

The ways in and what each is for are in [README · Signing in](../README.md#signing-in). The
walkthrough for the first one:

1. Open the link `/huddle:open` (or `/huddle:setup`) printed. With no authenticator enrolled yet,
   the link lands on the set-up page: scan the QR with your authenticator app (Google
   Authenticator, 1Password, Authy), type the confirming code, and the page shows ten one-time
   recovery codes once — copy or download them. "Later" skips it; Settings → Sign-in offers it
   again. From a terminal, `huddle totp setup` does the same, and `huddle totp off` removes it.

2. Every later visit is six digits on the landing page. A new code arrives every 30 seconds.

3. After a code sign-in the page offers to save this browser's lock — Touch ID, Face ID, a
   fingerprint — as a passkey. "Not now" asks again in 30 days; the code keeps working either way.
   `huddle pair --name phone` pairs another device, and Settings → Devices lists and removes them.

4. Lost everywhere? `huddle open` in a terminal always prints a fresh one-time link (5 minutes)
   as the fallback.

A device session rolls forward while you use it and ends after 7 days unused; one code or touch
starts a fresh one. Removing the device in Settings ends it at once.

## Bring sessions together

A channel is one piece of shared work: every session in it sees the same events, tasks, messages
and knowledge, whatever project each runs in.

1. In a session that is in a huddle (the one that ran `/huddle:setup`, or any session of that
   project), make a join line:

   ```text
   /huddle:invite
   ```

   It prints `/huddle:join 127.0.0.1:<port> --token <id.secret>`, valid 24 hours. Optional
   arguments: `--ttl 2h` (any `s`, `m`, `h`, `d`; `0` never expires), `--single-use`,
   `--can-invite` (lets the joiner invite too). Only the session that started Huddle, its
   project's sessions, or one invited with `--can-invite` may invite.

2. Run that line in the other Claude session, in any project. It trades the invite for that
   session's own credential — kept for its project, so the project's next sessions are in on their
   own — and joins the invite's channel. Each session reads the other's digest exactly once, and
   the channel lists its members present.

3. Give the work order with tasks that wait on each other:

   ```sh
   huddle new "Write the migration guide" --after api-change,docs-review
   huddle start            # takes your next task, marks it doing, prints it
   huddle finish "done: docs/migration.md written"   # marks done, names your next
   huddle depend api "land the API change first"     # a task for them, yours waits on it
   huddle wait-task <id>   # block until a task is done
   ```

   A `start` on a task that still waits refuses and names what it waits on; wait for `task.ready`
   instead of polling. `huddle wait` blocks until an event (exit 0), a message for you (exit 3)
   or a timeout (exit 124).

4. Watch the Now list on the dashboard. Every session on every machine sits in it, grouped by
   state: needs you, working, idle, stuck (10 quiet minutes), ended.

5. Answer approvals from the dashboard or a paired phone. A session waiting on a permission sits
   in needs you, one push when it appears and one escalation after 5 minutes open, and its prompt holds
   10 minutes; an allow or
   a deny settles it once. Snooze from the app for 15 minutes, an hour, or tomorrow at nine.

Messages (`huddle send`, `reply`), shared knowledge (`huddle remember`, `recall`) and the whole
surface live in the huddle skill and `huddle help`.

### Removing a channel

Only the session that started Huddle may remove a channel:

```sh
huddle channel rm old-channel
```

The dashboard's Channels page has the same Remove channel action. A channel with a session
online is refused; let its sessions leave first. The channel's file is not deleted: it moves to
`channels/.removed/<name>-<timestamp>.db` under the hub's data directory. To undo, stop the hub
(`huddle down`), move the file back to `channels/<name>.db`, and start the hub again — the
channel lists with its whole history.

## Reserve files

Two sessions editing the same file is the usual collision. A session says what it is about to
change, and every other session in the channel hears it before it edits:

```sh
huddle reserve internal/huddle/cli internal/huddle/hooks --note "splitting the world type"
huddle reservations            # the channel's live reservations, newest first
huddle release internal/huddle/cli            # one path, or no paths for all of yours
```

- A reservation answers per path: `held`, or `taken by <session> until <time>` — it never blocks.
  Re-reserving your own path extends it; the default hold is 60 minutes, `--ttl` takes 1 to 480.
- The MCP tools are the same words: `reserve`, `release`, `reservations` (paths absolute).
- When a session reaches for a reserved path — a directory reservation covers everything under
  it — one line in its context names the holder and the moment the hold lapses. Nothing is
  refused; the line says `huddle send <session>` first.
- Leaving releases your reservations. A reservation that outlives its session expires on its own.
- Matching is on absolute paths. When both sessions report the same repository (its normalised
  origin), the path inside the repository decides, so another checkout or another machine of the
  same repository is recognised; without an origin, only absolute paths match.

The channel page lists the live reservations with their holder, note and time left.

## Another machine

Two machines work one board and one hub when they share the repository the board belongs to. A
board's key is its repository's origin, so every clone of one repository names one board.

1. On the hub machine, mint the invite (one use, lives 10 minutes):

   ```sh
   huddle pair --satellite
   ```

2. On the other machine (install the plugin there too, so the binary is on its PATH), spend
   the invite:

   ```sh
   huddle join-hub https://hub.example.dev:8443 <invite> --name desk-2
   ```

   The machine keeps its credential and takes the satellite role; a restart links it.

What relays and what stays local:

| What | Where it goes |
|---|---|
| Claims and moves on the shared board | Relay to the hub, which holds the one `kanban.db` and the audit |
| A claim either machine asks for | One claim: the first pull wins, every other pull gets the board's refusal |
| New tickets | Stay local: written to the clone the session sits in, never through the hub |
| Sessions and their state | The satellite's sessions appear in the hub's Now list |
| The Kanban tab | The hub's board, live stream included; the satellite's local copy while the hub is away |

With the hub away, a claim is answered with one plain line and changes nothing — a claim needs
the hub's answer and is never queued.

## Other agents

Huddle's MCP server works with any harness that speaks MCP. For five of them the binary wires
itself in ([README · Other agents](../README.md#other-agents) has what each install edits):

```sh
huddle harness list             # installed, available or absent, per harness
huddle harness install codex    # or gemini, qwen, opencode, crush
huddle harness remove codex     # config back to its previous bytes
```

The names are exactly `codex`, `gemini`, `qwen`, `opencode` and `crush` (exit 4 names nothing
else). Install adds the MCP entry plus the harness's own lifecycle hooks, so its sessions sit in
the Now list beside Claude Code's, badged by agent; a hook posts to the local hub inside a 3
second deadline and never blocks its agent. Every change is recorded in
`~/.config/huddle/harness-ledger.json` (or `$HUDDLE_HOME/harness-ledger.json`) and undone exactly
by `remove`.

Any other MCP client, by hand:

1. Over HTTP, POST JSON-RPC to the channel's endpoint — `http://127.0.0.1:<port>/mcp/<channel>?as=<session>` —
   for the channel's tools: `join`, `send`, `publish`, `reply`, `start`, `finish`, `wait`, the
   task and knowledge operations.

2. Over stdio, run `huddle mcp`. The bridge adds the pub/sub surface:

   - `resources/list` names `huddle://<channel>/events` and one `huddle://<channel>/topic/<topic>`
     per topic the recent history shows.
   - `resources/read` returns the events as one JSON array; add `?after=<seq>` to read only what
     came after a cursor, at most 100 events.
   - `resources/subscribe` gets `notifications/resources/updated` on every event that touches the
     uri; `resources/unsubscribe` stops it.
   - The `poll` tool long-polls for harnesses without subscriptions: `after` (a seq), `topics`
     (globs) and `timeout_ms` (at most 60000); it answers the events after the cursor, or `[]` on
     timeout.

Identify a session with the `HUDDLE_SESSION` environment variable; no Claude Code variable is
needed.

## Read what happened

The Radar tab spends no tokens: it is fed by hooks, the transcripts Claude Code already writes,
and the provider routers' spool. Eleven views:

| View | What it shows |
|---|---|
| Overview | Day cards: the last 24 hours' requests, token sums and cost |
| Agents | Sessions and agents, live transcripts included |
| Requests | Every request, with a per-request inspector |
| Tools | Tool calls per session and agent |
| Timeline | Events in order |
| Models | Rankings per model |
| Costs | The cost series over the window |
| Usage | Daily tokens, cost and cache savings by model and project |
| Blocks | The active 5-hour metering window: tokens, cost, burn rate, projection to its end |
| Alerts | Alert rules and their history |
| Routers | huddle-router's health and the providers it serves |

Budgets and the rest of Radar's settings live in Settings › Radar.

Every request line carries its model, latency, input, output, cache-read and cache-write tokens
and its cost. Usage also splits tokens and cost by agent, copies each agent's resume command, and
counts codex rollouts and opencode sessions, tagged by agent.

Radar's days are UTC dates, the way Anthropic's and the providers' usage pages count them, so a
day's totals in Radar and on those pages turn over at the same moment.

Budgets stop and lift from Settings — on a hub, from a paired phone too. A stopped budget lands
in `budget-status.json` in radar's state dir (`$RADAR_HOME`, else `~/.agents/radar`), which
huddle-router reads before serving a provider model: while its scope (or `total`) is
stopped, the router refuses that provider's models. A missing file, one older than 10 minutes, or
one that does not parse stops nothing — a broken radar never cuts you off, and `claude-*`
requests are never read against it.

## Status bar

`huddle statusline` prints one line for Claude Code's status bar: the project's channel and who is
online, today's spend, and the 5-hour and weekly plan limits off the snapshot Claude Code hands the
statusline command. Parts with nothing behind them are left out, the whole render is bounded well
under a second, and a missing server or an empty snapshot prints nothing — the status line never
blocks a session.

Run `/huddle:statusline --install` (or `huddle statusline --install`) to put it in your status line:
the command lands in Claude Code's `settings.json`, the line you had there keeps running ahead of
huddle's, and `--remove` restores your settings exactly as they were. The Radar tab's Overview shows
the same two windows as its Plan limits card, with each window's reset countdown, fed only by the
snapshots sessions post — huddle never reads a credential to know your plan limits.

## Kanban

One board per repository, in `.agents/kanban/`: each column a subfolder (backlog, ready,
in-progress, review, testing, blocked, done), each ticket one Markdown file, plus `kanban.db` and
the audit. Move tickets only with the `kanban` command — it checks the gates and writes the audit.

1. Create the board:

   ```text
   /huddle:kanban-init
   ```

   `kanban: command not found` on the very first run means the session hook is still downloading
   the binary in the background; `/huddle:status` waits it out, and `kanban` is on PATH from the
   next session.

2. Claim a card and take it end to end. One ticket in progress per actor, and the first claim
   wins — every other pull, from either machine, gets the board's refusal:

   ```sh
   kanban pull --actor w1 --id T-14 --branch t-14-guide --worktree ../wt-t14
   kanban show T-14          # the whole ticket: plan, acceptance, verification
   kanban log T-14 --actor w1 --note 'go test ./... : exit 0'
   ```

3. The review wave. The author hands the card over:

   ```sh
   kanban review T-14 --actor w1 --note 'docs/USAGE.md; check the flags against the code'
   ```

4. The testing wave. A different agent than the author and every holder re-runs every
   Verification command in a clean state:

   ```sh
   kanban test T-14 --actor rev --note 'go test ./... : exit 0'
   kanban done  T-14 --actor rev --note 'guide merged'
   ```

   Anything failing or the diff breaking acceptance: `kanban reject T-14 --actor rev --note '<what
   failed, the command and its output line>'`. A ticket that cannot go on: `kanban block T-14
   --actor w1 --note '<cause>' --on X --until '<condition>'`. Main broken: `kanban stop-line`, later
   `kanban resume-line`.

5. `/huddle:kanban-lint` checks every ticket against the bar (exit 1 when any fails);
   `/huddle:kanban-refresh` rescans now and reloads every open board page; `/huddle:kanban-export`
   writes the board as one self-contained HTML page for sharing (`--out <path>`).

The hub serves every board in process: `kanban start` and `kanban open` name the board's page in
the hub (starting the hub when the huddle home is set up), and the live page shows every folder
change within half a second. A second machine works the same board through the satellite relay —
see [Another machine](#another-machine).

## A hub on a local name

Loopback alone needs nothing else. To serve paired devices and satellites on one name — approvals
from a phone, a second machine — give the hub a name and a certificate
([README · A local name](../README.md#a-local-name)). Requests to a hub on this machine skip any
`HTTPS_PROXY` or `HTTP_PROXY`, so the hub works from inside a session whose provider plugin runs
in proxy mode:

1. Point the name at this machine: dnsmasq, or one line in `/etc/hosts`.

2. Set the role:

   ```sh
   huddle role hub --listen 127.0.0.1:8443 --host hub.example.dev
   ```

3. Trust the root and open `https://hub.example.dev:8443`:

   ```sh
   huddle trust        # installs <home>/pki/root.crt into the OS's and Firefox's trust stores
   huddle untrust      # removes it again
   ```

   The local CA is huddle's own: a 10-year root, a 7-day intermediate, 12-hour leaves renewed at
   two thirds of their life — nothing to run, nothing that expires behind your back.

4. Already hold a pair (mkcert, a company CA)? Install it instead; `huddle cert` says which
   certificate is in use:

   ```sh
   huddle cert --import cert.pem key.pem
   ```

   An imported pair is never renewed; the hub's log names its expiry once a day from 30 days out.
   A public name takes its certificate over ACME DNS-01 instead: `huddle cert --provider <p>`,
   renewed 30 days before expiry.

5. Behind a TLS reverse proxy on this machine (Caddy terminates TLS for the name and forwards to
   the hub's loopback port), give the name alone and skip steps 3 and 4:

   ```sh
   huddle role hub --host hub.example.dev
   ```

   The hub opens no listener of its own; printed links name the proxy's origin
   (`https://hub.example.dev/?code=…`), and the sign-in cookie carries `Secure` when the proxy
   forwards `X-Forwarded-Proto: https` from loopback. The header from any other peer is ignored.
   The role pins the hub's port (18860, or the one already saved) so the proxy always finds it:
   when another program holds it, `huddle up` exits 2 instead of moving to another port. Leaving
   the hub role (`huddle role solo` or `satellite`) unpins it.

6. Bring the phone in: `huddle trust --phone` prints a QR (the page it opens serves for 10
   minutes and installs nothing on the computer); then `huddle pair --name phone`, scan, and save
   the passkey. The dashboard's Settings → Devices carries the same "Add a phone" QR.

7. Keep it running through logouts and reboots with the machine's own service manager:

   ```sh
   huddle service install      # a launchd agent on macOS, a systemd user unit on Linux
   huddle service uninstall
   ```

   A hub started by hand (`huddle serve`, from a terminal or the service) leaves its owner key in
   `<home>/run/owner.key` (mode 0600), so `huddle open` on the same machine still signs the owner
   in. `<home>` is `$HUDDLE_HOME`, else `$XDG_CONFIG_HOME/huddle`, else `~/.config/huddle`.

## Route other models

The huddle-router daemon serves a gateway under `/gateway` that speaks the Anthropic Messages API
and the OpenAI Chat Completions and Responses APIs, so a harness keeps its own protocol and still
shows up in radar ([README · The gateway](../README.md#the-gateway)).

1. Name your upstreams in `$HUDDLE_HOME/gateway.json`:

   ```json
   {
     "routes": [{"match": "glm-*", "upstream": "https://api.z.ai/api/anthropic"}],
     "anthropic": "https://api.anthropic.com",
     "openai": "https://api.openai.com"
   }
   ```

   A model goes to the longest glob that matches it; anything else goes to its API family's
   default. Requests are forwarded byte for byte with their headers — the gateway adds nothing and
   keeps nothing but the route line's metadata.

2. Run the router and read the address:

   ```sh
   huddle router start      # the daemon, if it is not up already
   huddle gateway status    # the /gateway address, or the way to start the router
   ```

3. Give your harness the base URL `http://127.0.0.1:<router port>/gateway` —
   `model_providers.<x>.base_url` in `~/.codex/config.toml` for Codex CLI, the provider's
   `baseURL` for opencode, `providers.<x>.base_url` for Crush. A client can name itself with an
   `X-Huddle-Harness` header, which lands on the route line next to `via: gateway`, so radar
   tells the harnesses apart.

### Coming from 0.0.1

A machine set up with 0.0.1 runs one router per provider (zai on 18787, kimi on 18788 and so on). It
moves to huddle-router on the first start of the daemon: each provider joins
`~/.agents/huddle-router/providers.json`, Claude Code's `HTTPS_PROXY` moves to the daemon's port,
the ledger names it, and the old router's binary copy and watch files go. A router that is still
serving sessions you have open retires — it keeps answering those sessions and exits once they
close, so nothing running is cut off. huddle-router also answers on the 0.0.1 ports 18787–18791
itself, so a session started before the move keeps working, and the old generation has nothing
left to bring back.

A hub a 0.0.1 build left running gives way the same way: the next `huddle up` or session start from
the current build stops it, starts the hub on the same port, and takes a standalone huddle-router's
socket over, so the router never stops answering.

A standalone router whose resident size passes its memory ceiling (1 GiB by default,
`HUDDLE_ROUTER_RSS_CEILING_MB` to move it) hands itself over to a fresh copy; the ceiling never
applies to a router hosted by the hub, whose memory is the hub's to manage.

## Troubleshooting

| Symptom | What to do |
|---|---|
| Signed out after a break | A device session ends after 7 days unused; one code or touch starts a fresh one |
| A port is already taken | The hub walks 18860, then 18861-18869, then an OS pick; `huddle up --port <n>` pins one |
| The link expired | Links live 5 minutes; print a fresh one with `huddle open` or `/huddle:open` |
| Code sign-in locked | Wait the 15 minutes, or sign in with a passkey or a recovery code |
| `huddle is not running` | `huddle status` exits 6; `huddle up` starts it, or just open a session |
| `kanban: command not found` | First run downloads the binary; `/huddle:status` waits it out |
| `502 anthropic unreachable (<code>)` | The code in parentheses names the cause: `ECERT` is a certificate check that failed. huddle-router never routes through another router, so restart sessions started before an update |
| "Claude cannot reach Anthropic through huddle-router" alert | Three `claude-*` failures in a minute raise it and the first request that gets through clears it; the alert names the code and the fix — ECERT means sessions started before an update need a restart, ECONNREFUSED means check `HTTPS_PROXY`, ETIMEDOUT means check the network |
| The hub's memory grows while radar catches up | The hub keeps its heap under a soft 1 GiB limit; `HUDDLE_MEMORY_LIMIT_MB` sets another size in MiB before the hub starts |
| The hub idles at more memory than its data needs | The hub runs at `GOGC=50`, so its heap stays near the data it holds; a `GOGC` you set before the hub starts takes its place |
| A session from before a plugin update | Its watchdog starts the router from the launcher's `current` link, so an old session still brings the router back after plugin updates removed its own build |
| The router runs as its own process beside the hub | Fixed: a start never takes the router from the hub that hosts it. `huddle up` after a plugin update replaces the hub, and the router moves with it |
| Setup in a second project says channel is empty / not in a huddle | Fixed: the project gets its own channel on the running hub, named after the project; run `huddle setup` there again |
| Something looks wrong | The log is `<home>/run/huddle.log`, the pid and port beside it |

The details behind the quick fixes:

- A device session ends after 7 days unused. Remove a device in Settings to end it at once.
- The hub's port choice is saved in `<home>/run/huddle.port`: 18860 first, then the next free port
  in 18861-18869, then one the OS picks.
- Five wrong codes in 5 minutes lock code sign-in for 15 minutes ("Too many tries. Wait 15 minutes
  or use a passkey."). The hub has one owner, so the lock is global.
- The first kanban run downloads the binary in the background; `kanban` is on PATH from the next
  session.

See also: [README.md](../README.md) · [docs/TESTPLAN.md](TESTPLAN.md) · [CHANGELOG.md](../CHANGELOG.md)
