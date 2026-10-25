# Incidents

Each incident, its cause, and the test that now stops it. A guard named here must exist:
`TestIncidentGuardsExist` checks.

## 2026-10-17, a worker rewrote the owner's Claude Code config

- **What happened:** a worker ran `export HOME=$T CLAUDE_CONFIG_DIR="$HOME/.claude"` on one line; the shell
  expanded `$HOME` before the export took effect, so `claude plugin marketplace add` changed the real
  `~/.claude`.
- **Cause:** the shell expands `$HOME` on the same command line before `export` sets it.
- **Guard:** `TestEnvironDropsEveryVariableThatNamesTheOwnersState` (internal/testkit/isoenv/isoenv_test.go),
  `TestNoTestBuildsAChildEnvironmentOutsideIsoenv` (internal/testkit/isoenv/guard_test.go)
- **Rule:** every child process gets its environment from isoenv, with literal temp paths.

## 2026-10-20, tests retired the live zai router twice

- **What happened:** a test ran the session hook with the real HOME, started `huddle.test router serve`, and
  that process retired the live router; workers got HTTP 400.
- **Cause:** an unsandboxed test binary became a daemon that took the owner's router over.
- **Guard:** `TestTheProcessIsSandboxed` (cmd/huddle/sandbox_test.go, internal/core/cli/sandbox_test.go),
  `TestIsTestBinary` (internal/adapters/process/testbin_test.go), `TestLiveExecutableOnThisTestBinary`
  (internal/adapters/process/live_test.go), `TestMatchLeftovers` (scripts/tmpguard/main_test.go)
- **Rule:** every test binary runs sandboxed, and a `.test` binary never starts a daemon.

## 2026-10-21, a test reached the live hub's port

- **What happened:** tests named ports in the 18786-18879 range the owner's live router and hub run on.
- **Cause:** a literal port where a free port belongs.
- **Guard:** `TestNoTestReachesTheLiveRouterPort` (internal/testkit/isoenv/port_guard_test.go),
  `TestEnvironNamesTheChildItsOwnRouterPort` (internal/testkit/isoenv/isoenv_test.go)
- **Rule:** tests take ports from `ports.Free`, never a literal in that range.

## 2026-10-21, an isolated run wrote the owner's hub config

- **What happened:** a run with a temp HOME but no HUDDLE_HOME wrote its port into the real
  `~/.config/huddle/config.toml`.
- **Cause:** `XDG_CONFIG_HOME` outranks HOME, and the run left it unset.
- **Guard:** `TestXDGDirPrefersTheXDGRootAndFallsBackToHome` (internal/harness/harness_test.go),
  `TestEnvironDropsEveryVariableThatNamesTheOwnersState` (internal/testkit/isoenv/isoenv_test.go)
- **Rule:** isolation sets HUDDLE_HOME, HUDDLE_DATA, RADAR_HOME, HUDDLE_ROUTER_HOME and every XDG_* variable.

## 2026-10-21, a script signalled 76 of the owner's processes

- **What happened:** `pgrep -f "$B mcp" -n` took `-n` as a second pattern on macOS, matched every process with
  `-n` in its arguments, and the script sent SIGTERM to all of them.
- **Cause:** macOS `pgrep -f` reads a second argument as another pattern, so `-n` matched far more than the
  script's own children.
- **Guard:** `TestMatchLeftovers` (scripts/tmpguard/main_test.go) — tmpguard matches only processes under a
  test's temp dir
- **Rule:** signal only a pid from `$!` or an isolated port, checked with `ps` first.

## 2026-10-21, the router split off the hub, and remove left our proxy values

- **What happened:** a setup started a second router because it compared a symlinked path with a resolved one;
  `/zai:remove` recorded our own CA and NO_PROXY as the user's values.
- **Cause:** the ownership check compared the path as typed, and remove treated the setup's own values as the
  user's.
- **Guard:** `TestRouterOwnership` (cmd/huddle/router_ownership_e2e_test.go), `TestLiveExecutable`
  (internal/adapters/process/live_test.go)
- **Rule:** compare resolved executables, and remove takes out only the values we wrote.

## 2026-10-21, injected text named sessions

- **What happened:** Claude Code's `<task-notification>` and `<system-reminder>` prompts became session titles
  and tasks on the Now page.
- **Cause:** the report hook took any prompt's words as the session's name.
- **Guard:** `TestTheReportHookLetsNoInjectedPromptNameTheSession` (internal/huddle/hooks/report_test.go)
- **Rule:** only a prompt the person typed names a session.

## 2026-10-21, a crashed session held a channel forever

- **What happened:** a session that crashed or was kicked never left, counted as online, and blocked removing
  the channel.
- **Cause:** presence had no staleness, so a dead session held its seat.
- **Guard:** `TestChannelsRemove` (internal/huddle/server/channels_remove_test.go)
- **Rule:** a session silent past 900 s is stale and counts as gone.

## 2026-10-21, a router routed claude traffic through another router

- **What happened:** every `claude-*` request through huddle-router failed for six hours, answered with 502s, and
  nothing said why.
- **Cause:** the daemon's `HTTPS_PROXY` and `HTTP_PROXY` named another router, one of another generation on a port
  that was not the known one, so the daemon tunnelled its claude traffic through it; a failing Anthropic path
  raised no event.
- **Guard:** `TestNoChainScenarios` (internal/routerd/nochain_scenario_test.go), `TestLegacyPortScenarios`
  (internal/routerd/legacy_scenario_test.go), `TestFailAlertScenarios`
  (internal/core/router/failalert_scenario_test.go), `TestE2ENoChainARouterNeverRoutesThroughAnotherRouter`
  (cmd/huddle/nochain_e2e_test.go)
- **Rule:** a daemon never routes through a proxy that answers a router health path.

## 2026-10-22, the hub tab crashed in a repaint loop

- **What happened:** the hub's tab fetched `/api/channels` about 500 times a second until it ran out of memory.
- **Cause:** the shell's channel-list effect was keyed on the `loadChannels` function, which `main.tsx` hands over
  fresh on every paint, and the list's answer repaints; fixed in `0e6d9c5` by keying the read on the open channel
  alone.
- **Guard:** `"reads once per channel however often the shell repaints with a new loadChannels"`
  (web/hub/test/app/App.test.tsx), `"no hub page asks one URL more than twice a second"`
  (web/e2e/fetchrate.spec.ts) — every hub page, watched for 5 s, fails above 2 requests a second to one URL
- **Rule:** an effect that fetches lists its inputs, and the hub e2e counts requests per URL.

## 2026-10-25, a hub start stopped mid-migration left hub.db unopenable

- **What happened:** under a load of 70, `TestServeEntryRefreshesAStaleService` cut a `serve` entry off at its
  600 ms budget while the registry was still migrating. The next start on the same home said
  `registry: migration 1: SQL logic error: table events already exists` and every start after it would have too.
- **Cause:** the migrations ran as plain statements, and the schema version was written once, after the last one.
  A start stopped between a migration and that write left tables without the version that says they exist.
- **Guard:** `TestMigrateRecordsEachStepAsItLands` (internal/huddle/hub/hub_test.go)
- **Rule:** each migration lands with its own version mark in one transaction, so a stopped start leaves a database
  the next start resumes.

## 2026-10-25, a test joined the live hub

- **What happened:** `TestHooksJsonCommand` failed on an untouched checkout: its hook said `You are in Huddle
  channel "huddle" as "lead"` and started a hub on 18861. `TestAloneEntryRunsFromRun` failed with `run = 6`, and
  `bun run e2e` died in its setup with `Huddle unreachable` in every worktree.
- **Cause:** huddle reads its identity from `CLAUDE_PROJECT_DIR`, else from the cwd, walking up to the nearest
  `.agents/huddle/huddle.json`. A test's cwd is a folder inside this repository, and the repository keeps the
  owner's own huddle.json (`{"channel":"huddle","as":"lead","autostart":true}`); every worktree under
  `.agents/worktrees/` walks up to the same file.
- **Guard:** `TestAHookRunFromInsideARepositoryWithAHuddleJsonReadsNoIdentity`
  (internal/testkit/isoenv/project_test.go), `TestHooksJsonCommand` (internal/radar/cli/launcher_test.go),
  `TestAloneEntryRunsFromRun` (cmd/huddle/alone_test.go), and the web e2e suite itself (`bun run e2e`, with
  web/e2e/global-setup.ts)
- **Rule:** a test process and every child it starts get `CLAUDE_PROJECT_DIR` set to an empty folder outside
  the repository: `isoenv.Sandbox` for the process, `isoenv.Environ` for a child, the e2e global setup for the
  web run.

## 2026-10-25, the web e2e run carried the daemon's router port

- **What happened:** the e2e global setup started its two hubs with temp homes and free hub ports, but no
  `HUDDLE_ROUTER_PORT`. Every child of those homes (the hubs' hosted router, `huddle hook`, `huddle mcp`) read
  the default, 18786: the port the installed daemon's router serves on this machine.
- **Cause:** the setup pinned the hub's port and nothing else; the router port is its own setting, read from the
  environment, and the Go test kit (`isoenv`) pins it while the web setup did not.
- **Guard:** `hermetic.spec.ts` (web/e2e), which reads the run's two router ports from the setup record.
- **Rule:** every environment the e2e setup builds names `HUDDLE_ROUTER_PORT`, one free port per hub, and the
  setup record carries both, the way `isoenv.Environ` does for every Go test child.
