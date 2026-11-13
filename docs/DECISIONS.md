# Decisions

The design decisions every change in this repository follows. Each one states the design as it is; a change that
breaks one changes this file in the same commit. The commands and rules for working in the repository are in
[AGENTS.md](../AGENTS.md), and the usage guide is [docs/USAGE.md](USAGE.md).

## D1: One binary, one workspace

The tree is the Rust workspace under `crates/`, and every part ships as the one `huddle` binary. The huddle
plugin (`plugin/`) and the five provider plugins (`plugins/`) live in this repository. huddle is the only
repository maintained.

## D2: One router per user

huddle-router is its own process, `huddle router serve`, and nothing else serves model traffic
([ADR-4](adr/0004-router-process.md)). It listens on `127.0.0.1:4835`; `HUDDLE_ROUTER_PORT` moves it, and the
port rule accepts plain digits only. Its health answer is `GET /huddle-router/health`. A second start finds the
first through the lock, names the holder and exits 0; a stale lock never blocks a start.

## D3: Providers are data

The five providers live once, compiled in (`crates/huddle-catalog`). A provider is enabled when its setup
recorded it in `~/.config/huddle/providers.toml`, which the daemon re-reads on change: a lane appears
within one poll. A request's `model` is matched against every enabled provider — catalog ids first, then the
longest model prefix, case-sensitively. A model no lane claims goes to Anthropic.

## D4: State

Files live in the XDG folders, split by kind. Config (`~/.config/huddle/`) holds `config.toml`, `providers.toml` and
`harnesses.toml`. Data (`~/.local/share/huddle/`) holds `bin/` and the CA in `ca/`. State (`~/.local/state/huddle/`)
holds `huddle.db`, the log, `run/` (the lock, `router.toml`, the handover socket), the settings ledger and the jobs.
`HUDDLE_CONFIG_HOME`, `HUDDLE_DATA_HOME` and `HUDDLE_STATE_HOME` point at huddle's own folder for a kind; else
`XDG_<KIND>_HOME/huddle`; else the default. Huddle's files are TOML, and every state file is written through an
atomic rename into a private directory. Nothing is written under `~/.agents`.

## D5: A provider's key

A key is read from the provider's environment variable first, then the OS keystore, then its mode-600 key file at `~/.config/huddle/keys/<p>`.
The lookup is cached, shared between concurrent requests and cleared on refusal. A key of the wrong shape is
refused without its value ever appearing in an error, a log line or a debug print.

## D6: Claude Code settings

Setup patches `~/.claude/settings.json` in place: the proxy pointing at the router, the CA root in
`NODE_EXTRA_CA_CERTS`, loopback in `NO_PROXY`, and the provider's models in the model picker. The first patch
keeps a private backup and a ledger of what huddle wrote. A proxy or CA of the user's own is never touched, a
foreign proxy switches that provider to its base URL, and undo restores the original bytes while keeping every
edit the user made in between.

## D7: Two hook events

The plugin wires two events and no more: SessionStart re-applies each enabled provider's wiring, names each
provider whose key is missing and brings a stopped router back; a
UserPromptSubmit probe checks the router within its one-second budget and brings a dead one back without
waiting. A hook exits 0 on any failure, stays silent when it has nothing to say, and every hook word a binary
has dropped stays a silent exit 0.

## D8: Hooks run the stable binary

Every hook line runs `${HUDDLE_DATA_HOME:-${XDG_DATA_HOME:-$HOME/.local/share}/huddle}/bin/huddle` when it is there, and the bootstrap
only until the first binary exists, so a session's hooks outlive a plugin update
([ADR-1](adr/0001-hooks-run-the-stable-binary.md)).

## D9: Never a chain

The daemon never sends upstream traffic through another router. It recognises one by the canonical address, its
own port, and by asking a loopback proxy for a router health answer — never by a port list alone — drops it and
goes direct. A proxy that is no router is kept and honoured, CONNECT tunnel and credentials included.

## D10: Never cut claude off

The router runs in its own process (D2) and every expected failure is a value. A binary update takes the
listening socket over through the handover socket without the port closing, and the old daemon drains the
tunnels it carries within its budget. A router whose resident size passes the ceiling (1 GiB by default,
`HUDDLE_ROUTER_RSS_CEILING_MB`, checked every 30 s) hands itself over the same way.

## D11: A streamed request is sent once

An upstream reset before the body reached the transport retries once, on a fresh connection, replaying the body
from byte zero. A timeout or a refused connection is never retried, and an answer already streaming is never
sent again in full.

## D12: Request history

Every finished request is one row in `huddle.db` under the state folder: time, session, agent, model, lane, status,
tokens, cache, time to first token, duration, stop reason and upstream id. No prompt, key or header value is stored.
A writer thread batches the inserts off the request path, and rows older than `[history] keep_days` in `config.toml`
(90 by default) are pruned once a day. Rows without usage are filled from Claude Code's session logs when a page is
open and when a history query runs. `huddle provider <p> usage` and the dashboard's history views read the database.
Claude models are priced at API list rates and labelled API-equivalent.

The router asks providers for quota or balance only while a dashboard is open: each set-up provider at most every 5
minutes, with its own key sent only to its own API host, and the last answer is cached with its time.

## D13: Delegation through jobs

`huddle run <harness> <task>` starts a job: the caller's folder (or the one `--dir` names), a driver running the named
harness, and the checks the caller gated it with. The report comes back verbatim — a verdict is what huddle
measured, and the harness decides what to do with it. `accept` lands the commit, `result` shows the report,
`cancel` stops the job and leaves the folder as it is, and a killed driver's process group is reaped by the next word.

## D14: Tests are hermetic

Every test runs on the kit's temp home (`crates/huddle-testkit`), which gives `HOME`,
`CLAUDE_CONFIG_DIR` and every `XDG_*` variable a private directory, leases its ports and lets no owner
variable reach a child. Upstreams and harnesses are in-process fakes. A run that leaves a process or a temp
entry behind fails, and no test reaches the live router's port or writes the process environment.
