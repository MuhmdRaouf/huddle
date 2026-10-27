# Decisions

The design decisions every change in this repository follows. Each one states the design as it is; a change that
breaks one changes this file in the same commit. The commands and rules for working in the repository are in
[AGENTS.md](../AGENTS.md), and the usage guide is [docs/USAGE.md](USAGE.md).

## D1: One binary, one repository

The provider engine lives in the huddle module as `internal/core/...`, beside huddle, radar and kanban, and every
part ships as the one `huddle` binary. The five provider plugins live in this repository too (D19). huddle is the
only repository maintained.

## D2: One router and one hub per user

huddle-router and the hub are two processes, each a singleton ([ADR-4](adr/0004-router-process.md)). `huddle
router serve` runs whenever at least one provider is set up; `huddle serve` runs the hub with its Radar and Kanban
tabs when the huddle home is set up. `huddle up` starts the router, then the hub. A hub restart never touches the
router. A second start of either finds the first and exits 0.

## D3: Port

huddle-router listens on `127.0.0.1:18786`; `HUDDLE_ROUTER_PORT` moves it. Its health answer is
`GET /huddle-router/health`.

## D4: Providers are data

All five providers are compiled in (`internal/core/providers`). A provider is enabled when its setup recorded it in
`~/.agents/huddle-router/providers.json` and its plugin is installed and enabled in Claude Code. The router matches
a request's `model` against every enabled provider: catalog ids first, then the longest model prefix. A model no
lane claims goes to Anthropic; there is no peer registry and no hop between routers.

## D5: State

Router state lives in `~/.agents/huddle-router/`: the pid record, the lock, the ledger and the logs. Each
provider's job state lives in `~/.agents/<p>/`. A provider's key comes from its environment variable, else the OS
keystore, else its key file (mode 0600), so a key saved once is read without re-entry.

## D6: Claude Code settings

Setup runs Claude Code in proxy mode: `HTTPS_PROXY=http://127.0.0.1:18786`, `NODE_EXTRA_CA_CERTS` at the shared
`~/.agents/ca` root, and loopback in `NO_PROXY`. A proxy or CA of the user's own is never touched; routing then
falls back to `ANTHROPIC_BASE_URL` pointing at the router. The `/model` entries are the union of the enabled
providers' catalogs. The ledger records each provider's changes and one shared proxy entry, and the proxy keys are
cleared only when the last provider goes.

## D7: Hooks

Provider plugins ship no hooks. huddle's merged event hook (`internal/hookplan`) runs a `providers` part at
SessionStart, which ensures the router, re-applies each enabled provider's settings after a plugin
update, detects removed plugins and prints the jobs-awaiting-review line, and a `usage` part at SubagentStop, which
says what a finished agent ran on.

## D8: Watchdog

`huddle mcp`, one per session, checks the daemon's health every 2 s while it runs and restarts it after 5 s of
silence; the daemon's start lock keeps the restart to one daemon. No other process watches the router.

## D9: Never cut Claude off

The router runs in its own process (D2), takes no lock the hub takes, and every goroutine and handler recovers its
panics. A graceful handover serves updates: the listening socket passes to the new process and the old one drains
its streams for up to 10 minutes, so the port never closes. A router whose resident size passes 1 GiB
(`HUDDLE_ROUTER_RSS_CEILING_MB` moves it, checked every 30 s) hands itself over to a fresh copy.

## D10: Gateway merged

huddle-router serves the gateway under `/gateway`: the OpenAI Chat Completions and Responses paths, the Anthropic
Messages path and the routes in `gateway.json`. `huddle gateway status` reports the gateway's address.

## D11: One PKI package, two roots

`internal/pki` holds both local CAs, and their roots never meet. The router root is name-constrained to
`api.anthropic.com` and trusted only through `NODE_EXTRA_CA_CERTS`. The hub root is separate: it may be in the OS
trust store, so it must never be able to sign `api.anthropic.com`.

## D12: One copy of each adapter

Each adapter exists once: the keystore (`internal/adapters/keystore`), processes (`internal/adapters/process`), the
file helpers (`internal/adapters/fsutil`), the spool (`internal/adapters/spool`) and the proxy environment
(`internal/netproxy`).

## D13: One launcher

Every plugin in the repository, huddle and the five providers, uses the one launcher `scripts/launcher.mjs`
generates. It finds or installs the huddle binary under `${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}/` and
points `current` at it. A provider plugin's `dist/run` execs `huddle provider <p> <entry>`. Plugins and binary come
from the same commit, so there is no version lock between them.

## D14: No 0.0.1 migration

huddle-router carries no 0.0.1 migration: it starts no move of a 0.0.1 setup, retires no per-provider router and
adopts no 0.0.1 port. A 0.0.1 install joins the current layout by running the setup words again, and setup moves
Claude Code's `HTTPS_PROXY` off a provider port it recognises. Job state and keys are never touched.

## D15: No leaks

One huddle home has one `huddle serve`, one user has one `huddle router serve`, and the router's memory has a
ceiling (D9). A start that finds either already serving answers with it instead of starting another.

## D16: No standalone servers

Hooks never start `huddle radar start` or `huddle kanban serve`: the hub serves Radar and every board in process, and
those words reach the hub. `huddle kanban serve` remains a foreground server for tests and debugging.

## D17: Provider data once

Provider data lives once, in `internal/core/providers`, and the Go tests read it from there. The plugin suites are
Go tests in `internal/plugins`, where `TestSiblingsMatchZai` keeps the five provider plugins copies of one another.
The repository's one Bun workspace is the web UI (`web/`).

## D18: Release

There is no release and no tag while there are no consumers. Plugins carry no version, so every commit is an
update, and the launchers build huddle from the marketplace checkout's own commit. huddle is the one repository
that releases, when consumers exist, and `go run ./scripts/build -manifest -check` keeps the release machinery
ready.

## D19: Repository layout

`.claude-plugin/marketplace.json` at the repository root is the `muhmdraouf` marketplace, listing `huddle`,
`zai-plugin-cc`, `kimi-plugin-cc`, `deepseek-plugin-cc`, `minimax-plugin-cc` and `qwen-plugin-cc`, so `/zai:setup`
and its siblings keep their names. huddle's plugin is `plugin/`, the path the git-subdir source of existing installs
names, and each provider plugin is `plugins/<p>-plugin-cc/`. Installing a provider plugin installs `huddle` as its
dependency. A machine runs the router alone until `/huddle:setup`, and the hub beside it after.

## D20: Tests describe behaviour

Every change is test-first, and a user-visible behaviour is covered at three levels:

1. TDD: the failing test comes first, then the code.
2. BDD: each user-visible behaviour is a scenario in a `*_scenario_test.go` file, written with
   `internal/testkit/bdd` (`bdd.Feature`, `Scenario`, `Given`, `When`, `Then`) on plain `testing`. Scenario names
   are sentences a user would recognise.
3. E2E: `cmd/huddle/*_e2e_test.go` builds the real binary and drives it as Claude Code would, under temp homes and
   against fake upstreams, never the real `~/.claude`, keystore or network.

A pure refactor is proven by the existing suites passing unchanged.

## D21: Retire claude-code-plugins

claude-code-plugins is retired. huddle is the one repository for huddle and the five provider plugins, and the
`muhmdraouf` marketplace (D19) is where every plugin installs from.

## D22: Never a chain

The 2026-10-21 incident, a session left on a 0.0.1 port whose requests looped through huddle-router and failed with
502 for hours, is in [docs/INCIDENTS.md](INCIDENTS.md). Three rules keep it from recurring:

1. A router never sends upstream traffic through another router. It recognises one by its own port, the known port
   list, and by asking any loopback proxy for a router health answer, never by a port list alone; daemon spawns
   drop proxy variables that point at a router (`internal/routerd/nochain.go`).
2. A failure names its cause: TLS and certificate failures are `ECERT`, not `EUPSTREAM`
   (`internal/core/router/upstream.go`).
3. A failing Anthropic path is visible within a minute: three `claude-*` upstream failures in 60 s raise one router
   health event and a Radar alert in the hub, with the code and the fix.

Rules 1 and 3 each have a BDD feature named "the 2026-10-21 incident cannot recur: ..." and an e2e test on the
built binary (`cmd/huddle/nochain_e2e_test.go`, `failalert_e2e_test.go`).

## D23: Source builds while there is one user

huddle installs from source while it has one user: the launchers build the binary of the marketplace checkout's
own commit with Go (D18), so a machine installing huddle needs Go. The rule flips to signed release binaries the
day huddle has a second user; darwin code signing and notarisation of the release binaries is the open question
then.

## D24: Each store owns its events

There is no single event log. Each store — the spool, `hub.db events`, the channel files, ledger.db,
router.db and its segments, history.db, kanban.db and its LOG — has one writer process and owns its
retention ([ADR-3](adr/0003-one-event-log.md)). Readers join stores by session id, request id or a `seq`
cursor, and never write another part's store. A new store names its writer, its join key, its retention
and the process that runs it, and adds itself to the ADR's list.
