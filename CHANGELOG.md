# Changelog

## 0.0.9

A lighter router: bodies stream through, the dashboard costs a fraction of its CPU, and the worker runs on fewer threads.

### Router

- A request body streams through: the router keeps the first 1 MiB for a retry, reads at most 256 KiB ahead of what the upstream has taken, and drops every chunk past the prefix once it is sent. A body holds at most replay prefix + window + one frame (1 MiB + 256 KiB + up to 64 KiB) instead of the whole upload.
- A request body over 1 MiB that meets a dead pooled upstream socket is no longer retried by the router; the client gets the cut and retries, and a retry never sends a half body.
- A streamed upload the router will not read again (an early 4xx, a failed send) is read to its end and dropped as it arrives, instead of being held whole until the client finishes.
- A request body that takes the fallback path is held once instead of twice, and the Qwen lane holds one copy (the adapted bytes) instead of the original beside it.
- The router worker runs on 4 runtime threads and the supervisor on 1, cutting idle threads from 29 and 18 to 9 and 4. An idle worker stays at 10 threads on a loaded machine.
- The router's memory ceiling and the dashboard's CPU and memory read the kernel's footprint directly, without forking `ps`; the ceiling now counts compressed and swapped pages.
- The answer stream tap reads only the `type` of most events and parses usage events in full, cutting its CPU about 8x.
- A second CONNECT from the same client resumes its TLS session instead of a full handshake, because every tunnel shares one TLS server config.
- The upstream pool drops expired idle sockets when another returns, keeps at most 16 idle sockets per host, and sweeps every host's idle sockets, not only the one that returned.
- The upstream client takes the request body's reader before the pump task starts and never marks a failure replayable except while the body can replay.
- The router decides a request's lane once instead of up to three times.
- A request's answer frames no longer take the live-state lock to record their last byte time.
- Log lines are written by one thread that keeps the file open, so a request no longer opens and closes the log file.
- The reload poll stats `providers.toml` and the dashboard hosts file and reads them only when one changed, on a blocking thread instead of the serve loop's async task.
- The history writer and the handover socket sleep until there is work instead of waking on one-second and 200-millisecond timers.
- The history database's WAL file is capped at 4 MB and shrinks back after each checkpoint.
- A history call runs on one read connection with a capped page cache and spawns no threads, so ten concurrent 90-day calls no longer add over 100 MB.
- A handover drain logs what it waits for at its start and what was still open when it ended or hit its limit.
- The dashboard asks the key store whether a key is set without reading the key, at most once per provider per minute.

### History fill

- The history fill reads session logs one line at a time and never recreates a removed data or state folder.
- The fill reads only the bytes appended to each session log since the last tick and stops asking for an id after an hour.

### Dashboard

- The dashboard's session scan reads only the bytes appended to each session log since the last scan, and skips a log that did not change.
- The dashboard builds one snapshot per change for every open tab, credits each session reading once, and shares the sessions and worktrees reading instead of copying it per tab; the joined snapshot copies only the busy sessions.
- Dashboard history ranges are answered from one cache shared by every tab (a new row or a new minute asks again), and ranges of 7 days or more refresh every 60 s instead of 5 s.
- Session and project totals filter in SQL, and the Sessions page asks for its listed sessions only.
- A dashboard tab whose live feed is closed for the 8-feed limit polls every 5 s instead of reconnecting and evicting another tab in a loop.
- The default one-second refresh shows sessions, worktrees, plugins, harnesses and router CPU and RAM, and a finished request gets its agent and tokens even when no page is open.
- The dashboard's worktree feed lists each repository's worktrees with one `git worktree list`, however many sessions work in it.
- The live feed sends the sessions and worktrees only when they change, and each snapshot event is written once.
- The page no longer rebuilds every region every 2 s; a tick moves only the clock and the relative times, and a hidden tab holds no timer.
- The top bar has one time-picker button that opens a panel with the quick ranges and an absolute From and To, replacing the range menu and its Custom popover.
- The time panel lists the last four applied custom ranges under the absolute fields and holds the Local/UTC switch in its footer; the separate header switch is gone.
- Beside the time-picker button, « and » shift the range back and forward by its own length and a zoom-out button doubles it around its centre; each turns the range into a custom range clamped to the kept history and to now, and » is disabled while the range ends at now.

### Plugin

- The plugin ships one MCP server, `huddle`, over HTTP: a Claude session starts no `huddle mcp` process and lists the 11 huddle tools once. The tools need the router up. The `huddle mcp` word stays in the binary.
- `/huddle:setup` asks once whether the router is already running instead of polling for up to 3 s, so a fresh install is about 3 seconds faster.

### Fixes

- When nothing newer is published, `/huddle:update` prints only `huddle <installed> is the
  latest published version. Nothing to update.` and exits 0, whether or not the plugin's own
  version is above it. It no longer says the plugin's version is not published yet.
- `/mcp`: the 401 text says to reconnect huddle, not huddle-http.

### Tests

- A new e2e test fails when eight parallel 20 MiB uploads through CONNECT grow the router worker's memory footprint by 32 MiB or more.
- The unit suite and the e2e suite run as separate tasks; no test runs the real ZCode app.

## 0.0.8

### Fixes

- Setup that could not reach the provider no longer says ready alone: the report says `ready, key not checked` and names `/huddle:<p>:setup` as the way to check the key once online. The lane keeps serving and exit stays 0.
- `huddle uninstall` leaves nothing of huddle on the machine: after the undo it deletes the CA and its key, the binaries, the config, the state (the request history too), the cache and the runtime folder, and names each one. A folder not named `huddle`, or a link, is left in place and named; a failed settings restore keeps the ledger, a failed router stop keeps the state, runtime and data folders, and a provider that did not remove keeps `providers.toml`.
- A process the router starts no longer inherits its listening socket after an update handover, so a leftover `git` cannot hold port 4835.
- MCP over HTTP needs a token: the router refuses `/mcp` without it, and the plugin's `headersHelper` runs `huddle mcp headers` so Claude Code sends it with no step from you. `huddle router status` says `mcp: token ok` or what is wrong.
- The live dashboard feed refuses an `Origin` header that is not readable text, like a wrong origin, instead of treating it as absent.
- Setup waiting for the key page reports ready and exits 0 when the key is stored but the ready note cannot be written, instead of `needs a key`.

### Router

- After an update hands the socket to the new binary, the old router closes its idle connections and open dashboard feeds at once, lets a request in flight finish for up to 10 minutes (`HUDDLE_ROUTER_DRAIN_LIMIT_MS` moves the limit), closes a tunnel that carries no byte for 3 seconds, then exits. It lets go of the listening socket at the handover, and logs one line when the limit cut a request off.
- `/huddle:update` installs the latest published release and never builds. It says `Nothing to update` when nothing newer is published, says when the plugin is ahead of the release, says when GitHub is unreachable, points to `/plugin` when the binary is newer than the plugin, and exits 0 in each case. A failed checksum installs nothing.
- The bundled SQLite is built without full-text search, R-tree, JSON, load-extension and other features huddle does not use (`.cargo/config.toml`): the macOS binary drops from 5.97 MB to 5.59 MB.
### Dashboard

- The page's poll asks for `/huddle-router/state?recent=0` on every page except Requests, Errors, Agents and Router, and while a sheet is open: the answer drops the 200 recent rows. Their counts (cancelled and failed in 15 minutes, each session's requests and tokens, each provider's failure causes) come from the server in both forms.
- The page draws its own menus and date picker instead of the browser's: the time range and the Requests status filter are menus with keyboard control (arrows, Home, End, type a letter, Enter, Escape), and Custom opens a month grid with hour and minute menus.
- MCP over HTTP no longer answers for the router's own folder: `huddle_run`, `huddle_status`, `huddle_result` and `huddle_cancel` take a `dir` and refuse with a clear error when it is missing. Over stdio they still default to the folder the server runs in.
- A request that ended in an error answer is recorded with zero tokens instead of unknown, so it no longer waits for a session log that will never credit it; a client that left stays unknown.
- `huddle provider <unknown>` names the typo before the table: the first line is `huddle: no provider named "nosuch"`, instead of the provider table alone.
- Time to first token p95 for a history range is the p95 of the range's own requests, not the largest of the buckets' p95s: over 7 days the Models page and the Overview card no longer read up to 3 times too high. `totals&by=model` and `by=lane` carry `ttft_p50` and `ttft_p95` per key and for all rows.
- Usage a session log reports after its request left the 15 minute window still reaches the live lane total once, so live and history agree on a lane's tokens.
- `/huddle:<p>:setup` with no key opens the key page and finishes the setup when you save the key: no second run. The page says `Key saved, setup done`, closes itself after 10 seconds and says `You can close this tab.` when the browser refuses. The command waits 100 seconds; after that the next prompt prints the ready report once.
- `/mcp` answers a body over 4 MiB (413), truncated JSON, a batch, a missing session id (400) and an unknown session (404) with a JSON-RPC error, not plain text. `resources/list` answers method-not-found, since the server has tools only. `huddle_router_status` lists the same providers as `/huddle-router/health`, without the Anthropic passthrough.
- Column labels say what they count: the Models column reads "Cost per 1M total tokens" and its values carry "Cache reads and writes included", and the Errors column reads "Sessions hit (newest 500)".
- The dev tooling runs on bun: `package.json` and `bun.lock` pin esbuild as a dev dependency, `mise run setup` installs it, the build uses only that version, the page tests fail when bun is missing instead of passing without running, and `mise run dashboard:check` fails on a syntax error or a duplicate top-level name in the page script.
- The Sessions page asks for session totals without each session's per-model split (`totals&by=session&models=0`): over 7 days the answer drops from 301 KB to 121 KB. The session sheet asks for its own session (`&session=<id>`) when it opens. `/huddle-router/history` without `models=0` answers as before.
- Every time on the dashboard is in the browser's zone, with a `Local (UTC+2)` / `UTC` switch in the top bar that is remembered in the browser. History day buckets and the cost-per-day chart follow the zone: `buckets` and `totals&by=day` take `tz_offset_min` (minutes east of UTC, -840 to 840, anything else is a 400); without it the days are UTC days as before.
- A refresh-now button and a refresh picker (`Off` to `1h`, `1s` by default) sit beside the range picker as one control. The live snapshot follows the chosen interval; history panels follow it too but never fetch faster than every 5 seconds, and one request per endpoint is in flight at a time. A hidden tab pauses and fetches once when it shows again. The choice is remembered in the browser.
- The refresh picker gains `Realtime`: one event stream (`/huddle-router/live`, slim form unless the page lists requests) pushes the live snapshot at most four times a second with a keep-alive every 15 seconds, and refresh-now is greyed (`Realtime is on`) while it is chosen. A dropped stream shows `Realtime (reconnecting)`, retries with a delay that grows to 10 seconds, and falls back to the 1 s poll after three failed tries. The stream refuses a foreign Origin, and past 8 open streams the oldest closes.

### Build

- Rust 1.100.0 in `mise.toml`, `rust-version`, the Docker toolchain image and CI.
- Every Rust dependency at its latest release, with rusqlite 0.42.0 and a fully updated `Cargo.lock`.
- Every dev tool and CI action at its latest release, pinned exactly: Python 3.15.2, pre-commit 4.7.0, markdownlint-cli 0.50.0, setup-python 7.1.0, zig 0.17.1 and the Claude Code CLI 2.1.325 that validates the plugin. The toolchain image checks the gitleaks, actionlint and shellcheck archives against their sha256, and `mise run pins:check` (`scripts/pins-check.js`, also the `pins` CI job) fails when a tool pin is a range or a workflow, the Dockerfile or a hook names another version than `mise.toml` and `package.json`.

## 0.0.7

Provider setup holds across sessions, and the key check gets a real answer.

### Fixes

- A new Claude Code session no longer removes the set-up providers, their keys and their agents.
- The key check sends `Host`, so Z.ai answers it: a working key reads as checked and a wrong one is refused.
- The key page names what the check got (`answered HTTP 400`, or no answer in 20 seconds) instead of always saying it timed out.
- `huddle router status` says "restarting" while the router brings a crashed worker back, instead of "not running".
- A test run no longer leaves a temp home behind: dropping a home stops every process that still writes under it.

### Dashboard

- The Harnesses page says why a harness fails to start (first line of its error) and when the table was last checked. The Router page shows requests per second, router overhead p50 and p95, upstream failures by phase (DNS, connect, TLS, answer), the parent and worker with restarts, and checks that follow the live lanes. The dashboard stops polling while its tab is hidden.
- A project is a git repository: a session in any worktree belongs to its repository, new history rows carry the repository's name, and the project totals roll old folder names into their repository.
- Token numbers split the same way live and in history (fresh input, cache read, cache write, output), cache hit matches between ranges, rates read per second, the Agents page counts each agent's requests and tokens, and each sidebar badge counts what its page lists.
- The state carries each set-up provider's quota or balance: Claude's 5 hour and 7 day windows, Z.ai's plan windows with their resets, DeepSeek's and Kimi's balance, MiniMax's plan (unverified until a live check). The router asks only while a dashboard is open, at most every 5 minutes per provider, and an answer it cannot read shows as "quota unknown".
- One time range in the top bar drives every page: 15 min, 1 hour, 24 hours, 7, 30 and 90 days, or a custom from-to that cannot start before the kept history. The history endpoint takes `range=15m|1h|24h|7d|30d|90d` or `from=<ms>&to=<ms>`, sizes buckets to 30 to 120 per window, says when it cut a start to `keep_days` (`clamped_from_ms`) and answers `view=meta` with `keep_days`. The choice is remembered and named in each page subtitle.
- The Providers page shows each provider's live status (Working, Out of quota, Failing above 20 % errors, Needs a fix, Not set up; a provider with traffic is never grey), its plan windows or balance with the reset time, requests, errors by cause, first-token p50 and p95, tokens, API price, models and agents, and the next step. Each provider row in the state carries its billing page for providers with no quota API.
- Models shows each model's provider, error rate, first-token p50 and p95, token split, cache hit, API price ("no price" for a model without a list price) and price per 1M tokens. Usage lists every provider's plan or balance, cost per day stacked by provider, what the cache saved in dollars, tokens by model, and a projects by models matrix that follows the picked range and groups by repository.
- The Overview opens with eight stat cards and a Needs you list of at most five items with Show all; a new Needs you page groups every item (questions, providers, router, jobs to review, setup checks, context), and traffic is stacked by provider.
- Every history view answers in under 300 ms on a 90-day database of a million rows. A request no longer scans the session logs: a background tick credits missing usage every minute, and the views read the database read-only, one slice of the time range per thread.
- Requests shows the provider, a reason chip on failures (quota used up, overloaded, rate limited), the agent, the upstream id, router time and the API price, lists only providers with traffic, and keeps the live list still while the page is scrolled, with a "n new" button to jump back. Errors groups by provider and cause with count, first and last seen, sessions hit and the fix (a used-up quota reads "quota used up, resets HH:MM"), charts the error rate per provider, and folds the cause legend away. Both follow the time range.

- Sessions show the six states in plain words (Needs you, Working, Background, Your turn, Done, Idle) with a filter chip each, a Group by repo toggle, errors and cost in the chosen range, and a cold cache says how many tokens the next message re-reads. No column clips at 1440 px and phones stack each row. The session drawer retries a failed history load and lists agents, background tasks and each request's reason. Agents lists main agents, subagents and open background tasks with requests and tokens, and a row quiet for 30 minutes reads Idle.
- The Sessions list follows the time range: a session needs a request inside it or a live state (needs you, working, background) to stay listed. The heading count, the chips and the sidebar badge count what the list shows, and the badge names the chosen range.
- Projects are one row per git repository (active sessions, worktrees with a stale count, requests, tokens, cost and top model in the range, last activity) and its drawer lists branches, worktrees and sessions. Worktrees group by repository with Stale and Uncommitted filters and the command that removes an agent worktree. Jobs keep finished jobs (finished, failed, accepted, cancelled) for the chosen range, with outcome, review state and duration.
- Cost cells read plain prices under an "API price" header, with "no price" for a model without a list price; page subtitles name the chosen time range; the Providers badge counts what the Providers page shows; the Errors legend shows the range average; Providers and Errors read failure reasons from history in every range (`by=reason`); a quota tool call asks the router to refresh once, within the usual five minute limit.
- The plugin's `huddle-http` server url follows `HUDDLE_ROUTER_PORT` (`http://127.0.0.1:${HUDDLE_ROUTER_PORT:-4835}/mcp`), so a router on a moved port stays reachable without editing the plugin.
- The release build compiles everything for size (`opt-level = "s"`): the macOS binary drops from 9.02 MB to 5.97 MB.
- The 60 s usage fill reads only its time window through the time index, instead of scanning the whole upstream-id index on every run; the query plan is pinned, so the fill stays bounded by the window as history grows.

### MCP

- `huddle mcp` serves the huddle tools to Claude Code as an MCP server over stdio. It works with the router down and asks the router when it is up. The first tool, `huddle_version`, reports the version, whether the router answers and the binary's path.
- The router serves the same tools over MCP streamable HTTP at `http://127.0.0.1:4835/mcp`. It answers only a Host of `127.0.0.1:<port>` or `localhost:<port>` and an Origin of the same two, never a dashboard host, and each client session has its own id.
- The release binary limit is 12 MB, up from 10 MB, for the MCP HTTP transport.
- Four delegation tools run the same code as the CLI words: `huddle_run` (waits for the report, or returns the job id with `background`), `huddle_status` (this repository's jobs, or every repository's live jobs), `huddle_result` (the report, the diff on request, accept with or without a commit, return with feedback) and `huddle_cancel`. A bad harness or job id is a tool error, never a crash.
- `huddle_usage` answers tokens (fresh input, cache read, cache write, output), requests, errors and cost at API price from the history, grouped by provider, model, session or project, for 15m, 1h, 24h, 7d, 30d, 90d or a from and to in milliseconds. `huddle_quota` answers each set-up provider's plan or balance and its resets from the router, or "router down, quota unknown". `huddle_providers` lists the five providers' lane, whether a key is stored (never the key), models, agent files and next setup step.
- The plugin declares two MCP servers in `.mcp.json`, `huddle` (stdio) and `huddle-http` (the router's `/mcp`), both on by default so they can be compared; disable one in `/mcp`. A test calls every tool on both and requires the same answers.

### Router

- `huddle router serve` is a small parent that holds port 4835 and runs the router as a worker process. A crashed worker is back on the same socket within a second (then 1 s, 2 s, 5 s and 30 s apart after repeated fast crashes), a request sent while it restarts waits instead of being refused, and each restart is one log line. `router.toml` names the parent pid and the worker pid.
- `huddle router stop` ends the parent and so the worker. `huddle router status` (and `--json`) names the parent pid, the worker pid, the restart count and how the last worker ended, and the dashboard state carries the same under `router.supervision`. An update hands the socket to the new binary and the old parent and worker exit. The session hook leaves a router whose lock is held alone, as the prompt hook does.
- `huddle_router_status` says whether the router is up, its pid, version, uptime, the lanes it serves, whether the Anthropic path works (with code, phase, peer and since) and its last five events; with the router down it gives the last exit line and the start command. `huddle_errors` groups failed requests from the history by provider, model, status and plain reason (`quota used up`, `overloaded`) with count and first and last time, router down or not. `huddle_router_log` returns the lines `huddle router log` prints, from one shared reader.

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
- A key file moves to `~/.config/huddle/keys/<p>` on first use.
- `huddle provider <p>` guides a terminal: a menu, a masked key prompt, backspace, Ctrl-C that stores nothing and restores the terminal, and three refused keys end with exit 6. Off a terminal it prints the next steps and never waits. A piped key is stored as before.

### Router start

- A start waits up to 10 s for the spawned router to answer health, polling every 50 ms, and stops at once with `the daemon exited before it answered` when the process is gone.
- A start and the prompt hook spawn nothing while another process holds the router lock: the hook logs `hook: router busy (<reason>), not restarted`, and a start waits for the holder's health answer. A spawned daemon that exits while the lock is held keeps the wait going.
- A start waits up to 200 ms for the router lock, so a probe's momentary hold never refuses a real start; a live holder still refuses it.

### Terminal

- `huddle` alone shows where you stand: the router, each provider's key and lane, and the next command. `huddle help` lists every word.
- `huddle` is linked into `~/.local/bin/huddle` on setup, never over a file that is not its own link, and removed by `huddle uninstall`. Setup says when `~/.local/bin` is not on `PATH`, and carries on when the folder cannot be written.
- An unknown word or action is answered with the valid ones and is never echoed back, in case it was a key.

### Router log

- Whatever the daemon prints on stdout or stderr is filed as an `output` event, so `huddle router log` and the dashboard show it. Lines are cut at 1 KiB, limited to 20 per second (the excess is counted) and stripped of `Bearer` tokens, `x-api-key` values and `sk-` keys.
- `router-events.log` keeps only the event lines, rolls at 1 MB and keeps five older files; `router.log` also keeps five. `huddle router log` prints the last 40 events, `--since <n>m|<n>h`, `--requests` or `--all`, without a router.
- `router.log` names where an upstream failure happened (`phase=dns|connect|tls|send|headers|body`, the peer and whether the socket was pooled), how long a failing path lasted and how many requests failed, and why a router started, handed over or exited. While the path fails, a probe line every 15 s shows DNS and connect separately.

### Dashboard

- One Providers page joins setup and traffic: a row per provider with its status, requests, first-token time, errors and next step, and a drawer with the key, lane, `/model` entries and commands. Providers that are not set up read as neutral, not as failures. The sidebar badges read as ratios with the good part green.
- The Overview opens with "Needs you", before the numbers. Providers you have not set up are one quiet line, not four problems, and a provider that needs a fix is a real item. Filter counts stay right, the "Memory only" footer shows only where the page reads live memory, and light-theme grey and blue text meet 4.5:1.
- Keyboard and phone fixes on the dashboard. Command chips on a phone break only after a colon.
- The dashboard source is one file per page under `crates/huddle-router/src/dashboard/`, joined at compile time. The page you see is the same.
- On a 360 px phone the Harnesses and Overview tables stack each row as a block of `label: value` lines, with no sideways scroll. Desktop is unchanged.
- When the Anthropic path fails, the Overview leads with `Anthropic is not answering` and the router log command, and keeps `Anthropic recovered` for 30 minutes. The Router page lists the last 20 router events.
- Session states say who is waiting on whom: needs you (an unanswered question or plan approval), working, background (the turn ended with tasks or agents still open), your turn, done (a driven session or a finished subagent) and idle. Each session carries when its state began and how many background tasks are open.

### Tests

- A test home stops the key page helpers it started when it is dropped.

### Build

- The Rust toolchain is pinned in `mise.toml`, the Docker toolchain image and CI. mise wraps cargo with mr-boxington (`mbx`), so every worktree shares one build cache.
- One banner per provider in `assets/banners/<p>.svg`, shown in the README's Providers section.

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
