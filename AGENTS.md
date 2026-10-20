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
go test ./...                                  # the whole suite
go test ./internal/kanban/... -run TestName    # one test
bun install && bun run check                   # the web workspace: web/{ui,hub,radar,kanban}
bun run build                                  # rebuild every page into internal/*/web/dist (committed)
go run ./scripts/build -out dist               # the release binaries, darwin and linux on amd64 and arm64
go run ./scripts/build -manifest               # write release/v<VERSION>.json (the launcher pins it)
claude --plugin-dir ./plugin                   # load the plugin from the working tree
node scripts/check-attribution.mjs             # no AI attribution in the history
```

## Layout

- `cmd/huddle`: the one binary. `huddle radar …` and `huddle kanban …` run radar's and kanban's commands.
- `internal/huddle`, `internal/radar`, `internal/kanban`: each part's server, CLI, hooks and store.
- `internal/contract`: the radar spool line and `budget-status.json` formats shared with the provider plugins in
  `MuhmdRaouf/claude-code-plugins`; `testdata/contract/` holds the golden fixtures both repositories keep identical.
- `internal/harness`: one adapter per harness (MCP config, hooks, transcript reader).
- `web/`: one Bun workspace with Vite+: `ui` (shared components), `hub`, `radar`, `kanban` pages.
- `plugin/`: what Claude Code installs (`.claude-plugin/plugin.json`, hooks, commands, skills, agents, `dist/`).
- `.claude-plugin/marketplace.json`: the `huddle` marketplace with the one plugin.

## Architecture

- One process per role: the hub server (channels, registry, needs-you, the dashboard with Radar and Kanban tabs),
  radar's server and the board server. Each is started by a hook or a command, never by an OS service, and comes
  back by itself on the next hook after a crash or a reboot.
- MCP is the interface for every agent: `publish`, `subscribe`, `poll` (long poll after a cursor), channels, tasks,
  claims and needs. Harnesses that support resource subscriptions get `notifications/resources/updated`; Claude Code
  also gets channel pushes.
- Huddle works alone. The provider plugins in `MuhmdRaouf/claude-code-plugins` depend on it and meet it only through
  files (the spool, `budget-status.json`) and local HTTP.

## Code rules

- Go: expected failures are values (concrete error types, matched with `errors.As`); no package-level mutable
  state; `context.Context` is the first parameter; `CGO_ENABLED=0`; reproducible builds (`-trimpath`, fixed flags).
- Functional core, imperative shell: decisions are unit-tested with fakes; adapters do only I/O and are tested
  against real files, processes and temp directories.
- TypeScript: strict, ESM, Preact; `vp check` and `vp test` only (no Biome, tsc or bare vitest scripts).
- A hook never blocks a session: it exits 0 on any failure and stays silent when it has nothing to say.
- Plug and play: never ask a user to export a variable, edit a config or run a setup step.
- Keys and tokens live in the OS keystore, never in chat or in files the tests do not own.
- Tests are hermetic: `HOME`, `HUDDLE_HOME`, `RADAR_HOME`, `CLAUDE_CONFIG_DIR` and the `XDG_*` directories point at
  temp dirs; tests never touch the real `~/.claude`, `~/.agents`, `~/.config`, ports 18787-18791, or processes they
  did not start. ACME, DNS and push services are fakes.
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
  `web`, `plugin`, `repo`. Imperative, lowercase start, no trailing period, no body.
- Signed (`git commit -S`), as Raouf. Stage files by name; never `git commit -a` or `git add .`.
- Never commit secrets: the gitleaks pre-commit hook guards each commit and CI scans the whole history.

## CI

`.github/workflows/ci.yml`: plan, repository rules (gitleaks, attribution, actionlint, plugin validation), the Go
job (gofmt, tidy, build, vet, staticcheck, govulncheck, race tests with coverage gate, release build, manifest
check), the web job (`bun run check`), the macOS job (tests and the reproducible-build proof) and the gate
`All checks passed`. Every job is capped at 10 minutes. Reproduce failures locally; re-run only the failed jobs.

## Releases

- A pushed tag `v<VERSION>` runs `release.yml`: four binaries, `SHA256SUMS`, an attestation, notes from
  `CHANGELOG.md`. The digests must match the committed `release/v<VERSION>.json`.
- Every version has a `CHANGELOG.md` section: what a user gets, grouped by part, fixes with their symptom, and how to
  verify a download.
- A Go change after a manifest is written changes the digests: regenerate the manifest and `plugin/dist/run` in the
  same branch.
