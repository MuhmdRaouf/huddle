# Test plan

One page listing every huddle feature the owner named, each beside the automated test that proves it.
Run everything from the repo root: `go run ./scripts/tmpguard -count=1 ./...`, `bun run check`, `bun run e2e`.

## Features and their proof

| Feature | Proof | What it asserts |
| --- | --- | --- |
| Install from the launcher | `cmd/huddle/fresh_install_test.go:TestFreshInstall`; `cmd/huddle/hub_e2e_test.go:TestE2EFreshInstallHubSatellitePWA/install`; `cmd/huddle/dev_channel_e2e_test.go:TestDevChannelE2E` | The launcher downloads the binary from a release server, checks it against its pinned digest, refuses a tampered asset, and runs it on a PATH with no JS runtime; with no release published it builds the checkout's own commit once with Go, keeps three builds, and hooks start the build detached. |
| A provider plugin from a fresh machine | `internal/plugins/fresh_install_e2e_test.go:TestFreshInstall` | On the committed launcher, `/<p>:setup` is the only step: the key page saves a key the running setup picks up, the provider's models route through huddle-router while `claude-*` reaches Anthropic untouched, a SIGKILLed daemon comes back at the next session start, and `setup --remove` restores `settings.json` byte for byte. |
| One router, one lane per provider | `cmd/huddle/router_e2e_test.go:TestE2EHuddleRouterWords`; `cmd/huddle/router_settings_e2e_test.go:TestRouterSettingsE2E` | `huddle router serve\|start\|status` behave as said, the second start steps aside with exit 0, and setup writes the proxy keys, the CA and `/model` so the provider's models route and the user's own proxy is never touched. |
| A router never routes through another router | `internal/routerd/nochain_scenario_test.go:TestNoChainScenarios`; `cmd/huddle/nochain_e2e_test.go:TestE2ENoChainARouterNeverRoutesThroughAnotherRouter` | A daemon whose proxy names a router — one on the known ports, one on its own port, or any loopback proxy that answers a router health path — drops it, logs `going direct`, and answers claude traffic from its own Anthropic while the other router carries nothing; a proxy that is no router is kept, and the daemon and hub spawns carry no proxy variable that names a router. |
| The socket handover | `cmd/huddle/handover_e2e_test.go:TestE2EHandoverAStartFromANewBinaryKeepsTheRouterPortOpen`, `TestE2EHandoverTheFullEditionTakesAStandaloneRouterOver` | A start from a new binary takes the listening socket over without the port closing, the old process drains; the full edition takes a standalone router over the same way. |
| The session watchdog | `cmd/huddle/watchdog_e2e_test.go:TestRouterWatchdogE2E` | A session's `huddle mcp` restarts a daemon that stays silent past 5 s, replaces a hung holder, and stays out of a program of the user's own. |
| The providers' hook parts | `cmd/huddle/hooks_providers_e2e_test.go:TestHooksProvidersE2E` | One merged hook process runs the providers' session-start work (re-apply, the jobs line, the removed-plugin cleanup) and the subagent-stop usage line. |
| First session starts the server | `cmd/huddle/first_session_e2e_test.go:TestE2EFirstSessionStartsHuddle` | A fresh install's SessionStart hook, with no channel and no `HUDDLE_URL`, starts the server through the launcher and it answers `/health`. |
| Hub on a local name over TLS | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName` (steps `hub_role_and_import`, `hub_up`) | A hub named `hub.example.invalid` serves its shell over TLS, verified against the CA that signed the pair, with no hosts-file edit. |
| Local CA and `huddle trust` | `internal/pki/pki_test.go:TestTheRootIsCreatedOnceAndReused`; `internal/huddle/cli/trust_test.go:TestTrustInstallsTheRootAndMarksItAsHuddles`; `web/e2e/fulltest.spec.ts` "a hub home with no import serves a leaf its own root signs" | The root is minted once and kept 0600, installed into the store once and marked as huddle's; a home with no import serves a leaf only its own root vouches for. |
| Imported certificate (`huddle cert --import`) | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/hub_role_and_import`; `internal/huddle/cli/cert_test.go:TestCertImportInstallsThePairTheOwnerHolds` | The imported pair is installed, named for the hub's host, and serves the TLS listener. |
| Phone trust (`huddle trust --phone`) | `internal/huddle/cli/trust_phone_test.go:TestTrustPhoneServesTheGuidePrintsTheLinkAndInstallsNothing`; `internal/huddle/phone/helper_test.go:TestTheHelperServesItsThreePathsAndRefusesEverythingElse`; `web/e2e/phone.spec.ts` "the guide shows the iOS steps, the fingerprint and the Android one hidden" | The guide serves the root, the profile and the QR link on loopback, prints the root's fingerprint, and installs nothing. |
| Invitations (mint, join, wrong and reused refused) | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName` (steps `invite_a`, `join_a`, `invite_b_join_b`, `invitations_are_one_use`); `web/e2e/invite.spec.ts` "the invite card creates a token and shows its join line" | A satellite invite joins once; a spent or made-up token is refused; the card shows the join line with a fresh token. |
| Pairing a browser | `web/e2e/pair.spec.ts` "a pair link pairs the device with a passkey and it appears in Settings"; `cmd/huddle/hub_e2e_test.go:TestE2EFreshInstallHubSatellitePWA/pair_passkey` | The pair link runs the passkey ceremony, the device lands in Settings, and the session survives a reload. |
| Passkey sign-in | `web/e2e/signin.spec.ts` "a paired browser signs back in with one passkey touch" and "a link sign-in offers a passkey, and the saved one signs in after cookies are cleared" | A paired browser signs back in with one touch after its cookies are gone; the landing's available call answers the booleans and the reason a refused cookie earned (`TestLoginAvailableNamesWhyTheSignInEnded`). |
| Landing page | `web/e2e/landing.spec.ts` "a fresh hub shows the first-time landing, its tour and its copyable commands"; `web/e2e/totp.spec.ts` "a phone browser is offered its lock as a passkey after a code sign-in" | A signed-out visit shows the sign-in card (the biometric button a saved passkey earns, the code field beside or instead of it), the start-a-session tabs and the four-step tour; the first-time card stands when nothing is enrolled. |
| First-run checklist | `web/e2e/landing.spec.ts` "the first-run checklist ticks from server state and stands down when all four hold" | The four items tick from server state (authenticator, session, channel, budget), the passkey row mints and saves its own passkey, and the panel hides once all four hold. |
| The hub's own dashboard link | `internal/huddle/cli/huddle_test.go:TestHuddleOpenPrintsTheHubsOwnLinkOnARoleHubHome`; `web/e2e/landing.spec.ts` "huddle open on a hub prints the hub's own link, and a browser opens it over TLS" | On a hub home every printed dashboard link names the hub's https origin; the hub listener redeems the one-time code itself and the session it earns passes its gate (solo and satellite keep the loopback link). |
| Session renewal (7 days idle) | `internal/huddle/server/signin_test.go:TestARenewalResendsTheCookie` | A request past the renewal hour re-sends the cookie with its seven-day life; a removed device answers 401 with nothing. |
| Session sync (two sessions, one channel) | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName` (steps `one_channel`, `a_posts_b_reads_once`, `b_posts_a_reads_once`) | Both sessions are tied to one channel and listed as present in it; each reads the other's digest exactly once and hears of the tie on its next prompt. |
| Now list | `web/e2e/now.spec.ts` "the sign-in link opens the hub and the Now list shows the seeded session"; `cmd/huddle/hub_e2e_test.go:TestE2EFreshInstallHubSatellitePWA/now_list` | The list names the live session under its machine, and one waiting on a permission sits in Needs you with its state word. |
| Radar requests count | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/radar_numbers`; `web/e2e/radar.spec.ts` "the requests table lists the seeded rows and the inspector opens one" | Radar answers the fixture's exact request count for today; the table lists each seeded row and the inspector shows one request's numbers. |
| Radar input, output, cache tokens and cost | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/radar_numbers`; `web/e2e/radar.spec.ts` "the overview's Day cards show the seeded totals" | The four token sums match the fixtures exactly, and the day's cost and spend match to the cent. |
| Usage and cost charts | `web/e2e/usage.spec.ts` "the usage view shows the seeded totals and splits the picked day"; `web/e2e/fulltest.spec.ts` "one signed-in walk over TLS covers every tab with no console error" | The tiles carry the seeded token totals, the daily chart draws one line per model with its tooltip, and the Costs series carries the seeded window. |
| Navigation of every view | `web/e2e/radar.spec.ts` "every radar view loads with no console error"; `web/e2e/radar-switch.spec.ts` "switching radar views in one document keeps a single app tree" | All eleven radar views load through the sub-tab row with no console error, and switching views keeps exactly one app tree. |
| Kanban view | `web/e2e/kanban.spec.ts` "the kanban tab lists the board and its three cards in the right columns" | The board names itself and holds each card in the column its folder put it in; the Holder filter narrows the board and Clear all restores it. |
| Layout at phone width | `web/e2e/layout.spec.ts` "every view lays out clean: dark phone" and "every view lays out clean: light phone" | The signed-out landing, every tab and every radar view opens at 390x844 with no console error, no sideways page scroll and no overlapping Kanban toolbar controls. |
| Radar settings writes from a paired device | `cmd/huddle/fulltest_e2e_test.go:TestFullScenarioOnALocalDevName/radar_writes` | A phone paired over TLS saves a budget through the Radar tab under the hub's own Origin; a foreign Origin gets 403. |
| The owner key of a serve no CLI started | `internal/huddle/cli/e2e_test.go:TestE2EAServeStartedByHandStillSignsTheOwnerIn`; `internal/huddle/cli/creds_test.go:TestTokenForReadsTheOwnerKeyOfThisHomesOwnLoopbackServer`; `internal/huddle/cli/e2e_test.go:TestE2EARootThatAgedOutOverNineRestartsStillOpensThroughTheOwnerKey` | A serve with no root handed to it leaves it 0600 in the home's run directory, never in its log; `huddle open --print` on the same machine prints a link with a code, a credential that aged out over nine restarts still opens through it, a file another user owns or others may read is refused with a line, and a clean stop takes it away. |

## Needs a human

- `claude rc` against a real Claude login: the suites feed hooks canned stdin and never run a live session.
- A public certificate: `huddle cert` with a real DNS provider token; the suites mint their own CA instead.
- Installing the local CA root into the OS trust store (`huddle trust`): tests run against a fake store; the
  password prompt needs the owner.
- Face ID / Touch ID on real hardware: the suites use a virtual authenticator and a soft key.
- TOTP sign-in with a real authenticator app on a phone: the suites compute codes from the secret.

## Live check

`sh scripts/live-e2e.sh` proves the path a first real use takes: it builds the binary, copies the plugin, and
runs two headless `claude` sessions in throwaway git projects — one runs `/huddle:setup` and prints the join
line, the other joins with it — then has them `ping` and `pong` through the huddle MCP tools and asserts both
messages against the channel's own event list. An opencode leg follows: the script writes a temp
`opencode.json` whose provider points at the router, runs `huddle harness install opencode`, joins the agent as
session `oc` with a fresh invite from proj-a, has it `ping` the Claude session and read the `pong` back through
the huddle MCP tools, asserts both events, and checks `huddle harness remove opencode` restores the temp config
byte for byte. It needs `claude`, `opencode` and a router already serving the zai models: the script probes
`$HUDDLE_LIVE_E2E_ROUTER` (the zai plugin router's loopback address when the variable is unset) for the zai
router's health line, and stops at that preflight when no router answers; with `opencode` off the
PATH the leg prints a `skip:` line and the claude steps still run. Every run gets its own temporary `HOME`,
`CLAUDE_CONFIG_DIR` and `XDG_*` directories, so the real `~/.claude`, `~/.config/huddle` and `~/.agents` are
never touched. `KEEP=1` keeps the temp dir and its logs. It is not part of CI.

## Live run

1. On the hub machine, read the home's `config.toml`: `role = "hub"`, the hub's `host` set to its name,
   `listen` holding 8443.
2. Install the pair you hold (mkcert, a company CA): `huddle cert --import cert.pem key.pem`.
3. Start the server (`huddle serve`) and wait for its `hub listener on https://...:8443` line.
4. Open <https://hub.raouf.test:8443> in a browser and sign in with the owner link or a paired passkey.
5. Walk each view: Now, Channels, every Radar view, Kanban, Settings.
6. Note anything off — a view that errors, a number that looks wrong — for the Report below.

## Report

Live run on 2026-10-18 against <https://hub.raouf.test:8443> (a hub on 127.0.0.2:8443 with an imported mkcert pair,
headless Chromium on the system's DNS and trust store, every home temporary).

What passed:

1. `huddle open --print` on the hub printed `https://hub.raouf.test:8443/?code=…`, and the link signed the browser in.
2. The first link showed the authenticator set-up; a code computed from the shown secret enrolled it, and the page
   showed 10 recovery codes once.
3. With cookies cleared, the landing page signed in with a fresh code, then offered Touch ID; a passkey saved there
   signed in again with one touch.
4. Now, Channels, Radar, Kanban and Settings all opened; Now showed the first-run checklist with the authenticator
   item ticked.
5. `huddle pair --satellite` minted an invite; `huddle join-hub https://hub.raouf.test:8443 <invite>` joined one
   machine over TLS, and the same invite was refused for a second one (exit 2).

What the run found and fixed:

- A hub started with `huddle serve` held a root no CLI had, so `huddle open` printed no code: the owner key fixes it.
- The setup link was refused on the hub's own name: the hub listener now accepts the one-time code.
- The signed-out page logged two 401s to the console, and an empty Now pointed at an empty list (UI-POLISH-2).

Still for the owner: the items under "Needs a human".
