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
  (internal/adapters/process/live_test.go), `TestMatchLeftovers` (internal/testkit/leftover/leftover_test.go)
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
- **Guard:** `TestMatchLeftovers` (internal/testkit/leftover/leftover_test.go) — tmpguard matches only processes
  under a test's temp dir
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
- **Guard:** `TestNoChainScenarios` (internal/routerd/nochain_scenario_test.go), `TestFailAlertScenarios`
  (internal/core/router/failalert_scenario_test.go), `TestE2ENoChainARouterNeverRoutesThroughAnotherRouter`
  (cmd/huddle/nochain_e2e_test.go). huddle-router adopts no 0.0.1 port and carries no 0.0.1 migration, so the
  no-chain guard is what keeps this incident from recurring.
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

## 2026-10-25, a hub restart cut every Claude stream in flight

- **What happened:** at 01:28:15Z a `huddle down` during a deploy stopped the hub, pid 54041. The session's
  transcript says `API Error: Connection lost mid-response` at 01:28:15.079Z, and Claude Code does not retry that
  error. Six hub restarts ran that night, each one a cut.
- **Cause:** huddle-router ran inside `huddle serve`, so stopping the hub closed the CONNECT tunnel of every Claude
  request in flight. The socket handover protected only a router running on its own.
- **Guard:** `TestE2EUpStartsARouterAndAHub` (cmd/huddle/up_two_processes_e2e_test.go),
  `TestE2EDownStopsAHubTheLauncherStarted` (cmd/huddle/router_ownership_e2e_test.go),
  `TestE2EAHubRestartKeepsAStreamingTurn` (cmd/huddle/hub_restart_stream_e2e_test.go)
- **Rule:** huddle-router is its own process and the hub never hosts it
  ([ADR-4](adr/0004-router-process.md)); a hub restart never touches the router.

## 2026-10-25, a healthy router had no pid to show

- **What happened:** the gate's `TestE2ERouterOverhead` saw the daemon answer its health path, read
  `run/router.json` once and found no file: "the daemon published no pid". It passed on every rerun alone.
- **Cause:** the daemon served its listener first and wrote its record after adopting the legacy ports, so
  under load a caller could see it healthy before the record landed. `huddle router start` and `huddle status`
  read the pid the same way the test did.
- **Guard:** `TestTheRecordLandsBeforeTheFirstHealthAnswer` (internal/routerd/record_test.go)
- **Rule:** the daemon publishes its record before the serve goroutine starts, so a router seen healthy always
  has a pid to show.

## 2026-10-26, a deploy found no router to hand over from

- **What happened:** the 8a3cac9 deploy printed "the daemon (pid 29918) never handed over". The router log read
  "takeover: no daemon to take over from at run/handover.sock", and `run/` held no socket. The router kept
  serving from a build of 2026-10-25 21:37, so no later deploy reached it.
- **Cause:** a daemon that hands over retires its socket file at once, and the taker opens its own at the same
  path. The old daemon's stop ran again when its drain ended ten minutes later, and it removed that path, which
  was now the taker's socket.
- **Guard:** `TestTheRetiredDaemonLeavesItsTakersSocketAlone` (internal/routerd/handover_test.go)
- **Rule:** a daemon removes its handover socket file once, either at the handover or at its stop, never both.

## 2026-10-27, two test binaries leased the same port

- **What happened:** zai job 261027-3gxj5j saw `TestAutostartOffLeavesHuddleDownWithNoChannel` fail because
  another binary's hub answered on the port it had leased: `ports.Free` kept its answers in one map per test
  binary, and `go test` runs package binaries in parallel.
- **Cause:** a per-process lease under parallel package binaries.
- **Guard:** `TestAFreshSearchSkipsThePortAnotherBinaryAnswered` (internal/testkit/ports/ports_test.go)
- **Rule:** a port a child binds is leased through TMPDIR.

## 2026-10-27, a handover on a busy machine refused dials

- **What happened:** the RSS-ceiling handover e2e saw 1-6 refused dials under five parallel build jobs.
- **Cause:** the taker ran its setup before its first Accept, while the old daemon had closed its copy.
- **Guard:** `TestAcceptAhead` (internal/routerd/acceptahead_test.go), `TestHuddleRouterHandoverScenarios` (internal/routerd/handover_scenario_test.go)
- **Rule:** a daemon accepts on a live port before any setup.

## 2026-10-28, Radar's writes were refused through the hub

- **What happened:** saving a budget, changing Radar's settings or clearing history from the hub's Settings
  answered 403 "writes need x-huddle-action naming the action"; HV-SET-06's add-budget e2e found it.
- **Cause:** Radar's controller `sendJson` (web/hub/src/radar/app/controller.ts) sent no `x-huddle-action`
  header, and the hub's write guard (internal/huddle/server/guard.go, `ActionHeader`) refuses a write without
  it; no test wrote through the hub.
- **Guard:** `"saves a new budget and re-polls the spend"` (web/hub/test/radar/app-controller.test.ts), and
  the Spending e2e in web/e2e/settings.spec.ts
- **Rule:** every write a page makes through the hub names its action in `x-huddle-action`, and an e2e writes
  through the hub.

## 2026-10-28, a hard reboot left Radar's history malformed

- **What happened:** after a hard reboot at 13:09, `history.db` failed `quick_check` as malformed. Every hub
  start logged "history unavailable" once in its run log and ran without history for about 12 hours: history
  reads answered 503 and new requests were not kept. A recovered copy (`.recover`, integrity ok) replaced it
  with nothing lost.
- **Cause:** the stores ran WAL with `synchronous=NORMAL` and no `checkpoint_fullfsync`; on macOS a plain
  `fsync` leaves the drive's own cache unflushed, so a power loss mid-checkpoint tore the main file.
- **Guard:** `TestOpenDBFlushesCheckpointsPastTheDiskCache` (internal/radar/history/driver_test.go and
  internal/radar/ledger/db_test.go), `TestOpen/"flushes checkpoints past the disk cache"`
  (internal/huddle/store/store_test.go)
- **Rule:** every SQLite store Radar and the hub open sets `checkpoint_fullfsync(1)`.

## 2026-10-29, the hub burned two cores on a busy machine

- **What happened:** with the dashboard open on a machine past 200 sessions, the hub held near two full
  cores and reached 526 MB resident.
- **Cause:** on a machine past 200 sessions, every new session made the Radar store walk every record to
  drop one stale session, and a line for an old session brought it back to be dropped again; every store
  change also threw away every cached view, so each poll and each stream push rebuilt and re-sorted up to
  200,000 records, and the pricing ran regexps per request.
- **Guard:** `TestE2EHubStaysLight` (cmd/huddle/hub_light_e2e_test.go)
- **Rule:** the hub spends under 0.2 s of CPU over 20 s idle and under 12.5 s over the fixed 30 s polled
  workload, and stays under the guard's RSS ceiling; a change that costs more fails CI.

## 2026-10-29, an e2e hub built the notify applet and showed a real notification

- **What happened:** the full gate on `0d673c99` failed `TestE2ETheDigestOverTheCLI` with
  `TempDir RemoveAll cleanup: unlinkat .../.agents/huddle/notify/Huddle.app/Contents/Resources: directory not
  empty`. The digest test's blocked task made the hub notify the owner: with no applet in the temp home it
  spawned `osascript` (a real notification on the screen) and started the applet build in the background, which
  ran `osacompile`, `plutil`, `codesign` and `lsregister -f` under the temp HOME while the test's cleanup was
  deleting it.
- **Cause:** an e2e hub started with the notify switch's default (on), so the first needs-you event built the
  notify applet from the temp HOME, registered it with the real Launch Services database, and showed real
  notifications.
- **Guard:** `TestE2EABlockedTaskNeverBuildsTheNotifyApplet` (internal/huddle/cli/e2e_test.go)
- **Rule:** e2e hubs run with desktop notifications off; only in-process fakes exercise the notifier.

## 2026-10-29, the design guard counted prose and hid real classes

- **What happened:** the hub's design guard counted prose words as daisy and legacy classes (`list` out of
  "The board list is unreachable." in KanbanTab.tsx, `code` and `row` out of doc comments) and, in
  radar/app/AgentTranscript.tsx, hid 10 daisy and 1 legacy offences behind a fake string, so the file's
  baseline recorded a false count.
- **Cause:** the design guard's lexer reads a word-internal apostrophe in JSX text as a string opening, a
  backtick or quote inside a regex literal as a template or string, and any upper-case const's value as a
  class list.
- **Guard:** `"a word-internal apostrophe in JSX text opens no string"`, `"a regex literal with a backtick or
  a quote opens no string"`, `"division and JSX closing tags are not regex literals"`, `"an upper-case const
  holding prose is not a class list"` (web/hub/test/design-guard/scan.test.ts)
- **Rule:** a baseline a lexer fix shows to be false is reset once to the true count; from there it moves
  down only.

## 2026-10-29, the light guard passed while the live hub held 700-800 MB

- **What happened:** the guard answered 116 MB resident and passed, while the live hub held 700-800 MB and a probe
  of the snapshot home read 604 MB peak.
- **Cause:** the guard's seed did not look like the live world: it carried transcripts only — about 20k requests
  and no spool lines — while the live hub's store held 74k requests and 65k spool lines of route calls and hook
  events, and the parse trees of those hook events are what holds most of the resident size.
- **Guard:** `TestE2EHubStaysLight` (cmd/huddle/hub_light_e2e_test.go), whose polled run serves a home
  seeded with the live shape at quarter scale
- **Rule:** the guard's seed is the live shape scaled down to keep the cold catch-up inside CI's 10-minute job,
  and its RSS ceilings only move down.

## 2026-11-01, e2e runs left their hubs and routers running

- **What happened:** eight `huddle serve` processes from one worktree's Playwright run folder ran for three days
  under launchd and loaded the machine, so later gates went flaky.
- **Cause:** the teardown knew only the two hub pids the setup saved; a killed run never reached the teardown, and
  the next run's setup emptied the run folder, losing the pids. In the Go harness, `router start` runs
  `router serve` with `Setsid`, so the router outlived the test binary that asked for it, and a killed `go test`
  never reached any cleanup.
- **Guard:** `"a run's processes are the executables under its bin folder, and nothing else"`,
  `"stopping a run's processes ends a detached one under its bin folder"`,
  `"the reaper ends a killed run's processes"` (scripts/e2e-leftovers.test.ts),
  `TestSweepStopsADetachedProcessUnderTheRoot`, `TestSweepStopsAProcessWhoseWorkingDirectoryIsOutsideTheRoot`,
  `TestStaleRootsOfDeadOwnersAreSwept` (internal/testkit/leftover/leftover_test.go)
- **Rule:** an e2e harness stops every process under its run folder: at the teardown, at the next start, and through
  a reaper when the runner is killed; a teardown that finds one still running fails the run. The Go e2e suite
  (cmd/huddle) keeps everything in one run folder, sweeps it at the end, and sweeps a killed run's folder at the
  next start.

## 2026-11-01, sessions stopped hearing their channel after the hub moved

- **What happened:** the hub restarted on another port; a message sent to the homelab session was never pushed to
  it, and the session's `huddle mcp` held no connection to the hub, while the hub's own repo kept hearing it.
- **Cause:** the bridge retried its live stream against the address it read once at start, never re-reading who
  the session is; and `Identify` answered the address the join credential or the project file had recorded, which
  carries the port the hub had at join time. A hub that lost the member (a restart on a store without it)
  still answers the live stream 200, because the live route checks no member, so the bridge had no refusal to
  react to and the session stayed off the channel's list.
- **Guard:** `TestE2ETwoReposFollowTheHub` (cmd/huddle/channel_two_repos_e2e_test.go),
  `TestIdentifyFollowsTheLiveHubOnLoopback` and `TestFollowReadsTheSessionAgainBeforeAReconnect`
  (internal/huddle/cli/identity_live_test.go), `TestE2EASessionTheHubLostJoinsAgainAndHearsTheNextAsk` and
  `TestE2EASessionThatLeftOnPurposeStaysLeft` (cmd/huddle/channel_rejoin_e2e_test.go),
  `TestASessionTheHubHoldsNoRowForJoinsAgainAndTheMissedAskArrivesOnce`,
  `TestAStreamThatStaysUpRejoinsWithinOneCheckAfterTheHubDropsTheMember`,
  `TestASessionThatLeftOnPurposeStaysLeft`, `TestASessionThatLeavesThroughTheBridgeStaysLeft`,
  `TestASessionThatJoinsAgainThroughTheBridgeIsKeptListedAgain`,
  `TestAMissingChannelStopsRejoiningAndSaysHowToCreateIt`, `TestA401SaysToRunHuddleJoinAndNeverJoins`,
  `TestACheckNeverRunsBesideTheBridgesOwnJoin` and `TestARefusedStreamAnswersTheStatusAsAValue`
  (internal/huddle/cli/huddle_mcp_rejoin_test.go)
- **Rule:** a session re-resolves the hub before every reconnect, and a loopback address is the user's one hub:
  the port it runs on now wins over the one a credential or a project file recorded. The bridge also reads the
  channel's list on every reconnect and every five minutes while its stream is up, and joins again with the hook's
  join call when the hub holds no row for the session. A row in state left is a leave someone asked for and is
  never joined again; a leave sent through the bridge ends the checks until a join through the bridge starts them
  again. A 404 (no such channel) and a 401 (no credential) are said once and never joined.

## 2026-11-01, the body store split no real request

- **What happened:** 34,608 of 34,941 stored input bodies were one span, so the content-addressed store saved
  nothing across turns of a conversation (only the deflate helped); the store grew 2.2 GB a day.
- **Cause:** `splitBody` read a top-level member whose value is an object (`metadata`, `thinking`) as only its
  opening brace. The closing-brace check then met a nested or later token instead of the end, and the whole body
  stayed one span. Every fixture had only string, boolean and array members, so no test met a real request.
- **Guard:** `TestSplitBody` (its subtests for an object-valued member, an object as the last member, and empty or
  nested top-level values), `TestBodyStoreSplitsARealShapedBody`, `TestBodyStoreSharesTheConversationPrefix` and
  `TestBodyStoreRealShapedScenarios` (internal/radar/ledger).
- **Rule:** a splitter or store test builds its body in the shape of the real request, object-valued members
  included; two consecutive turns of one conversation share every span but the last message.

## 2026-11-01, CI red for a week while the local gates stayed green

- **What happened:** the Go (rest) job hit its 10-minute cap on every main run from 2026-10-25 (runs 40228809970,
  40395977951, 40829575260 and 41297396357) while deploys went out on local runs. The last main run, 41314513918
  on d531913f, failed on more than the timeout: staticcheck U1000 (an unused field in
  `internal/huddle/hooks/idle_wait_test.go`) and, in every web job, the e2e-leftovers test "stopping a run's
  processes ends a detached one under its bin folder" on Linux. The macOS jobs were skipped.
- **Cause:** the deploy gate ran the tests on the owner's Mac only. It ran neither staticcheck nor the Linux
  runs, and it never read the CI result, so a red main was invisible to it. The rest shard had also grown past what
  one 10-minute runner holds: `internal/huddle/server` and `cmd/huddle` alone took 5 to 10 minutes each.
- **Guard:** `scripts/ci-green.mjs` reads the last completed `ci.yml` run on main with `gh run list` and refuses
  on anything but success: `"passes when the last completed main CI run succeeded"`,
  `"refuses when the last completed main CI run failed, naming the run"`,
  `"refuses when the last completed main CI run was cancelled"`,
  `"refuses when the last completed main CI run timed out"`,
  `"asks gh only for the last completed ci.yml run on main, a read"`,
  `"fails closed with one line when gh is not installed"`,
  `"fails closed with one line when gh cannot reach GitHub"`,
  `"fails closed when main has no completed ci.yml run"` and
  `"fails closed when gh answers something that is not a run"` (scripts/ci-green.test.mjs). The shard split that
  keeps every Go job under its cap is held by `TestEveryModulePackageFallsInExactlyOneShard`,
  `TestEveryTestRunsInExactlyOneShardOfASplitPackage` and `TestCIRunsEveryShardOnLinuxAndMacOS`
  (scripts/covergate/main_test.go).
- **Rule:** no deploy lands while the last main CI run is red: the deploy script calls
  `node scripts/ci-green.mjs` first, and a gh that is missing or offline refuses too. The push that fixes main is
  the owner's call.

## 2026-11-01, transcript secrets reached history.db in plain text

- **What happened:** history.db's transcript `content` (245 MB: tool results, prompts, commands, thinking) was
  stored with no redaction, so a `cat` of a config, an `env` dump or a pasted key sat in the file as typed. The
  router's 6 secret shapes also missed a secret right after a JSON `\n` escape, so `...\nsk-...` in a stored
  request body kept its key.
- **Cause:** `internal/radar/ingest/transcript.go` called no redactor and `internal/radar/shared/redact.go` covers
  hook events only; the one body redactor lived in the ledger package, out of the history writer's reach. Its
  shapes started at a `\b`, and in JSON text the `n` of `\n` and the first letter of a token are one word, so no
  boundary fell between them. Every fixture put its secret between spaces.
- **Guard:** `TestPutContentRedactsEveryShape`, `TestPutContentKeepsOrdinaryTextExact`,
  `TestPutContentCountsTheStandingSideOnly`, `TestPutContentGrowthCountsRedactedLength`,
  `TestWriterIngestRedactsTranscriptSecrets` and `TestMigrationLeavesStoredContentAsItWas`
  (internal/radar/history); `TestBodyStoreRedactsEveryShape` (internal/radar/ledger);
  `TestRedactSecretsRedactsEveryFake`, `TestRedactSecretsCountsEveryMatch` and
  `TestRedactSecretsLeavesOrdinaryTextAlone` (internal/radar/shared).
- **Rule:** one shape list in `internal/radar/shared` (`RedactSecrets`), run before any store writes text; a shape
  reads plain and JSON-escaped text alike. A new shape comes with a fake in `internal/testkit/fakesecret` and is
  held by a test in both stores. Rows written before this change keep their text until the history TTL removes
  them; nothing rewrites them.

## 2026-11-01, a hook event wrote two spool lines on macOS CI

- **What happened:** `TestE2EOneSpoolLinePerHookEvent` failed on the macOS runner of run 41297396357 ("2 spool lines
  for one hook event, want 1") and passed on Linux and on the owner's Mac. The same run's
  `TestE2EAnOwnerMessageReachesOnlyItsSession` saw route "next_prompt" where it wants "now".
- **Cause:** the hub skips its own spool write for a Claude Code report from its own machine, telling the machine
  by name. The hook reports the name from the home's `config.toml`, a hostname cut at its first dot and fitted to
  the device-name rule, while the hub compared it with the raw `os.Hostname()`. Wherever the hostname carries a
  domain (a Mac runner's `host.local`) or the home names the machine something else, the names differed, the hub
  took the report for another machine's and wrote the line a second time. The same mismatch sat under the needs
  list's own-machine check.
- **Guard:** `TestTheHubTellsItsMachineByTheNameTheHomeConfigHolds` (internal/huddle/server) names the machine in
  `config.toml` and posts a report under that name and under another;
  `TestTheHubKnowsItsOwnSessionsByTheMachineNameTheHooksReport` (internal/huddle/server) wants the push route
  for a session that reports that name; `TestE2EOneSpoolLinePerHookEvent` and
  `TestE2EAnOwnerMessageReachesOnlyItsSession` (cmd/huddle) hold the whole paths.
- **Rule:** one machine has one name: the hub reads it from the home's `config.toml`, the place every hook reads it
  from, never from the operating system's hostname.

## 2026-11-02, a test run showed a real notification with the Script Editor icon

- **What happened:** the owner's Notification Center showed "Huddle · The golden channel / Task t-doc is blocked
  (lead)" under the Script Editor icon. The channel and the task are the golden fixture, so a test run had posted
  a real notification to the owner's desktop.
- **Cause:** two bugs. The test kit gave a child no notification log, so a hub that reached `Notifier.Send` with
  the switch on showed a real banner, because only `HUDDLE_NOTIFY=0` or `HUDDLE_NOTIFY_LOG` stops one and the kit
  drops every inherited `HUDDLE_*` variable. And `Send` fell back to a bare `osascript` line whenever the
  notification applet was not built under `HOME` (a temp home never has it), which shows Script Editor's icon;
  radar's `NotifyCommand` had the same fallback for an empty icon. The hand-built environment maps of the cmd/huddle
  e2e tests named no log either.
- **Guard:** `TestEnvironSendsNotificationsToALogUnderHome` and `TestSandboxSendsNotificationsToALogUnderItsHome`
  (internal/testkit/isoenv) want `HUDDLE_NOTIFY_LOG` under the home of every child and process the kit builds;
  `TestAChildOfEnvironThatStartsANotifierIsRecorded` and `TestInstalledShimsRecordTheirArgvAndFail`
  (internal/testkit/isoenv) hold the `osascript`, `osacompile` and `notify-send` shims;
  `TestEveryHandBuiltEnvironmentNamesANotifyLogAndTheShims` (cmd/huddle) wants both renderers of the hand-built
  environment maps to carry the log and the shims;
  `TestARunWhoseChildStartsANotifierFailsNamingTheSpawn` (scripts/tmpguard) wants a run that spawned one to fail
  naming it; `TestNotifier` (internal/huddle/outside) and `TestDesktopNotifications` (internal/radar/alerts)
  want a notification on macOS to go out through the applet's stub or not at all, and an alert radar could not
  show to stay unnotified until the applet is built.
- **Rule:** a test never reaches the real desktop: the shared kit sets `HUDDLE_NOTIFY_LOG` and puts the shims first
  on the `PATH`, and tmpguard fails a run that spawned a notifier. Huddle never shows a notification without its
  icon: a missing applet is built first; the hub drops a notification whose build failed with one log line, and
  radar keeps an alert unnotified until the applet exists.

## 2026-11-02, the launcher test left an idle waiter running and the leftover guard did not see it

- **What happened:** an idle waiter with a 2,000,000 s timeout ran on, its binary under
  `/tmp/TestLauncherFreshInstall*`, long after the suite that started it had passed.
- **Cause:** `TestLauncherFreshInstall` runs each hook of the plugin before setup, and the session-start hook arms
  an idle waiter that listens for the session's join. The test never stopped it. Tmpguard points `TMPDIR` at its
  own folder and reports any process left running from under it, but `isoenv.Sandbox` dropped `TMPDIR`, so the
  suite's temp dirs sat in the system temp dir, outside the folder the guard watches. A process that started
  inside a run shorter than a second also fell through: `ps` rounds its elapsed time up to whole seconds, and the
  match took 00:01 for older than the run.
- **Guard:** `TestTheWorldStopsTheWaitersItsHomeRecorded` (internal/huddle/cli/launcher_test.go),
  `TestTheGuardNamesAChildThatOutlivesASandboxedSuite` (scripts/tmpguard/main_test.go),
  `TestMatchCountsAProcessStartedInsideARunShorterThanOneSecond` (internal/testkit/leftover/leftover_test.go)
- **Rule:** a launcher world stops the idle waiters its home recorded when its test ends, and a hook run before a
  join gives up on it after one second. `isoenv.Sandbox` keeps `TMPDIR` as the process got it, so tmpguard sees
  every process a suite leaves running from a temp dir.

## 2026-11-02, a transcript a hook named waited 30 s for radar, and the tests passed by luck

- **What happened:** three hub tests that wait for radar's numbers failed on every run of an unchanged tree,
  hours after a full gate had passed them.
- **Cause:** the change feed watches a project folder only once it holds a recent transcript, and its fallback
  scan runs every 30 s. A transcript created later in a folder the feed does not watch raises no event, so a
  session that names it only through its hook (any harness without Claude Code's session registry) reached
  radar at the next scan, up to 30 s after the hook, where the old 1 s relist read it within a second. The hub's
  own spool line for another harness's report also left out the transcript path. The tests passed only when the
  watcher's first read happened to run after they wrote the transcript, which a loaded machine made likely.
- **Guard:** `TestATranscriptASpoolLineNamesIsReadOnThePassThatReadsTheLine`
  (internal/radar/ingest/watch_folders_test.go) and `TestAReportNamesItsTranscriptInRadarsSpoolLine`
  (internal/huddle/server/registry_test.go)
- **Rule:** a change that makes a loop lighter keeps every latency the loop gave, with a test that proves it on an
  idle machine. A feed pass tails the transcript a spool line names the moment it reads the line, and the hub's
  spool line for a report carries the report's transcript path.

## 2026-11-02, radar's first spool lines waited 30 s when the spool folder did not exist yet

- **What happened:** `TestTheOwnersInsightsNameAFailingLane` failed in one run of about fifty (no lane tip in
  20 s) and passed alone and on the rerun.
- **Cause:** the hub makes radar's state and spool folders when it writes server.json, after the ingest has
  started. The change feed arms the spool folder in the ingest's first moments; when that came first the folder
  was not there, the arm failed, and the feed watched nothing in it until a later scan, up to 30 s. A fresh
  install met the same gap for its first hook lines. A second gap sat beside it: on macOS the feed reports a new
  file's creation before it watches the file, so a line written right after a spool or transcript file was
  created, after the pass the creation raised had read it, raised no event and waited for the scan too. A probe
  that created a spool day file and appended at once lost the second line in most runs.
- **Guard:** `TestTheSpoolFolderIsWatchedEvenWhenNothingHasWrittenToItYet` and
  `TestAWriteThatRacesTheWatchOfANewFileIsStillRead` (internal/radar/ingest/watch_folders_test.go)
- **Rule:** the ingest makes the state and spool folders before the feed arms them, and the feed watches a created
  spool or transcript file before anything reads it (`FolderWatch.WatchFile`), so a write that lands while the
  watch is made is in what the read finds and a later one raises its own event.
