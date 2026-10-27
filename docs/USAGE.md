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
   needed on the PATH. The build runs on the first hook after the install or a plugin update, each commit
   building once, so `go` must be on the machine. The launcher keeps three builds and hooks never wait for
   one: the hook that finds no build starts it detached and exits at once. Released binaries replace source
   builds once huddle has a second user.

2. Start any session in any project. The first session's `SessionStart` hook starts the hub
   through the launcher; the next `SessionStart`, prompt or tool call brings it back after a
   crash or a reboot, so there is nothing to start or baby-sit by hand. One hook process runs per
   Claude Code event, and huddle's, radar's and kanban's parts ride inside it, each with its own
   deadline. Huddle's part reports the event to the hub: a tool call carries the tool's name and
   its short target (a Bash command's first line, the file path, or the MCP tool's name), cut to
   120 characters, and nothing else of the tool's input; a typed prompt carries its first line,
   cut the same way; and every report carries the session's transcript path and its messaging
   inbox (the Unix socket Claude Code hands its hooks), both kept on the hub, never sent to the
   web. The session's messaging token stays in the OS keystore under the session's own account
   until the session ends, when it is deleted.

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

The router is its own process, `huddle router serve`, beside the hub's `huddle serve`: one of each per user. A hub
restart, update or crash never touches it, so a Claude answer streaming through it keeps going. `huddle up`
starts the router and then the hub, and prints one line for each:

```text
huddle is up at http://127.0.0.1:18860 · pid 41210
huddle-router is up at http://127.0.0.1:18786 · pid 41188
```

`huddle status` ends with the same router line, and the hub's `/health` answer carries `pid` and
`router: {running, pid, port}`.

The router stays out of the way of a session: it adds under 5 ms to the first byte of a streamed answer and
under 1 ms to each chunk, on a provider lane and on the decrypted `api.anthropic.com` path alike, and with no
request in flight it uses under 0.5% of one core. `TestE2ERouterOverhead` and `TestRouterIdleCPU` hold it to
both budgets.

A lane can fall back to another of its provider's models when one keeps failing. List them under the
provider in `~/.agents/huddle-router/providers.json`:

```json
{"providers": ["zai"], "lanes": {"zai": {"fallback": ["glm-5.3-flash"]}}}
```

After three 5xx, 429 or dropped answers in a row for one model, the lane sends that model's requests to the
next model on the list for 60 s, then tries the model again. A 2xx starts the count over. The router logs
`fallback glm-5.3 -> glm-5.3-flash (3 x 503)`, and Radar's Requests table marks each such request
`from glm-5.3`. The router re-reads the file within 2 s; a lane with no list never falls back, and `claude-*`
never does.

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

The hub keeps one time zone, taken from the first browser that signs in. Settings → This hub shows it
as "Time zone: Europe/Istanbul (from your first sign-in)"; pick another zone or UTC there. Every
database stores UTC whatever the zone says.

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
   a deny settles it once. Claude's questions open as cards; pick or type each answer and send
   them, or answer in the terminal. Snooze from the app for 15 minutes, an hour, or tomorrow at nine.

Messages (`huddle send`, `reply`), shared knowledge (`huddle remember`, `recall`) and the whole
surface live in the huddle skill and `huddle help`.

The dashboard's Insights card reads the hub's own tips and each session's. "Tips to my sessions"
lets a session's own hooks ask for tips about it: with it on, a prompt or a start adds one line to
the session's context when the hub has a tip about it, and each tip reaches a session once. A
harness that only speaks MCP reads the same tips from the resource `huddle://insights`. "Not
useful" on a tip mutes its rule for 30 days, and "Forget what you learned" deletes insights.json,
which resets both switches.

The tips:

- You allowed one prompt five times in a week and waited for it each time; an allow rule would
  skip the prompt.
- One command ran four times over in a session; a narrower run is faster while fixing.
- A tool keeps failing in a session; the card quotes its error.
- A model lane fails more than one request in twenty over a day; the card names the share and
  the status that failed most.
- One ask has waited on you far longer than the rest; pair your phone to answer from it.
- One day's sessions together ran past 10 hours; the card names the day and the length.
- A subagent started over 20 minutes ago and has no stop event; the card names it and its age.

### Reach an idle session

A message from the dashboard, a phone or the channel chat reaches an idle session at once only
when its Claude Code was started with the development-channels flag:

```sh
claude --dangerously-load-development-channels server:plugin:huddle:huddle
```

Without the flag Claude Code drops channel pushes silently, so the hub holds the message and the
session gets it on its next prompt. The hub reads each session's command line to tell: the
composer says "Delivers now" for a session started with the flag, and "Delivers on the next
prompt" for any other. Huddle never restarts a session or wraps `claude`; add the flag yourself
when you want idle sessions to wake.

A session on the hub's own machine gets the owner's message through its inbox socket first. Four
routes answer an owner's message: `inbox` delivers at once through the session's inbox socket,
`held` waits for your approval in the session's terminal and expires after 5 minutes, `now`
arrives through the channel, and `next_prompt` arrives when you next prompt the session.

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

Huddle's MCP server works with any harness that speaks MCP. For eight of them the binary wires
itself in ([README · Other agents](../README.md#other-agents) has what each install edits):

```sh
huddle harness list             # installed, available or absent, per harness
huddle harness install codex    # or gemini, qwen, opencode, crush, omp, pi, zcode
huddle harness remove codex     # config back to its previous bytes
```

The names are exactly `codex`, `gemini`, `qwen`, `opencode`, `crush`, `omp`, `pi` and `zcode` (exit 4
names nothing else). Install adds the MCP entry plus the harness's own lifecycle hooks, so its sessions sit in
the Now list beside Claude Code's, badged by agent; a hook posts to the local hub inside a 3
second deadline and never blocks its agent. For codex, install also adds an opt-in model
provider, `huddle`, that sends the Responses API through the router's `/gateway/v1` over plain
HTTP on loopback with your own `OPENAI_API_KEY`: run `codex -c model_provider=huddle` to use it.
Your default provider and `openai_base_url` stay as they are. For omp, install writes the
`huddle` server into `~/.omp/agent/mcp.json`, which omp reads ahead of the servers it imports
from Claude Code and Codex, and no hooks. It also adds `providers.anthropic` to
`~/.omp/agent/models.yml`, between `# huddle:begin` and `# huddle:end`: `baseUrl` is the
router's `http://127.0.0.1:<router port>/gateway` and the header `X-Huddle-Harness: omp` names
omp in radar. omp's Anthropic requests then go through the gateway over plain HTTP on loopback
with your own key or login, and need the router up. A `models.yml` that already configures
`anthropic` stays as it is: install refuses it and says why. For opencode, install writes the
`huddle` server under `mcp` in `~/.config/opencode/opencode.json` and, in the same file,
`provider.anthropic.options`: `baseURL` is the router's
`http://127.0.0.1:<router port>/gateway/v1` and the header `X-Huddle-Harness: opencode` names
opencode in radar. opencode's Anthropic requests then go through the gateway over plain HTTP on
loopback with your own key, and need the router up. An `opencode.json` that already configures
`anthropic` stays as it is, the same way. For pi, install writes the `huddle` server into
`~/.pi/agent/mcp.json` with a 120 second request timeout, and no hooks. It also adds
`providers.anthropic` to `~/.pi/agent/models.json`: `baseUrl` is the router's
`http://127.0.0.1:<router port>/gateway` and the header `X-Huddle-Harness: pi` names pi in
radar. pi keeps its own anthropic models, and their requests go through the gateway over plain
HTTP on loopback with your own key, and need the router up. A `models.json` that
already configures `anthropic` stays as it is, the same way.
For zcode, install writes the `huddle` server under
`mcp.servers` in `~/.zcode/cli/config.json`, and no hooks. It also adds two rules to
`~/.zcode/v2/provider_config.json`, for the Z.ai Coding Plan providers (individual and team):
`baseUrl` is the router's `http://127.0.0.1:<router port>/gateway` and the header
`X-Huddle-Harness: zcode` names zcode in radar. zcode's GLM requests on those plans then go
through the gateway over plain HTTP on loopback with your own login, and the gateway's built-in
GLM route forwards them to `https://api.z.ai/api/anthropic`; they need the router up. The start,
off-peak, highspeed and BigModel plans speak other upstreams, so they stay direct and radar does
not see them. A rule of your own for either coding plan stays as it is: install refuses it and
says why. Every change is recorded in
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
| Overview | Day cards: the last 24 hours' requests, token sums and cost; the four graphs below |
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

Budgets and the rest of Radar's settings live in Settings › Spending.

Every request line carries its model, latency, input, output, cache-read and cache-write tokens
and its cost. Usage also splits tokens and cost by agent, copies each agent's resume command, and
counts codex rollouts and opencode sessions, tagged by agent.

A model the price list does not know costs its family's rate (`claude-opus-9-9` prices as the newest
opus), and the Costs view marks that cost with `≈`.

Radar's days follow the hub's time zone: the day card, the usage report, the Overview's graphs and
"Spend today" start at midnight there, and every time on the Radar pages reads in that zone. With
the zone at Europe/Athens, a request at 00:30 UTC on the 25th counts for the 25th. Storage stays
UTC. A zone this machine cannot load counts UTC days. The Blocks view keeps UTC, the calendar
Anthropic's and the providers' usage pages count in, and says so on the view; so does the
self-check.

The Overview draws four graphs, each a series in the hub's zone refreshed once a minute:

1. Requests per hour, today.
2. Tokens by kind (input, output, cache read, cache write) per day, the last 7 days.
3. Cost per day, the last 30 days.
4. Cost per session, today's 10 costliest, each named by its title and machine.

The first and the fourth read `GET /api/radar/api/usage/today` on the hub (`/api/usage/today`
on radar's own port); the other two read `/api/usage?days=30`. A phone stacks them in one column.

Above the graphs, the day card shows two figures for the day so far in the hub's zone, refreshed
once a minute; its title names the zone, "Today, Europe/Athens":

- Peak sessions at once: the most sessions the hub's registry held alive at the same moment. A
  session is alive from its first reported event until it ends.
- Cards done: the `done` lines the boards the hub knows wrote to their LOG.

The card reads `GET /api/radar/api/day` on the hub, which answers
`{"peakSessions": 3, "cardsDone": 2}`. A standalone radar holds neither fact and answers 503 there.

Every 10 s the hub samples each open session's CPU and memory: one `ps` call for the whole machine,
then the session's pid and every process under it, summed. A pid that no longer runs `claude` is
skipped, and with no session open nothing is read. `GET /api/radar/api/sessions` on the hub carries
the latest sample on each session's row, `"cpuPct": 12.5` (the share of one core over the last
10 s, missing until the second sample) and `"rssMB": 412.3`. A session's last sample stays on its
row until the UTC day turns, after it ends too. A standalone radar knows no pids and leaves both
fields out.

Radar checks itself once a minute while the Overview is open. It sums today's Claude Code
transcripts again, counting each message id once (one API call writes one line per content block),
and compares requests, input, output, cache-read and cache-write tokens with its own totals for
the same UTC day. The Overview's badge is green at 1% drift or less, yellow above, and grey when
no transcript has a call today; hover it for both sets of numbers. The same answer is
`GET /api/radar/api/selfcheck` on the hub (`/api/selfcheck` on radar's own port).

Budgets stop and lift from Settings — on a hub, from a paired phone too. A stopped budget lands
in `budget-status.json` in radar's state dir (`$RADAR_HOME`, else `~/.agents/radar`), which
huddle-router reads before serving a provider model: while its scope (or `total`) is
stopped, the router refuses that provider's models. A missing file, one older than 10 minutes, or
one that does not parse stops nothing — a broken radar never cuts you off, and `claude-*`
requests are never read against it.

An idle hub costs next to nothing: with no request and no page open, `huddle serve` uses under 0.5% of one
core over a home holding 500 days-old sessions, because Radar's 5 s transcript rescan re-reads only the
folders that changed. `TestHubIdleCPU` holds it to that budget.

A budget warns at 80% of its limit and again at 100%. Claude's 5-hour plan window has no budget, so
Radar raises a "Quota nearly used" alert when the newest status line snapshot shows it at 80% or more:
`Claude 5-hour window 80% used, resets 14:00 UTC`. It comes once per window, as a desktop notification
too, and the next window raises a new one.

### Claude Code's OpenTelemetry export

Radar also takes Claude Code's own token and cost metrics as a second source. Usage › Source ›
OpenTelemetry shows them per model, beside the transcripts and never added to them, so the two
counts can be checked against each other.

1. Find the hub's port: `run/huddle.port` in the huddle home (`$HUDDLE_HOME`, else
   `~/.config/huddle`).

2. Add this to Claude Code's `settings.json`, with that port:

   ```json
   {
     "env": {
       "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
       "OTEL_METRICS_EXPORTER": "otlp",
       "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
       "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:<port>/api/radar/otlp"
     },
     "otelHeadersHelper": "printf '{\"x-huddle-token\":\"%s\"}' \"$(cat ~/.config/huddle/run/owner.key)\""
   }
   ```

3. Start a new session. Claude Code exports once a minute; the rows show on the next export.

The intake is `POST /api/radar/otlp/v1/metrics`. It takes `application/x-protobuf` or
`application/json` (`http/json`), gzipped or not, up to 4 MiB, and only with huddle's credential:
the helper reads the owner's key on this machine, and the hub listener never takes that key. It keeps
`claude_code.token.usage` and `claude_code.cost.usage`, one row per point, source `otel`; delta
points add up, a cumulative series counts its newest point. `GET /api/radar/api/otel` answers the
totals and the newest 200 rows. The rows live in the hub's memory, the newest 100,000 kept, and a
restart starts them empty.

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

5. Behind Caddy on this machine (Caddy terminates TLS for the name and forwards to the hub's
   loopback port), skip steps 2 to 4 and pass the name with `/huddle:domain` (`huddle domain` in a
   terminal):

   ```sh
   huddle domain hub.example.dev    # role hub for the name, and the Caddyfile block to paste
   huddle domain                    # the name, or "localhost only"
   huddle domain --check            # asks https://hub.example.dev/health: does Caddy reach this hub?
   huddle domain off                # back to loopback only, the same as huddle role solo
   ```

   The block forwards to the pinned port with `header_up X-Forwarded-Proto https`. A public name
   gets its certificate from Caddy's own ACME (a DNS challenge when the name has no public A
   record); a private name (`hub.example.test`) gets `tls internal`, and `caddy trust` once makes
   this machine's browsers accept it. huddle never edits the Caddyfile, and only `--check` reaches
   the network: 3 s, the system resolver, no `HTTPS_PROXY`. The long form of the same role is
   `huddle role hub --host hub.example.dev`.

   The hub opens no listener of its own; printed links name the proxy's origin
   (`https://hub.example.dev/?code=…`), and the sign-in cookie carries `Secure` when the proxy
   forwards `X-Forwarded-Proto: https` from loopback. The header from any other peer is ignored.
   The role pins the hub's port (18860, or the one already saved) so the proxy always finds it:
   when another program holds it, `huddle up` exits 2 instead of moving to another port. Leaving
   the hub role (`huddle role solo` or `satellite`) unpins it.

6. Bring the phone in: `huddle trust --phone` prints a QR (the page it opens serves for 10
   minutes and installs nothing on the computer); then `huddle pair --name phone`, scan, and save
   the passkey. The dashboard's Settings → Phone carries the same "Add a phone" QR.

   What to expect on the phone: the first open moves about 260 KB (the app as brotli, its
   stylesheet and two fonts) and shows the Now page in about 2 s on a slow 4G link. Every later
   open moves only the small shell page and the live data: the app's files are named by their
   content and kept by the browser for a year, so a new build is the only thing that fetches
   them again. Slower than that points at the network between the phone and the hub, not the hub.

   Turn on push on the phone: open a channel's Settings and flip "Push to this device", then allow
   notifications. On iPhone and iPad, add Huddle to the Home Screen first (Share, Add to Home
   Screen) and flip the switch in the installed app: Safari pushes only there. The phone then
   gets one notification when a session needs you (tap it to land on that session's item). With
   Settings → Notifications on "Needs you and alerts", it also gets one when the Anthropic path
   starts failing (tap it for Radar's alerts). A finished turn pushes only a device that turned
   on "Push when a turn finishes" under the same heading; it is off by default. While a hub tab on
   one device is visible and focused, and for 60 s after its last report, every other device gets
   no push; the item still waits in Now. The payload names the
   session, its machine, the kind and the tool, never the command or the prompt. The hub makes
   its VAPID keys once, when the first browser subscribes, and keeps them in the OS keystore
   with a copy in `<home>/keys/vapid` (mode 0600). A subscription the push service reports gone
   is deleted.

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
   default, which is the API's own host (`https://api.anthropic.com`, `https://api.openai.com`)
   when the file names none, so the file is optional. Requests are forwarded byte for byte with
   their headers — the gateway adds nothing and keeps nothing but the route line's metadata.

2. Run the router and read the address:

   ```sh
   huddle router start      # the daemon, if it is not up already
   huddle gateway status    # the /gateway address, or the way to start the router
   ```

3. Give your harness the base URL `http://127.0.0.1:<router port>/gateway/v1` —
   `model_providers.<x>.base_url` in `~/.codex/config.toml` for Codex CLI (`huddle harness
   install codex` writes one named `huddle`; `codex -c model_provider=huddle` uses it), the provider's
   `baseURL` for opencode, `providers.<x>.base_url` for Crush. A client can name itself with an
   `X-Huddle-Harness` header, which lands on the route line next to `via: gateway`, so radar
   tells the harnesses apart.

A hub an older build left running gives way on the next `huddle up` or session start: the hub
starts again on the same port, and the router beside it moves over by its own handover, so it
never stops answering.

A router whose resident size passes its memory ceiling (1 GiB by default,
`HUDDLE_ROUTER_RSS_CEILING_MB` to move it) hands itself over to a fresh copy.

## Deploy a new build

1. Update Claude Code's plugin cache, which runs the plugins' hooks, commands and skills from the commit it
   installed: `claude plugin update huddle@muhmdraouf`, then the same for each provider plugin you use
   (`zai-plugin-cc@muhmdraouf`, `kimi-plugin-cc@muhmdraouf`, `deepseek-plugin-cc@muhmdraouf`,
   `minimax-plugin-cc@muhmdraouf`, `qwen-plugin-cc@muhmdraouf`). Running sessions keep working: their hooks and
   their MCP server run the launcher's `current` binary, not the plugin directory the update replaces
   ([ADR 1](adr/0001-hooks-run-the-stable-binary.md)).
2. Build or install the new binary (the launcher does it on the next session start or command after the update).
3. Run `huddle up` from the new build. It replaces an older hub and moves the router over by handover
   ([ADR 4](adr/0004-router-process.md)): the router port never closes, the old router process drains what it
   already accepted, and no Claude stream in flight is cut.
4. Check: `huddle status` names the hub's pid and the router's, then the deploy line
   `plugin cache 052c8e49c9ff · binary 38e9e40e321e (cache behind)`. `(same)` means the cache and the binary
   come from one commit; `(cache behind)` means step 1 is still to run. No plugin install prints no such line.
   Radar's Alerts view shows the same skew: each session start that finds the two commits apart keeps one
   `plugin_behind` alert naming both and the update line, and the first start that finds them equal clears it.

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
