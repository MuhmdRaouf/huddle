# AGENTS.md

Guidance for coding agents (Claude Code, Codex, Gemini CLI, opencode and others) working in this repository.
`CLAUDE.md` imports this file.

Huddle: one local hub where AI coding sessions work as a team, from any harness. Three parts ship as one binary and
one Claude Code plugin:

- **huddle**: channels, tasks, messages and knowledge shared between sessions; the hub (session list, needs-you
  approvals from a phone, session links); MCP pub/sub for any agent.
- **radar**: a zero-token dashboard of requests, tokens, cost and health, fed by hooks, transcripts and the spool.
- **kanban**: a board where sessions claim and move cards; one SQLite file per repository.

Claude Code is the first-class client and gets every feature. Other harnesses get the MCP surface plus whatever their
hooks and transcripts allow.

## Commands

```sh
mise run setup                                 # pinned tools and the pre-commit hook, once per clone
mise run check                                 # every gate CI runs: lint, static checks, all tests
go build ./... && go vet ./...                 # the Go module: cmd/huddle, internal/{huddle,radar,kanban,…}
go run ./scripts/tmpguard -count=1 ./...       # the whole suite under the temp-home and leftover-process guard
go test ./internal/kanban/... -run TestName    # one test
go test ./internal/plugins/                    # the plugins' suites and the sibling guard (TestSiblingsMatchZai)
bun install && bun run check                   # the web workspaces: web/{ui,hub}
bun run build                                  # rebuild every page into internal/*/web/dist (committed)
go run ./scripts/build -out dist               # the release binaries, darwin and linux on amd64 and arm64
go run ./scripts/build -manifest -check        # release-time check: the digests match release/v<VERSION>.json
claude --plugin-dir ./plugin                   # load the plugin from the working tree
node scripts/check-attribution.mjs             # no AI attribution in the history
```

## Layout

- `cmd/huddle`: the one binary. `huddle radar …` and `huddle kanban …` run radar's and kanban's commands;
  `huddle router …` runs the huddle-router daemon's words.
- `internal/huddle`, `internal/radar`, `internal/kanban`: each part's server, CLI, hooks and store.
- `internal/routerd`: the one daemon: the router run loop, the start probe (`routerd.Start`), the session
  watchdog in `huddle mcp` (health every 2 s, restart after 5 s of silence) and the socket handover.
- `internal/core`: the provider engine — router lanes, job engine and provider CLI — reached as
  `huddle provider <p> <entry>`; `internal/core/router/multi.go` is huddle-router itself.
- `internal/domain`: the pure logic both huddle and the engine share (briefs, jobs, budgets, the provider
  catalog and the routing decisions).
- `internal/adapters`, `internal/gateway`, `internal/pki`, `internal/netproxy`, `internal/hookplan`,
  `internal/harness`, `internal/plugins`, `internal/testkit`: I/O adapters, the `/gateway` routes, the local
  CAs, the proxy environment, the merged hook plan, the harness configs, the plugin suites and the test kit.
- `internal/plugins`: the plugins' shared suites — the plugin surface, the launcher e2e, the fresh-install e2e
  and `TestSiblingsMatchZai`, which keeps the five provider plugins copies of one another.
- `web/`: one Bun workspace with Vite+: `ui` (shared components) and `hub` (the dashboard, with its Radar and
  Kanban tabs). The hub's Radar tab (`web/hub/src/radar/`) is the one copy of Radar's pages; the radar server
  serves its API only. The Kanban tab (`web/hub/src/kanban/`) also builds as the board server's own page, into
  `internal/kanban/web/dist/`.
- `plugin/`: what Claude Code installs for huddle (`.claude-plugin/plugin.json`, hooks, commands, skills,
  agents, `dist/`).
- `plugins/<p>-plugin-cc/`: the five provider plugins (zai, kimi, deepseek, minimax, qwen), each with its
  agents, commands and a `dist/run` that runs `huddle provider <p> <entry>` on the one binary. They ship no
  hooks: huddle's hook process runs their parts.
- `.claude-plugin/marketplace.json`: the `muhmdraouf` marketplace with the six plugins.

## Architecture

- **One router and one hub per user** ([ADR-4](docs/adr/0004-router-process.md)). `huddle router serve` is
  huddle-router, its own process, running once a provider is set up; `huddle serve` is the hub (channels,
  registry, needs-you, the dashboard with its Radar and Kanban tabs), running once the huddle home is set up.
  `huddle up` starts the router, then the hub; a hub restart never touches the router. A second start of either
  finds the first and exits 0.
- **One router.** huddle-router listens on `127.0.0.1:18786` (`HUDDLE_ROUTER_PORT` moves it), health at
  `/huddle-router/health`, state under `~/.agents/huddle-router/` (`providers.json` lists the enabled
  providers, re-read every 2 s; one lane per provider, matched by catalog id, then the longest model prefix).
  `claude-*` goes to Anthropic untouched; a model no lane claims goes to Anthropic; the gateway's OpenAI and
  Anthropic routes are mounted under `/gateway`; Radar's `budget-status.json` can stop a provider. A handover
  serves a binary update and an RSS ceiling of 1 GiB (`HUDDLE_ROUTER_RSS_CEILING_MB`, checked every 30 s).
- **One hook process.** Each Claude Code event runs one `huddle hook <event>` process whose parts run in order
  (`internal/hookplan`): huddle's handler, its report, radar, kanban's session start, and the providers' parts
  (session-start re-applies every set-up provider's wiring; subagent-stop adds the usage line). A part owns a
  deadline and a hook never blocks a session.
- **Thin provider plugins.** zai, kimi, deepseek, minimax and qwen are data and slash commands: setup records
  the provider in `providers.json`, points Claude Code's `HTTPS_PROXY` at huddle-router with the shared local CA
  (`NODE_EXTRA_CA_CERTS` at `~/.agents/ca`), and adds its models to `/model`; jobs run as detached drivers of
  the one binary. The plugins meet huddle only through files (the spool, `budget-status.json`) and local HTTP.
- MCP is the interface for every agent: `publish`, `subscribe`, `poll` (long poll after a cursor), channels, tasks,
  claims and needs. Harnesses that support resource subscriptions get `notifications/resources/updated`; Claude Code
  also gets channel pushes.

## Code rules

- Go: expected failures are values (concrete error types, matched with `errors.As`); no package-level mutable
  state; `context.Context` is the first parameter; `CGO_ENABLED=0`; reproducible builds (`-trimpath`, fixed flags).
- Functional core, imperative shell: decisions are unit-tested with fakes; adapters do only I/O and are tested
  against real files, processes and temp directories.
- TypeScript: strict, ESM, Preact; `vp check` and `vp test` only (no other runner).
- A hook never blocks a session: it exits 0 on any failure and stays silent when it has nothing to say.
- Plug and play: never ask a user to export a variable, edit a config or run a setup step.
- Keys and tokens live in the OS keystore, never in chat or in files the tests do not own.
- Tests are hermetic: `HOME`, `HUDDLE_HOME`, `RADAR_HOME`, `CLAUDE_CONFIG_DIR` and the `XDG_*` directories point at
  temp dirs; tests never touch the real `~/.claude`, `~/.agents`, `~/.config`, the loopback ports the daemons
  serve (huddle-router's 18786, the hub's 18860-18869), or processes they
  did not start. ACME, DNS and push services are fakes.
- Every incident is a case in `docs/INCIDENTS.md`: cause, guard test, rule. A new incident adds its section and
  its guard in the same change.
- macOS and Linux only.

## Writing code

1. **Simple first.** Write the smallest design that solves today's problem. No abstraction, option or layer until a
   second real use needs it; three similar lines beat a premature helper.
2. **High cohesion, low coupling.** A package does one job and owns its data. Packages talk through small, explicit
   interfaces, never through each other's internals or shared globals.
3. **Everything statically typed.** Go uses structs for data and small interfaces declared by the consumer (no
   `map[string]any` or `interface{}` across a boundary). TypeScript runs in `strict` mode with `type`s and
   `interface`s and no `any`; JSON is decoded into those types at the edge and checked there.
4. **Good design, kept honest.** Clear boundaries, dependencies that point inward, names that say what a thing is.
   A pattern earns its place by removing code or a class of bugs, not by being a pattern.

## Tests

1. **TDD.** Write the failing test first, watch it fail, make it pass, then refactor. A bug fix starts with the
   test that shows the bug.
2. **E2E for every user-facing path.** Unit tests prove the parts; an end-to-end test against the real binary
   proves the path a user takes works as a whole.
3. **Frontend work ships with headless-browser E2E tests** (Playwright, Chromium) that run unchanged on a laptop
   and in CI.
4. **CI runs every test.** A test that runs on one machine only does not count. `mise run check` runs the gates
   locally; `.github/workflows/ci.yml` runs them on every push.
- Behaviour tests are scenarios: `internal/testkit/bdd` (Feature, Scenario, Given/When/Then/And) in
  `*_scenario_test.go`; e2e tests build the binary in `cmd/huddle/*_e2e_test.go`.

## Agent files

Everything an agent writes stays under `.agents/`, in the gitignored folders below, and nowhere else in the repo,
so none of it can reach git:

| Folder | What goes there |
|---|---|
| `.agents/scratch/` | notes, plans, drafts, logs, build caches, downloads |
| `.agents/secrets/` | keys and tokens a test run needs (mode 0600); docs name them, never print them |
| `.agents/tickets/` | the kanban board and the tickets agents work from |
| `.agents/worktrees/` | one git worktree per ticket |

Only `.agents/skills/` is committed. Never write working files into the source tree, `/tmp` or the home
directory, and never pass a secret on a command line.

## Comments and docs

- Present tense, describing the code as it is. No history, no "used to", no earlier designs.
- Comment only the non-obvious why (a constraint, a protocol rule, a security reason).
- README and the plugin's skill text change in the same commit as the behaviour.

## Writing

Applies to docs, command and skill text, CLI output, commit messages and replies.

- Lead with the action or the answer; number steps that happen in order.
- Short sentences, plain words, specific numbers.
- No AI jargon ("seamless", "robust", "leverage", "comprehensive", "powerful", "delve", "unlock", "empower",
  "elevate", "streamline", "cutting-edge", "game-changer"). No emojis. No preamble or sign-off.

## Commits and pull requests

**Hard rule: no AI attribution, ever.** No `Co-authored-by` trailer, no "Generated with" line, no session link, no AI
author or committer, in commits, pull requests, tags or release notes. `.claude/settings.json` turns Claude Code's
attribution off; `node scripts/check-attribution.mjs` runs in CI on every push.

- One line, Conventional Commits: `<type>(<scope>): <message>`. No body, no co-author, no AI jargon, no emojis. Types:
  `init`, `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`. Scopes: `huddle`, `radar`, `kanban`,
  `core`, `web`, `plugin`, `repo`. Imperative, lowercase start, no trailing period, no body.
- Signed (`git commit -S`), as Raouf. Stage files by name; never `git commit -a` or `git add .`.
- Never commit secrets: the gitleaks pre-commit hook guards each commit and CI scans the whole history.

## CI

`.github/workflows/ci.yml`: plan, repository rules (gitleaks, attribution, actionlint, plugin validation), the Go
job (gofmt, tidy, build, vet, staticcheck, govulncheck, race tests with coverage gate, release build), the web
job (`bun run check`), the macOS job (tests and the reproducible-build proof) and the gate
`All checks passed`. Every job is capped at 10 minutes. Reproduce failures locally; re-run only the failed jobs.

## Releases and the development channel

- No release and no tag while there are no consumers. The plugins carry no version, so every marketplace update
  is one, and the launchers build huddle from the marketplace checkout's own commit with Go (see the launcher
  spec in `scripts/launcher.mjs`): once per commit, kept three builds, hooks never waiting for it. The rule
  flips to signed release binaries the day huddle has a second user; darwin code signing is the open question
  then.
- The release machinery stays ready for the day it is used: `go run ./scripts/build -check` (or
  `-manifest -check`) rebuilds the four targets and exits 1
  naming every target whose digest differs from the committed `release/v<VERSION>.json`; exit 0 when all match,
  and it writes nothing.
- A pushed tag `v<VERSION>` runs `release.yml`: four binaries, `SHA256SUMS`, an attestation, notes from
  `CHANGELOG.md`. The digests must match the committed `release/v<VERSION>.json`.
- Every version has a `CHANGELOG.md` section: what a user gets, grouped by part, fixes with their symptom, and how to
  verify a download.
- A Go change after a manifest is written changes the digests: regenerate the manifest and every plugin's
  `dist/run` in the same branch.
